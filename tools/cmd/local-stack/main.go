// Command local-stack runs the extension and its proxy as host processes on the
// local simulation, the way the e2e repository runs the standard TEE and proxy:
// no images, no compose. The proxy runs in this process; the extension TEE
// (tee-node with the Go extension) runs as a child process built from the module
// root, because that binary links the extension and the tools module must not.
// Redis is the only external service. By default it is the simulation's fourth
// instance, which e2e reserves for an extension proxy.
//
// Ports are the host-process block of tools/pkg/configs: the proxy binds 6683
// and 6684 directly, the TEE the ports fixed in cmd/start-tee, all clear of the
// e2e simulation's own processes. start-services.sh records the proxy URL for
// the follow-up scripts. The TEE takes CHAIN_ID and EXTENSION_ID from the
// environment this runner gives it.
//
// The proxy configuration is rendered from the local devnet template into out/:
// the redis address, the chain id reported by the RPC, and the
// FlareSystemsManager, Relay and VoterRegistry addresses from the addresses
// file replace the template's values, so a fresh simulation needs no editing.
package main

import (
	"bufio"
	"context"
	"flag"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"os/exec"
	"os/signal"
	"path/filepath"
	"regexp"
	"runtime"
	"strings"
	"syscall"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	proxyConfig "github.com/flare-foundation/tee-proxy/pkg/config"
	initProxy "github.com/flare-foundation/tee-proxy/pkg/init"
	"github.com/joho/godotenv"
	"github.com/redis/go-redis/v9"
)

// The simulation's redis instances sit on 6379–6382; the fourth is the one
// e2e's compose reserves for an extension proxy.
const defaultRedis = ":6382"

// Values the extension TEE child needs when the environment does not set them.
// GOVERNANCE_SIGNERS defaults to the owner, matching the compose stack and what
// set-governance publishes, so register-tee finds the same governance hash.
var childDefaults = map[string]string{
	"GOVERNANCE_THRESHOLD": "1",
	"LOG_LEVEL":            "INFO",
	"MODE":                 "1",
	"SIMULATED_TEE":        "true",
}

func main() {
	root := projectRoot()
	// The .env is read before the flags take their defaults from the environment;
	// the shared tools configuration captures the environment at program start,
	// which is too early for a command that loads .env itself.
	loadEnv(root)
	af := flag.String("a", envOrDefault("ADDRESSES_FILE", configs.AddressesFile), "file with deployed addresses (relative paths resolve against the module root)")
	cf := flag.String("c", envOrDefault("CHAIN_URL", configs.ChainNodeURL), "chain node url")
	redis := flag.String("redis", defaultRedis, "redis address for the proxy queue (host:port or :port)")
	template := flag.String("template", filepath.Join("deploy", "config", "proxy", "extension_proxy.docker.toml"),
		"proxy config template for the local devnet, rendered into out/ (relative to the module root)")
	extensionID := flag.String("extensionID", "", "extension id for the TEE and the extension, set as EXTENSION_ID in the child's environment (default: the build's)")
	flag.Parse()
	*af = rootRelative(root, *af)
	*template = rootRelative(root, *template)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	// --- Preflight: everything the two processes will need, checked up front. ---
	chainID, err := chainIDOf(ctx, *cf)
	if err != nil {
		logger.Fatalf("chain %s: %v", *cf, err)
	}
	if err := dialCheck(*redis); err != nil {
		logger.Fatalf("redis %s: %v (the simulation's compose publishes redis on 6379–6382)", *redis, err)
	}
	// The redis instance outlives this process, and a proxy that inherits a
	// previous instance's queues and signing-policy state serves stale answers;
	// the compose stack gets a fresh redis per run for the same reason. This
	// instance is reserved for one extension proxy, so it is emptied here.
	if err := flushRedis(ctx, *redis); err != nil {
		logger.Fatalf("emptying redis %s: %v", *redis, err)
	}
	addresses := map[string]string{}
	for key, name := range map[string]string{
		"flare_systems_manager": "FlareSystemsManager",
		"relay":                 "Relay",
		"voter_registry":        "VoterRegistry",
	} {
		addr, err := configs.ResolveContract("", *af, name)
		if err != nil {
			logger.Fatalf("addresses: %v", err)
		}
		addresses[key] = addr.Hex()
	}
	if _, err := exec.LookPath("go"); err != nil {
		logger.Fatalf("go is not on PATH; the extension TEE is built from %s", root)
	}
	if *extensionID == "" && os.Getenv("EXTENSION_ID") == "" {
		logger.Fatalf("EXTENSION_ID is not set and no -extensionID given; the TEE and the extension refuse to guess it")
	}
	if os.Getenv("PROXY_PRIVATE_KEY") == "" {
		logger.Fatalf("PROXY_PRIVATE_KEY is not set; the proxy signs with it")
	}
	owner, err := ownerAddress()
	if err != nil {
		logger.Fatalf("%v", err)
	}

	// --- Proxy configuration for this run. ---
	rendered := filepath.Join(root, "out", "extension_proxy.host.toml")
	externalPort, err := renderProxyConfig(*template, rendered, *redis, chainID, addresses)
	if err != nil {
		logger.Fatalf("proxy config: %v", err)
	}
	proxyURL := fmt.Sprintf("http://localhost:%s", externalPort)
	// The proxy panics on a config it cannot read; read it here so a stale
	// template fails before anything is built or started.
	if _, err := proxyConfig.Read(rendered); err != nil {
		logger.Fatalf("proxy config %s: %v", rendered, err)
	}

	// --- Extension TEE: built from the module root, run as a child. ---
	binary := filepath.Join(root, "out", "bin", "start-tee")
	logger.Infof("building %s (%s)", binary, buildMode(root))
	if err := run(root, "go", "build", "-o", binary, "./cmd/start-tee"); err != nil {
		logger.Fatalf("building start-tee: %v", err)
	}

	env := append(os.Environ(),
		"CHAIN_ID="+fmt.Sprint(chainID),
		"INITIAL_OWNER="+owner.Hex(),
	)
	if os.Getenv("GOVERNANCE_SIGNERS") == "" {
		env = append(env, "GOVERNANCE_SIGNERS="+owner.Hex())
	}
	for key, value := range childDefaults {
		if os.Getenv(key) == "" {
			env = append(env, key+"="+value)
		}
	}
	if *extensionID != "" {
		env = append(env, "EXTENSION_ID="+*extensionID)
	}
	tee := exec.Command(binary)
	tee.Dir = root
	tee.Env = env
	teeExit := make(chan error, 1)
	if err := startPrefixed(tee, "tee"); err != nil {
		logger.Fatalf("starting the extension TEE: %v", err)
	}
	go func() { teeExit <- tee.Wait() }()
	logger.Infof("extension TEE running as pid %d (owner %s, governance %s/%s)",
		tee.Process.Pid, owner.Hex(), envOr(env, "GOVERNANCE_SIGNERS"), envOr(env, "GOVERNANCE_THRESHOLD"))

	// --- Proxy, in this process. ---
	// Init serves until the context is cancelled and reports failures by
	// panicking, so it runs on its own goroutine; the child must not outlive it.
	go func() {
		defer func() {
			if r := recover(); r != nil {
				terminate(tee, teeExit)
				logger.Fatalf("starting the proxy: %v", r)
			}
		}()
		initProxy.Init(ctx, rendered)
	}()
	logger.Infof("extension proxy starting from %s", rendered)

	// The proxy first waits for the chain indexer's database to be within a
	// minute of the chain, sleeping between checks, so on a testnet its first
	// answer can be minutes away; start-services.sh checks the lag up front.
	if err := waitForURL(ctx, proxyURL+"/info", 5*time.Minute); err != nil {
		terminate(tee, teeExit)
		logger.Fatalf("proxy did not come up: %v", err)
	}
	report(proxyURL)

	select {
	case <-ctx.Done():
		logger.Info("shutting down")
		terminate(tee, teeExit)
	case err := <-teeExit:
		logger.Fatalf("the extension TEE exited: %v", err)
	}
}

// report prints what the follow-up scripts need: the machine identity the
// proxy exposes, and the commands that continue the local flow.
func report(proxyURL string) {
	// The proxy answers /info before it has installed the initial signing
	// policy on the node; give it a moment so the summary shows the real hash.
	var info *types.SignedTeeInfoResponse
	deadline := time.Now().Add(30 * time.Second)
	for {
		var err error
		info, err = fccutils.TeeInfo(proxyURL)
		if err != nil {
			logger.Warnf("reading %s/info: %v", proxyURL, err)
			return
		}
		if info.TeeInfo.LastSigningPolicyHash != (common.Hash{}) || time.Now().After(deadline) {
			break
		}
		time.Sleep(time.Second)
	}
	teeID, proxyID, err := fccutils.TeeProxyId(info)
	if err != nil {
		logger.Warnf("reading identities from /info: %v", err)
		return
	}
	fmt.Printf(`
Extension stack running as host processes
  proxy               %s
  TEE id              %s
  proxy id            %s
  extension id        %s
  signing policy      %s

Continue with the proxy URL of this stack:
  EXT_PROXY_URL=%s ./deploy/scripts/post-build.sh
  EXT_PROXY_URL=%s ./deploy/scripts/publish-local.sh endpoints
  EXT_PROXY_URL=%s ./deploy/scripts/publish-local.sh admins
  ./deploy/scripts/oracle-cycle.sh -tee %s --dry-run

Ctrl-C stops both processes.
`, proxyURL, teeID.Hex(), proxyID.Hex(), info.MachineData.ExtensionID.Big(),
		info.TeeInfo.LastSigningPolicyHash.Hex(), proxyURL, proxyURL, proxyURL, teeID.Hex())
}

// renderProxyConfig writes the template with this run's redis address, chain id,
// contract addresses and the host port block, and returns the external port.
func renderProxyConfig(template, dst, redis string, chainID uint64, addresses map[string]string) (string, error) {
	raw, err := os.ReadFile(template)
	if err != nil {
		return "", err
	}
	text := string(raw)
	replace := func(key, value string) error {
		re := regexp.MustCompile(`(?m)^` + regexp.QuoteMeta(key) + `\s*=.*$`)
		if !re.MatchString(text) {
			return fmt.Errorf("%s has no %s line", template, key)
		}
		text = re.ReplaceAllString(text, key+" = "+value)
		return nil
	}
	if err := replace("redis_port", fmt.Sprintf("%q", redis)); err != nil {
		return "", err
	}
	if err := replace("chain_id", fmt.Sprint(chainID)); err != nil {
		return "", err
	}
	for key, addr := range addresses {
		if err := replace(key, fmt.Sprintf("%q", addr)); err != nil {
			return "", err
		}
	}
	// The template reaches infrastructure through the compose network; host
	// processes reach it on localhost.
	text = strings.ReplaceAll(text, `"host.docker.internal"`, `"localhost"`)

	// The template carries the container's ports; the host stack binds its own
	// block, the one cmd/start-tee points the node at.
	if err := replace("internal", fmt.Sprintf("%q", fmt.Sprint(configs.ExtProxyInternalPort))); err != nil {
		return "", err
	}
	if err := replace("external", fmt.Sprintf("%q", fmt.Sprint(configs.ExtProxyExternalPort))); err != nil {
		return "", err
	}
	external := fmt.Sprint(configs.ExtProxyExternalPort)
	if err := os.MkdirAll(filepath.Dir(dst), 0o755); err != nil {
		return "", err
	}
	return external, os.WriteFile(dst, []byte(text), 0o644)
}

// ownerAddress is INITIAL_OWNER, or the address of DEPLOYMENT_PRIVATE_KEY, the
// same derivation cmd/start-tee applies. It is also the default governance
// signer, so it is resolved here rather than left to the child.
func ownerAddress() (common.Address, error) {
	if owner := os.Getenv("INITIAL_OWNER"); owner != "" {
		if !common.IsHexAddress(owner) {
			return common.Address{}, fmt.Errorf("INITIAL_OWNER %q is not an address", owner)
		}
		return common.HexToAddress(owner), nil
	}
	key := strings.TrimPrefix(strings.TrimPrefix(os.Getenv("DEPLOYMENT_PRIVATE_KEY"), "0x"), "0X")
	if key == "" {
		return common.Address{}, fmt.Errorf("set INITIAL_OWNER or DEPLOYMENT_PRIVATE_KEY; the TEE needs its initial owner")
	}
	priv, err := crypto.HexToECDSA(key)
	if err != nil {
		return common.Address{}, fmt.Errorf("DEPLOYMENT_PRIVATE_KEY: %v", err)
	}
	return crypto.PubkeyToAddress(priv.PublicKey), nil
}

func chainIDOf(ctx context.Context, url string) (uint64, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	client, err := ethclient.DialContext(ctx, url)
	if err != nil {
		return 0, err
	}
	defer client.Close()
	id, err := client.ChainID(ctx)
	if err != nil {
		return 0, err
	}
	return id.Uint64(), nil
}

// flushRedis empties the database the proxy is about to use. Plain redis, no
// auth: that is what the simulation runs.
func flushRedis(ctx context.Context, addr string) error {
	if strings.HasPrefix(addr, ":") {
		addr = "localhost" + addr
	}
	client := redis.NewClient(&redis.Options{Addr: addr})
	defer func() { _ = client.Close() }()
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	return client.FlushDB(ctx).Err()
}

func dialCheck(addr string) error {
	conn, err := net.DialTimeout("tcp", addr, 2*time.Second)
	if err != nil {
		return err
	}
	return conn.Close()
}

func waitForURL(ctx context.Context, url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	client := &http.Client{Timeout: 2 * time.Second}
	for {
		resp, err := client.Get(url)
		if err == nil {
			_ = resp.Body.Close()
			if resp.StatusCode == http.StatusOK {
				return nil
			}
		}
		if time.Now().After(deadline) {
			if err == nil {
				return fmt.Errorf("%s answered %d", url, resp.StatusCode)
			}
			return err
		}
		select {
		case <-ctx.Done():
			return ctx.Err()
		case <-time.After(500 * time.Millisecond):
		}
	}
}

// buildMode says which tee-node the child is compiled against: the sibling
// checkouts when start-services.sh has written a go.work, else the pinned module.
func buildMode(root string) string {
	if _, err := os.Stat(filepath.Join(root, "go.work")); err == nil {
		return "go.work present: sibling tee-node checkout"
	}
	return "no go.work: pinned tee-node from go.mod"
}

func run(dir, name string, args ...string) error {
	cmd := exec.Command(name, args...)
	cmd.Dir = dir
	cmd.Stdout = os.Stdout
	cmd.Stderr = os.Stderr
	return cmd.Run()
}

// startPrefixed starts cmd with both output streams copied to stdout, each line
// tagged, so the child's log stays distinguishable from the proxy's.
func startPrefixed(cmd *exec.Cmd, tag string) error {
	stdout, err := cmd.StdoutPipe()
	if err != nil {
		return err
	}
	stderr, err := cmd.StderrPipe()
	if err != nil {
		return err
	}
	if err := cmd.Start(); err != nil {
		return err
	}
	copyLines := func(r io.Reader) {
		scanner := bufio.NewScanner(r)
		scanner.Buffer(make([]byte, 0, 64*1024), 1024*1024)
		for scanner.Scan() {
			fmt.Printf("%s | %s\n", tag, scanner.Text())
		}
		if err := scanner.Err(); err != nil {
			logger.Warnf("%s output: %v", tag, err)
		}
	}
	go copyLines(stdout)
	go copyLines(stderr)
	return nil
}

// terminate asks the child to stop and gives it a few seconds before killing it.
func terminate(cmd *exec.Cmd, exited <-chan error) {
	if cmd.Process == nil {
		return
	}
	_ = cmd.Process.Signal(syscall.SIGTERM)
	select {
	case <-exited:
	case <-time.After(5 * time.Second):
		_ = cmd.Process.Kill()
		<-exited
	}
}

func envOr(env []string, key string) string {
	for i := len(env) - 1; i >= 0; i-- {
		if value, ok := strings.CutPrefix(env[i], key+"="); ok {
			return value
		}
	}
	return ""
}

func envOrDefault(key, fallback string) string {
	if value := os.Getenv(key); value != "" {
		return value
	}
	return fallback
}

// rootRelative resolves a relative path against the module root, which is how
// the scripts and .env spell paths, unless it exists from the working directory.
func rootRelative(root, path string) string {
	if filepath.IsAbs(path) {
		return path
	}
	if _, err := os.Stat(path); err == nil {
		return path
	}
	return filepath.Join(root, path)
}

func projectRoot() string {
	_, thisFile, _, _ := runtime.Caller(0)
	return filepath.Join(filepath.Dir(thisFile), "..", "..", "..")
}

// loadEnv reads the project root .env the way the other tools do; variables
// already in the environment win.
func loadEnv(root string) {
	if err := godotenv.Load(filepath.Join(root, ".env")); err != nil {
		if err := godotenv.Load(); err != nil {
			logger.Warnf("no .env loaded: %v", err)
		}
	}
}
