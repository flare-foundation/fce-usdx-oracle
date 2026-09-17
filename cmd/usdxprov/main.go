// Command usdxprov builds, signs and submits provisioning envelopes.
//
//	usdxprov build   fetch the TEE key, encrypt, emit an unsigned envelope
//	usdxprov sign    one admin signs the envelope body
//	usdxprov submit  combine signatures and POST
//
// Three subcommands because signing involves different people. The unsigned
// envelope is safe to pass around: the credential inside is already ciphertext
// only the target enclave can open.
//
// It lives in the extension's module so it imports internal/provisioning/envelope directly
// — this and the verifier share one EncodeBody and cannot drift.
package main

import (
	"bytes"
	"crypto/ecdsa"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"io"
	"math/big"
	"net/http"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/provisioning/envelope"
	"fce-usdx-oracle/internal/provisioning/store"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// envelopeFile is the on-disk intermediate passed between the subcommands.
type envelopeFile struct {
	ChainID     string   `json:"chainId"`
	TeeID       string   `json:"teeId"`
	Ref         string   `json:"ref"`
	Version     uint64   `json:"version"`
	IssuedAt    uint64   `json:"issuedAt"`
	Ciphertext  string   `json:"ciphertext"`
	Body        string   `json:"body"`
	Signatures  []string `json:"signatures"`
	ProxyPubKey string   `json:"proxyPublicKey"`
}

func main() {
	if len(os.Args) < 2 {
		usage()
	}
	var err error
	switch os.Args[1] {
	case "build":
		err = build(os.Args[2:])
	case "commit":
		err = commit(os.Args[2:])
	case "sign":
		err = sign(os.Args[2:])
	case "submit":
		err = submit(os.Args[2:])
	default:
		usage()
	}
	if err != nil {
		fmt.Fprintf(os.Stderr, "error: %v\n", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `usdxprov — build, sign and submit provisioning envelopes

  usdxprov build  -url <proxy> -chain-id <n> -ref <name> -value <secret> [-version <n>] [-tee <id>] -out env.json
  usdxprov commit                      print the urlHash a SET_ENDPOINTS publishes for the cash URL
  usdxprov sign   -in env.json -key <hex-private-key> [-out env.json]
  usdxprov submit -in env.json -url <proxy> [-api-key <k>] [-wait <d>] [-action-url <url>]

Always pass -tee for a real credential. build encrypts to whatever public key the
proxy's /info serves, and -tee is what makes it refuse a substituted one; take the
value from the attestation, with "verify-tee -print-tee-id". The upload scripts do
this for you.

The `+config.HexCashURLRef+` credential is not a string but abi.encode(salt, url), the
preimage of the urlHash the hex_cash group publishes. build takes it from HEX_CASH_URL
and HEX_CASH_SALT rather than from a value, and commit prints the matching urlHash
from the same two variables.

The freshness window is `+envelope.Freshness.String()+`: sign and submit within it, or
the envelope is rejected as stale.

Two role-tagged signer sets are published per machine, and the ref decides which
one applies: `+strings.Join(config.BackingRefs(), ", ")+`
are authorised by the backing set (at least `+fmt.Sprint(config.MinBackingThreshold)+` signature), every other ref by
the providers set (at least `+fmt.Sprint(config.MinProviderThreshold)+` DISTINCT signatures). The exact thresholds
are whatever SET_ADMINS published, and only the target machine can tell you
whether an envelope satisfies them.
`)
	os.Exit(2)
}

// privateURL validates the cash URL and its salt as the store will see them.
// The salt is what keeps the on-chain commitment from being brute-forced back
// to the URL, so a zero or short one is refused here rather than published.
func privateURL(rawURL, rawSalt string) (store.PrivateURL, error) {
	if rawURL == "" || rawSalt == "" {
		return store.PrivateURL{}, fmt.Errorf(
			"HEX_CASH_URL and HEX_CASH_SALT are both required for %s", config.HexCashURLRef)
	}
	if !strings.HasPrefix(rawURL, "https://") {
		return store.PrivateURL{}, fmt.Errorf("HEX_CASH_URL must use https")
	}
	saltBytes, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(rawSalt, "0x"), "0X"))
	if err != nil || len(saltBytes) != 32 {
		return store.PrivateURL{}, fmt.Errorf("HEX_CASH_SALT must be 32 bytes of hex (openssl rand -hex 32)")
	}
	salt := common.BytesToHash(saltBytes)
	if salt == (common.Hash{}) {
		return store.PrivateURL{}, fmt.Errorf("HEX_CASH_SALT is zero")
	}
	return store.PrivateURL{Salt: salt, Url: rawURL}, nil
}

// --- commit ---

// commit prints the urlHash the hex_cash group of a SET_ENDPOINTS must carry for
// HEX_CASH_URL under HEX_CASH_SALT: keccak256 of the very blob build provisions
// for hex_cash_url. Stdout carries the hash and nothing else, so a script can
// substitute it into an endpoints configuration.
func commit(args []string) error {
	fs := flags("commit", args, map[string]*string{}, nil)
	cashURL := fs.secret("private-url", "HEX_CASH_URL", "the restricted cash URL")
	cashSalt := fs.secret("salt", "HEX_CASH_SALT", "32-byte hex salt of the commitment")
	if err := fs.parse(); err != nil {
		return err
	}
	u, err := privateURL(cashURL(), cashSalt())
	if err != nil {
		return err
	}
	h, err := store.CommitPrivateURL(u)
	if err != nil {
		return fmt.Errorf("committing the cash URL: %w", err)
	}
	fmt.Println(h.Hex())
	return nil
}

// --- build ---

func build(args []string) error {
	fs := flags("build", args, map[string]*string{}, nil)
	proxyURL := fs.str("url", "", "proxy or mocknode base URL")
	chainID := fs.i64("chain-id", 0, "target chain id")
	ref := fs.str("ref", "", "secret name, e.g. hex_api_key")
	value := fs.secret("value", "USDXPROV_VALUE", "the credential itself")
	version := fs.u64("version", 1, "must strictly increase per ref")
	out := fs.str("out", "envelope.json", "output file")
	teeIDFlag := fs.str("tee", "",
		"the teeId the ATTESTATION derived (verify-tee -print-tee-id); "+
			"build refuses to encrypt if /info serves a key deriving to anything else")
	cashURL := fs.secret("private-url", "HEX_CASH_URL",
		"the restricted cash URL, the value of -ref "+config.HexCashURLRef)
	cashSalt := fs.secret("salt", "HEX_CASH_SALT",
		"32-byte hex salt of the cash URL commitment")
	if err := fs.parse(); err != nil {
		return err
	}

	// Every credential but one is the string the operator holds. The cash URL is
	// provisioned as its commitment preimage, abi.encode(salt, url), which no
	// environment variable can carry (it contains zero bytes), so it is built
	// here from the URL and the salt, and a value given for it is refused rather
	// than encrypted as a string the enclave would reject.
	var credential []byte
	if *ref == config.HexCashURLRef {
		if value() != "" {
			return fmt.Errorf("%s is derived from HEX_CASH_URL and HEX_CASH_SALT, not given as a value",
				config.HexCashURLRef)
		}
		u, err := privateURL(cashURL(), cashSalt())
		if err != nil {
			return err
		}
		if credential, err = store.EncodePrivateURL(u); err != nil {
			return fmt.Errorf("encoding the cash URL: %w", err)
		}
	} else {
		credential = []byte(value())
	}
	if *proxyURL == "" || *ref == "" || len(credential) == 0 || *chainID == 0 {
		return fmt.Errorf("-url, -chain-id, -ref and a value are required")
	}

	pub, teeID, err := fetchKey(*proxyURL)
	if err != nil {
		return err
	}

	// -tee asserts which machine answered; it never overrides what /info served, which
	// would send the plaintext to whoever answered the port. verify-tee runs a separate
	// request, so it cannot speak for the key this call fetched — hence the flag.
	if *teeIDFlag != "" {
		want := common.HexToAddress(*teeIDFlag)
		if want == (common.Address{}) {
			return fmt.Errorf("-tee is not a usable address")
		}
		if want != teeID {
			return fmt.Errorf(
				"%s served a public key deriving to %s, but -tee names %s — "+
					"the key was substituted between verification and now; "+
					"do NOT send a credential to this endpoint",
				strings.TrimRight(*proxyURL, "/"), teeID.Hex(), want.Hex())
		}
	}
	if teeID == (common.Address{}) {
		return fmt.Errorf("could not determine teeId; pass -tee")
	}

	// Encrypt to the enclave. From here the credential exists in plaintext only
	// inside the target TEE.
	ciphertext, err := ecies.Encrypt(cryptoRand(), ecies.ImportECDSAPublic(pub), credential, nil, nil)
	if err != nil {
		return fmt.Errorf("encrypting to the TEE key: %w", err)
	}

	env := envelope.Envelope{
		ChainID:  big.NewInt(*chainID),
		TeeID:    teeID,
		Version:  *version,
		IssuedAt: uint64(time.Now().UTC().Unix()),
		Ref:      *ref,
		Payload:  ciphertext,
	}

	body, err := envelope.EncodeBody(env)
	if err != nil {
		return err
	}

	f := envelopeFile{
		ChainID:     env.ChainID.String(),
		TeeID:       teeID.Hex(),
		Ref:         env.Ref,
		Version:     env.Version,
		IssuedAt:    env.IssuedAt,
		Ciphertext:  "0x" + hex.EncodeToString(ciphertext),
		Body:        "0x" + hex.EncodeToString(body),
		ProxyPubKey: "0x" + hex.EncodeToString(crypto.FromECDSAPub(pub)),
	}
	if err := writeJSON(*out, f); err != nil {
		return err
	}

	fmt.Printf("envelope written to %s\n", *out)
	fmt.Printf("  teeId    %s\n", teeID.Hex())
	fmt.Printf("  ref      %s\n", env.Ref)
	fmt.Printf("  version  %d\n", env.Version)
	fmt.Printf("  expires  %s (%s from now)\n",
		time.Unix(int64(env.IssuedAt), 0).Add(envelope.Freshness).UTC().Format(time.RFC3339),
		envelope.Freshness)
	fmt.Printf("\nRef %q is authorised by the %s set: at least %d distinct signature(s) —\n",
		env.Ref, setNameFor(env.Ref), config.AdminPolicy().MinThresholdFor(env.Ref))
	fmt.Printf("the published threshold for this machine may be higher. The credential is\n")
	fmt.Printf("already encrypted, so this file is safe to send to the signers.\n")
	return nil
}

// --- sign ---

func sign(args []string) error {
	fs := flags("sign", args, nil, nil)
	in := fs.str("in", "envelope.json", "envelope file")
	keyHex := fs.secret("key", "USDXPROV_KEY", "admin private key, hex")
	out := fs.str("out", "", "output file (defaults to -in)")
	if err := fs.parse(); err != nil {
		return err
	}
	adminKey := keyHex()
	if adminKey == "" {
		return fmt.Errorf("-key is required")
	}

	var f envelopeFile
	if err := readJSON(*in, &f); err != nil {
		return err
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(adminKey, "0x"))
	if err != nil {
		return fmt.Errorf("bad admin key")
	}

	body, err := hexBytes(f.Body)
	if err != nil {
		return err
	}

	// EIP-191 personal-sign, so a hardware wallet can produce the same thing.
	sig, err := crypto.Sign(accounts.TextHash(crypto.Keccak256(body)), key)
	if err != nil {
		return err
	}
	sig[64] += 27

	signer := crypto.PubkeyToAddress(key.PublicKey)
	for _, existing := range f.Signatures {
		raw, err := hexBytes(existing)
		if err != nil {
			continue
		}
		if recovered, ok := recoverSigner(body, raw); ok && recovered == signer {
			return fmt.Errorf("%s has already signed; M-of-N needs DISTINCT signers", signer.Hex())
		}
	}

	f.Signatures = append(f.Signatures, "0x"+hex.EncodeToString(sig))

	dest := *out
	if dest == "" {
		dest = *in
	}
	if err := writeJSON(dest, f); err != nil {
		return err
	}

	// No local admin-set check: the set lives in the enclave, and a local copy would
	// go stale on the first rotation. The machine is the authority.
	fmt.Printf("signed by %s (%d signature(s) so far)\n", signer.Hex(), len(f.Signatures))
	return nil
}

// --- submit ---

func submit(args []string) error {
	fs := flags("submit", args, nil, nil)
	in := fs.str("in", "envelope.json", "envelope file")
	proxyURL := fs.str("url", "", "proxy base URL; POSTs to /direct")
	apiKey := fs.secret("api-key", "USDXPROV_API_KEY", "proxy API key")
	actionURL := fs.str("action-url", "",
		"post straight to an extension's /action instead of the proxy (local testing)")
	wait := fs.fs.Duration("wait", 60*time.Second,
		"how long to wait for the machine's own result; 0 returns as soon as the proxy accepts")
	if err := fs.parse(); err != nil {
		return err
	}

	var f envelopeFile
	if err := readJSON(*in, &f); err != nil {
		return err
	}
	// The floor for the set this ref falls under, not the machine's actual threshold:
	// fewer than this can never be accepted anywhere, so catch it before the trip.
	floor := config.AdminPolicy().MinThresholdFor(f.Ref)
	if len(f.Signatures) < floor {
		return fmt.Errorf("have %d signatures, need at least %d for the %s set",
			len(f.Signatures), floor, setNameFor(f.Ref))
	}
	// More is not safer: the extension drops an over-signed envelope before it
	// looks at the threshold, and says only that it was rejected. Catching it
	// here is the difference between a fixable message and a silent bounce.
	if len(f.Signatures) > envelope.MaxSignatures {
		return fmt.Errorf("have %d signatures and the extension accepts at most %d; drop the extras",
			len(f.Signatures), envelope.MaxSignatures)
	}

	age := time.Since(time.Unix(int64(f.IssuedAt), 0))
	if age > envelope.Freshness {
		return fmt.Errorf("envelope is %s old and the window is %s; rebuild and re-sign",
			age.Truncate(time.Second), envelope.Freshness)
	}

	body, err := hexBytes(f.Body)
	if err != nil {
		return err
	}
	ciphertext, err := hexBytes(f.Ciphertext)
	if err != nil {
		return err
	}

	sigs := make([][]byte, 0, len(f.Signatures))
	for _, s := range f.Signatures {
		raw, err := hexBytes(s)
		if err != nil {
			return err
		}
		sigs = append(sigs, raw)
	}

	chainID, ok := new(big.Int).SetString(f.ChainID, 10)
	if !ok {
		return fmt.Errorf("bad chainId in envelope")
	}

	message, err := envelope.Encode(envelope.Envelope{
		ChainID:  chainID,
		TeeID:    common.HexToAddress(f.TeeID),
		Version:  f.Version,
		IssuedAt: f.IssuedAt,
		Ref:      f.Ref,
		Payload:  ciphertext,
	}, sigs)
	if err != nil {
		return err
	}
	_ = body

	if *actionURL != "" {
		return postAction(*actionURL, message)
	}
	if *proxyURL == "" {
		return fmt.Errorf("-url or -action-url is required")
	}
	id, err := postDirect(*proxyURL, apiKey(), message)
	if err != nil {
		return err
	}
	if *wait <= 0 {
		fmt.Printf("not waiting for the result; the extension applies it when the machine next polls the direct queue\n")
		return nil
	}
	return awaitResult(*proxyURL, id, *wait)
}

// resultPollInterval is the gap between polls of a submitted action's result.
var resultPollInterval = time.Second

// awaitResult waits for the machine's own verdict on a submitted action. The
// proxy files it under the action id once tee-node has run it: status 1 is
// applied, 0 is rejected with the extension's one-line reason in Log, and a 404
// means not yet. The wait is what turns "the proxy accepted it" into "the
// enclave holds it"; without it a state read straight after the submit can
// race the machine and report a credential missing that lands a moment later.
func awaitResult(proxyURL string, id common.Hash, wait time.Duration) error {
	url := strings.TrimRight(proxyURL, "/") + "/action/result/" + id.Hex() +
		"?submissionTag=" + string(teetypes.Submit)
	deadline := time.Now().Add(wait)
	for {
		res, terminal, err := fetchResult(url)
		if err == nil && terminal {
			if res.Status == 1 {
				fmt.Printf("applied by the machine (action %s)\n", id.Hex())
				return nil
			}
			return fmt.Errorf("rejected by the machine: %s", strings.TrimSpace(res.Log))
		}
		if time.Now().After(deadline) {
			last := "no result filed"
			if err != nil {
				last = err.Error()
			}
			return fmt.Errorf("no verdict on action %s within %s (%s); the machine may still apply it — "+
				"check its state before resubmitting", id.Hex(), wait, last)
		}
		time.Sleep(resultPollInterval)
	}
}

// httpTimeout bounds one request to a proxy or node. Every URL here is an
// operator argument and may name a host that accepts the connection and never
// answers, which would otherwise hold the command indefinitely.
const httpTimeout = 10 * time.Second

// maxBody caps a response before it is decoded; real payloads are a few kilobytes.
const maxBody = 1 << 20

var httpClient = &http.Client{Timeout: httpTimeout}

// fetchResult reads one action result off the proxy and reports whether it is
// terminal. A 404 is the normal state until tee-node has answered.
func fetchResult(url string) (teetypes.ActionResult, bool, error) {
	resp, err := httpClient.Get(url)
	if err != nil {
		return teetypes.ActionResult{}, false, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode != http.StatusOK {
		return teetypes.ActionResult{}, false, fmt.Errorf("proxy returned %d", resp.StatusCode)
	}
	var body teetypes.ActionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&body); err != nil {
		return teetypes.ActionResult{}, false, fmt.Errorf("decoding the result: %w", err)
	}
	st := body.Result.Status
	return body.Result, st == 0 || st == 1, nil
}

// postDirect submits through the proxy, which is the production path, and
// returns the id of the action the proxy enqueued.
func postDirect(proxyURL, apiKey string, message []byte) (common.Hash, error) {
	payload := map[string]string{
		"opType":    config.OPTypeTeeOracle.Hex(),
		"opCommand": config.OPCommandSetSecret.Hex(),
		"message":   "0x" + hex.EncodeToString(message),
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return common.Hash{}, err
	}

	req, err := http.NewRequest(http.MethodPost, strings.TrimRight(proxyURL, "/")+"/direct", bytes.NewReader(raw))
	if err != nil {
		return common.Hash{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := httpClient.Do(req)
	if err != nil {
		return common.Hash{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return common.Hash{}, fmt.Errorf("proxy returned %d: %s", resp.StatusCode, strings.TrimSpace(string(out)))
	}
	// The proxy echoes the action it enqueued; the id is what its result is
	// filed under.
	var action teetypes.Action
	if err := json.Unmarshal(out, &action); err != nil {
		return common.Hash{}, fmt.Errorf("decoding the proxy response: %w", err)
	}
	if action.Data.ID == (common.Hash{}) {
		return common.Hash{}, fmt.Errorf("the proxy returned no action id")
	}
	fmt.Printf("submitted to %s/direct as action %s\n", strings.TrimRight(proxyURL, "/"), action.Data.ID.Hex())
	return action.Data.ID, nil
}

// postAction delivers straight to an extension, bypassing proxy and chain. Local
// testing only; the production path is postDirect. The inner envelope is a
// DirectInstruction — what the proxy really sends — never a DataFixed.
func postAction(actionURL string, message []byte) error {
	di := map[string]any{
		"opType":    config.OPTypeTeeOracle.Hex(),
		"opCommand": config.OPCommandSetSecret.Hex(),
		"message":   "0x" + hex.EncodeToString(message),
	}
	diRaw, err := json.Marshal(di)
	if err != nil {
		return err
	}

	action := map[string]any{
		"data": map[string]any{
			"id": "0x" + strings.Repeat("bb", 32),
			// MUST be "direct": SET_SECRET is refused over the instruction
			// channel, since on-chain a credential ciphertext would be permanent
			// and public.
			"type":          "direct",
			"submissionTag": "submit",
			"message":       "0x" + hex.EncodeToString(diRaw),
		},
		"additionalVariableMessages": []string{},
		"timestamps":                 []int{},
		"additionalActionData":       "0x",
		"signatures":                 []string{},
	}
	raw, err := json.Marshal(action)
	if err != nil {
		return err
	}

	resp, err := httpClient.Post(actionURL, "application/json", bytes.NewReader(raw))
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()
	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))

	var result struct {
		Status uint8  `json:"status"`
		Log    string `json:"log"`
	}
	_ = json.Unmarshal(out, &result)

	if result.Status == 1 {
		fmt.Println("accepted")
		return nil
	}
	return fmt.Errorf("rejected: %s", result.Log)
}

// --- helpers ---

func fetchKey(base string) (*ecdsa.PublicKey, common.Address, error) {
	resp, err := httpClient.Get(strings.TrimRight(base, "/") + "/info")
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("fetching /info: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	// The response carries no teeId field: the machine identity is the address
	// of the attested publicKey and must be derived from it — the same
	// derivation fccutils.TeeProxyId and tee-proxy's ParseTeeID use.
	//
	// teetypes.PublicKey rather than a local pair of strings, so the coordinates
	// are decoded by the same declaration tee-node serialises them from, and the
	// on-curve check is the one it applies.
	var info struct {
		TEEInfo struct {
			PublicKey teetypes.PublicKey `json:"publicKey"`
		} `json:"teeInfo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, maxBody)).Decode(&info); err != nil {
		return nil, common.Address{}, fmt.Errorf("parsing /info: %w", err)
	}

	pub, err := teetypes.ParsePubKey(info.TEEInfo.PublicKey)
	if err != nil {
		return nil, common.Address{}, fmt.Errorf("/info public key: %w", err)
	}
	return pub, crypto.PubkeyToAddress(*pub), nil
}

func recoverSigner(body, sig []byte) (common.Address, bool) {
	if len(sig) != 65 {
		return common.Address{}, false
	}
	s := make([]byte, 65)
	copy(s, sig)
	if s[64] >= 27 {
		s[64] -= 27
	}
	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), s)
	if err != nil {
		return common.Address{}, false
	}
	return crypto.PubkeyToAddress(*pub), true
}

// setNameFor names the signer set governing a ref, for messages only. The
// mapping is config.AdminPolicy, the same one the enclave is built with.
func setNameFor(ref string) string {
	return config.AdminPolicy().RoleFor(ref)
}

// b32 renders Solidity's bytes32(s). The op identifiers passed to it come from
// internal/config rather than string literals: the extension routes on exactly
// those constants, and a literal here would keep compiling after a retag while
// every envelope bounced as an unsupported op type.
func hexBytes(s string) ([]byte, error) {
	return hex.DecodeString(strings.TrimPrefix(s, "0x"))
}

func readJSON(path string, v any) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	return json.Unmarshal(raw, v)
}

func writeJSON(path string, v any) error {
	raw, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o600)
}
