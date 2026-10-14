// Command keeper drives the USDX oracle price feed on a timer: request a feed
// update from the configured machines, poll the proxy for the signed result,
// relay it to TeeOracleFeedStore, and emit one JSON record per cycle.
//
//	go run ./cmd/keeper -interval 10m               # targets read from the chain every cycle
//	go run ./cmd/keeper -tee @a,@b,@c -interval 10m # a fixed set of targets
//	go run ./cmd/keeper -once                       # one cycle, for cron
//
// The store resolves from -store or the TeeOracleFeedStore entry in the
// addresses file; the instructions sender is derived from the store's own
// instructionsSender() unless given explicitly with -instructionSender.
//
// Every cycle requests through requestFeedUpdate(feedId, teeIds) on the
// instruction sender, paying the registry fee for one instruction addressed to
// every named machine. Without -tee the keeper picks the targets itself, every
// cycle: the machines the registry lists as active for the store's extension,
// less any the sender reports behind the feed's latest endpoints or admins
// publication — the request reverts as a whole if it names a machine that is
// paused or not yet pushed the configuration. A machine that restarts under a
// new id, or is paused, therefore drops in and out without a restart. With
// -tee the set is fixed, and naming a machine that is no longer active stops
// every request.
//
// The result is polled on the -p proxy and every registered machine's proxy
// until each target has answered or -pollTimeout passes. Before paying, the
// keeper asks those proxies over GET_READINESS whether their machines would
// sign at all — a fee spent on a machine with a stale observation buys a
// predictable failure.
//
// The keeper holds no authority, so a compromised one costs gas, not
// correctness. requestFeedUpdate and submitFeedUpdates are both permissionless,
// and the store verifies every TEE signature against the machine registry
// itself, so the address here only ever spends its own gas: give it KEEPER_KEY,
// a funded key and nothing more. Run at least two keepers: their submissions
// collide harmlessly, because the store keeps only a strictly newer
// observedAt, and a keeper whose batch has been overtaken records race_lost. It
// does not alert — paging on the records, and on on-chain feed age, is the
// monitoring layer's job.
package main

import (
	"context"
	"crypto/ecdsa"
	"flag"
	"math/big"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/params"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL,
		"extension proxy url; set explicitly it OVERRIDES the url the registry publishes for every "+
			"machine (right only with one machine), otherwise each machine is reached at its own "+
			"registered url and this is only the fallback when the registry lists none")
	senderF := flag.String("instructionSender", "",
		"TeeOracleInstructionsSender address; optional — when omitted it is derived "+
			"from the store's instructionsSender() pointer")
	storeF := flag.String("store", "",
		"TeeOracleFeedStore address (default: the TeeOracleFeedStore entry in the addresses file)")
	feedIDF := flag.String("feedId", configs.FeedID,
		"feed id (bytes21 hex); defaults to USDX/USD")
	teeF := flag.String("tee", "",
		"comma-separated TEE machine addresses to request from; when omitted, every cycle requests "+
			"from the extension's active machines at the feed's latest configuration, read from the chain")
	keeperIDF := flag.String("keeperId", defaultKeeperID(),
		"stable identifier for this instance, so records from ≥2 keepers can be told apart")
	intervalF := flag.Duration("interval", 10*time.Minute,
		"how often to start a cycle; match the ~10-minute source refresh")
	pollF := flag.Duration("pollTimeout", 5*time.Minute,
		"how long to wait for a terminal action result")
	submitF := flag.Duration("submitTimeout", 3*time.Minute,
		"how long to wait for the submitFeedUpdates receipt")
	recordsF := flag.String("records", "",
		"append JSON cycle records to this file (default: stdout)")
	bondsLogF := flag.String("bondsLog", "",
		"at the start of every cycle, fetch the public Hex bonds endpoint the machines read and append "+
			"its answer to this file, one JSON line per fetch — Hex keeps no public history of the "+
			"figure (default: off)")
	onceF := flag.Bool("once", false, "run a single cycle and exit — useful for a cron-driven keeper")
	requestF := flag.Duration("requestTimeout", keeper.DefaultRequestTimeout,
		"how long to wait for the requestFeedUpdate receipt before giving up on the cycle")
	minBalanceF := flag.String("minBalance", "5",
		"warn when the paying account falls below this many coins; 0 disables the check")
	dryRunF := flag.Bool("dry-run", false,
		"run the cycle through the on-chain preflight and stop before submitting; the request is still sent and paid for")
	apiKeyF := flag.String("directApiKey", "",
		"proxy API key for the GET_READINESS preflight, which is skipped without one "+
			"(default: $DIRECT_API_KEY)")
	flag.Parse()
	// Read after parsing rather than as the flag's default, so -h never prints the key.
	if *apiKeyF == "" {
		*apiKeyF = os.Getenv("DIRECT_API_KEY")
	}

	// Which flags were given by hand — -p means one thing as an override and
	// another as a fallback.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if *senderF != "" && !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("%q is not an address", *senderF))
	}
	if *apiKeyF == "" {
		// Not fatal: the preflight is a saving, not a requirement. Warned about
		// because the symptom otherwise is fees quietly spent on refusals.
		logger.Warnf("no API key: every cycle pays the registry fee without asking the machines " +
			"first whether they would sign — pass -directApiKey or set DIRECT_API_KEY")
	}

	minBalance, err := parseCoins(*minBalanceF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// Resolved before the client, so a keeper can run with KEEPER_KEY alone and
	// no EXTENSION_OWNER_KEY anywhere on the machine.
	prv, keySource, err := keeperKey()
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	// Logged before the client is opened: when a node is unreachable the first
	// question is which address was about to pay.
	logger.Infof("paying from %s (%s)", crypto.PubkeyToAddress(prv.PublicKey).Hex(), keySource)

	s, err := support.SupportWithKey(*af, *cf, prv)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "TeeOracleFeedStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// The instructions sender is derived from the store's own pointer unless
	// given explicitly — the store's instructionsSender() is the single source
	// of truth, and the same address the preflight checks. Keeping it optional
	// lets operations pass only -store.
	var senderAddr common.Address
	if *senderF != "" {
		senderAddr = common.HexToAddress(*senderF)
	} else {
		store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
		if err != nil {
			fccutils.FatalWithCause(err)
		}
		senderAddr, err = store.InstructionsSender(nil)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("deriving the instructions sender from the store: %s", err))
		}
		logger.Infof("derived instructions sender from store: %s", senderAddr.Hex())
	}

	feedRaw := common.FromHex(*feedIDF)
	if len(feedRaw) != 21 {
		fccutils.FatalWithCause(errors.Errorf("feedId %q is not 21 bytes", *feedIDF))
	}
	var feed [21]byte
	copy(feed[:], feedRaw)

	// Empty is not an error: the keeper then picks the targets each cycle.
	teeIDs, err := parseTeeIDs(*teeF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	if set["tee"] && len(teeIDs) == 0 {
		fccutils.FatalWithCause(errors.New("-tee names no machine; omit it to let the keeper pick the targets"))
	}

	records, closeRecords, err := openRecords(*recordsF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	defer closeRecords()

	var bonds *keeper.BondsLog
	if *bondsLogF != "" {
		bondsFile, closeBonds, err := openRecords(*bondsLogF)
		if err != nil {
			fccutils.FatalWithCause(err)
		}
		defer closeBonds()
		bonds = keeper.NewBondsLog(bondsFile, keeper.HexBondsURL, *keeperIDF)
		logger.Infof("recording %s to %s every cycle", keeper.HexBondsURL, *bondsLogF)
	}

	// -p overrides every machine's registered url only when it was given by
	// hand: its default is an env-driven fallback, and silently treating that as
	// an override would send every machine's request to one proxy.
	proxyOverride := ""
	if set["p"] {
		proxyOverride = *pf
	}

	cfg := keeper.LoopConfig{
		Config: keeper.Config{
			KeeperID:       *keeperIDF,
			Sender:         senderAddr,
			Store:          storeAddr,
			FeedID:         feed,
			TeeIDs:         teeIDs,
			Proxy:          *pf,
			DirectAPIKey:   *apiKeyF,
			PollTimeout:    *pollF,
			SubmitTimeout:  *submitF,
			RequestTimeout: *requestF,
			MinBalance:     minBalance,
			DryRun:         *dryRunF,
		},
		Interval:      *intervalF,
		ProxyOverride: proxyOverride,
		Emitter:       keeper.NewEmitter(records),
		Bonds:         bonds,
	}

	// Cancelling lets an in-flight cycle unwind at its next checkpoint rather
	// than being killed mid-submission.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()
	// Restoring the default behaviour once the first signal has landed: a second
	// one then kills a process that is still winding down, instead of being
	// swallowed by the handler that is no longer listening for anything.
	go func() {
		<-ctx.Done()
		stop()
	}()

	if *onceF {
		rec, err := keeper.RunOnce(ctx, s, cfg)
		cfg.Emitter.Emit(rec)
		if err != nil && !rec.Outcome.Advanced() {
			fccutils.FatalWithCause(err)
		}
		logger.Infof("cycle finished: %s", rec.Outcome)
		return
	}

	if err := keeper.Run(ctx, s, cfg); err != nil {
		fccutils.FatalWithCause(err)
	}
}

// keeperKey resolves the key the keeper pays with. KEEPER_KEY first, because
// relaying needs no authority at all: both of the keeper's transactions are
// permissionless, so this address can be a throwaway that holds nothing but
// gas, and a server that is compromised loses exactly that. Falling back to
// EXTENSION_OWNER_KEY keeps existing deployments running, and says what it is
// doing — that key can also configure the extension and allow code hashes, none
// of which this process ever needs.
func keeperKey() (*ecdsa.PrivateKey, string, error) {
	key, err := fccutils.OverrideKey("KEEPER_KEY")
	if err != nil {
		return nil, "", err
	}
	if key != nil {
		return key, "KEEPER_KEY", nil
	}

	key, err = support.DefaultPrivateKey()
	if err != nil {
		return nil, "", err
	}
	logger.Warnf("KEEPER_KEY is not set, so this keeper signs with EXTENSION_OWNER_KEY — " +
		"a key that also owns the extension, on a host that only needs to pay gas")
	return key, "EXTENSION_OWNER_KEY", nil
}

// parseCoins reads a balance floor given in whole coins into wei.
func parseCoins(s string) (*big.Int, error) {
	amount, ok := new(big.Float).SetString(strings.TrimSpace(s))
	if !ok || amount.Sign() < 0 {
		return nil, errors.Errorf("-minBalance %q is not a non-negative number of coins", s)
	}
	wei, _ := new(big.Float).Mul(amount, big.NewFloat(params.Ether)).Int(nil)
	return wei, nil
}

// openRecords defaults to stdout, which a process supervisor captures.
func openRecords(path string) (*os.File, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, errors.Errorf("creating the directory of %s: %s", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, errors.Errorf("opening %s: %s", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// defaultKeeperID falls back to the hostname, distinct per instance.
func defaultKeeperID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "keeper"
}

// parseTeeIDs splits a comma-separated list of hex addresses, trimming
// spaces and ignoring empty entries.
func parseTeeIDs(s string) ([]common.Address, error) {
	out := make([]common.Address, 0, strings.Count(s, ",")+1)
	for raw := range strings.SplitSeq(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !common.IsHexAddress(raw) {
			return nil, errors.Errorf("%q is not a hex address", raw)
		}
		out = append(out, common.HexToAddress(raw))
	}
	return out, nil
}
