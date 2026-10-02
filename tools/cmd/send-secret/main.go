// Command send-secret installs one credential in a machine: it builds a
// SET_SECRET envelope, collects the admins' signatures and submits it over the
// proxy's direct channel.
//
//	send-secret build   fetch the machine's key, encrypt, write an unsigned envelope
//	send-secret sign    one admin signs the envelope body
//	send-secret submit  combine the signatures, POST, wait for the machine's verdict
//	send-secret commit  print the urlHash a SET_ENDPOINTS publishes for the cash URL
//	send-secret signers list who has signed an envelope, and whether it meets its set's threshold
//
// Subcommands because signing involves different people. The unsigned envelope
// is safe to pass around: the credential inside is already ciphertext only the
// target enclave can open. The envelope format and the admin policy are the
// extension's own, pkg/wire/envelope and pkg/protocol, so the body an admin
// signs is produced by the encoder the enclave verifies with.
//
// Credentials are read from the environment and never from a flag: argv is
// readable by every local process through ps, and it lands in shell history.
//
//	SECRET_VALUE    the credential build encrypts, for every ref but hex_cash_url
//	HEX_CASH_URL    the restricted cash URL and the salt of its commitment, for
//	HEX_CASH_SALT   build -ref hex_cash_url and for commit
//	ADMIN_KEY       the signing admin's private key, for sign
//	DIRECT_API_KEY  the proxy's gate for the direct channel, for submit
package main

import (
	"context"
	"crypto/ecdsa"
	"crypto/rand"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/wire/configpayload"
	"fce-usdx-oracle/pkg/wire/eip191"
	"fce-usdx-oracle/pkg/wire/envelope"
	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/direct"
	"fce-usdx-oracle/tools/pkg/fccutils"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	"github.com/ethereum/go-ethereum/ethclient"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
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

// requestTimeout bounds one round trip to the chain node or a proxy.
const requestTimeout = 15 * time.Second

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
	case "signers":
		err = signers(os.Args[2:])
	default:
		usage()
	}
	if errors.Is(err, flag.ErrHelp) {
		os.Exit(2)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "send-secret:", err)
		os.Exit(1)
	}
}

func usage() {
	fmt.Fprint(os.Stderr, `send-secret — install one credential in a machine

  send-secret build  -ref <name> [-p <proxy>] [-c <chain node>] [-version <n>] [-tee <id>] [-out env.json]
  send-secret sign   -in env.json [-out env.json]
  send-secret submit -in env.json [-p <proxy>] [-wait <d>] [-extension <url>]
  send-secret commit                      print the urlHash a SET_ENDPOINTS publishes for the cash URL
  send-secret signers -in env.json [-admins admins.json]   who has signed, and is it enough

Credentials come from the environment, never from a flag: SECRET_VALUE for build,
HEX_CASH_URL and HEX_CASH_SALT for the cash URL, ADMIN_KEY for sign and
DIRECT_API_KEY for submit. -p and -c default to the .env the other tools read.

Always pass -tee for a real credential. build encrypts to whatever public key the
proxy's /info serves, and -tee is what makes it refuse a substituted one; take the
value from the attestation, with "verify-tee -printTeeId". The upload scripts do
this for you.

The `+protocol.HexCashURLRef+` credential is not a string but abi.encode(salt, url), the
preimage of the urlHash the hex_cash group publishes. build takes it from HEX_CASH_URL
and HEX_CASH_SALT rather than from SECRET_VALUE, and commit prints the matching urlHash
from the same two variables.

The freshness window is `+envelope.Freshness.String()+`: sign and submit within it, or
the envelope is rejected as stale.

Two role-tagged signer sets are published per feed, and the ref decides which
one applies: `+strings.Join(protocol.BackingRefs(), ", ")+`
are authorised by the backing set (at least `+fmt.Sprint(protocol.MinBackingThreshold)+` signature), every other ref by
the providers set (at least `+fmt.Sprint(protocol.MinProviderThreshold)+` DISTINCT signatures). The exact thresholds
are whatever SET_ADMINS published, and only the target machine can tell you
whether an envelope satisfies them.
`)
	os.Exit(2)
}

// privateURL validates the cash URL and its salt as the store will see them.
// The salt is what keeps the on-chain commitment from being brute-forced back
// to the URL, so a zero or short one is refused here rather than published.
func privateURL(rawURL, rawSalt string) (configpayload.PrivateURL, error) {
	if rawURL == "" || rawSalt == "" {
		return configpayload.PrivateURL{}, errors.Errorf(
			"HEX_CASH_URL and HEX_CASH_SALT are both required for %s", protocol.HexCashURLRef)
	}
	if !strings.HasPrefix(rawURL, "https://") {
		return configpayload.PrivateURL{}, errors.New("HEX_CASH_URL must use https")
	}
	saltBytes, err := hex.DecodeString(strings.TrimPrefix(strings.TrimPrefix(rawSalt, "0x"), "0X"))
	if err != nil || len(saltBytes) != 32 {
		return configpayload.PrivateURL{}, errors.New("HEX_CASH_SALT must be 32 bytes of hex (openssl rand -hex 32)")
	}
	salt := common.BytesToHash(saltBytes)
	if salt == (common.Hash{}) {
		return configpayload.PrivateURL{}, errors.New("HEX_CASH_SALT is zero")
	}
	return configpayload.PrivateURL{Salt: salt, Url: rawURL}, nil
}

// --- commit ---

// commit prints the urlHash the hex_cash group of a SET_ENDPOINTS must carry for
// HEX_CASH_URL under HEX_CASH_SALT: keccak256 of the very blob build provisions
// for hex_cash_url. Stdout carries the hash and nothing else, so a script can
// substitute it into an endpoints configuration.
func commit(args []string) error {
	fs := flag.NewFlagSet("send-secret commit", flag.ContinueOnError)
	if err := fs.Parse(args); err != nil {
		return err
	}
	u, err := privateURL(os.Getenv("HEX_CASH_URL"), os.Getenv("HEX_CASH_SALT"))
	if err != nil {
		return err
	}
	h, err := configpayload.CommitPrivateURL(u)
	if err != nil {
		return errors.Errorf("committing the cash URL: %s", err)
	}
	fmt.Println(h.Hex())
	return nil
}

// --- build ---

func build(args []string) error {
	fs := flag.NewFlagSet("send-secret build", flag.ContinueOnError)
	pf := fs.String("p", configs.ExtensionProxyURL, "the machine's proxy url, whose /info serves its public key")
	cf := fs.String("c", configs.ChainNodeURL, "chain node url; the envelope is bound to the chain it reports")
	ref := fs.String("ref", "", "secret name, e.g. "+protocol.HexAPIKeyRef)
	version := fs.Uint64("version", 1, "must strictly increase per ref")
	out := fs.String("out", "envelope.json", "output file")
	teeIDFlag := fs.String("tee", "",
		"the teeId the ATTESTATION derived (verify-tee -printTeeId); "+
			"build refuses to encrypt if /info serves a key deriving to anything else")
	if err := fs.Parse(args); err != nil {
		return err
	}
	if *ref == "" {
		return errors.New("-ref is required")
	}

	// Every credential but one is the string the operator holds. The cash URL is
	// provisioned as its commitment preimage, abi.encode(salt, url), which no
	// environment variable can carry (it contains zero bytes), so it is built
	// here from the URL and the salt, and a value given for it is refused rather
	// than encrypted as a string the enclave would reject.
	var credential []byte
	if *ref == protocol.HexCashURLRef {
		if os.Getenv("SECRET_VALUE") != "" {
			return errors.Errorf("%s is derived from HEX_CASH_URL and HEX_CASH_SALT, not given in SECRET_VALUE",
				protocol.HexCashURLRef)
		}
		u, err := privateURL(os.Getenv("HEX_CASH_URL"), os.Getenv("HEX_CASH_SALT"))
		if err != nil {
			return err
		}
		if credential, err = configpayload.EncodePrivateURL(u); err != nil {
			return errors.Errorf("encoding the cash URL: %s", err)
		}
	} else {
		credential = []byte(os.Getenv("SECRET_VALUE"))
	}
	if len(credential) == 0 {
		return errors.New("SECRET_VALUE is required: the credential to install")
	}

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout)
	defer cancel()
	chainID, err := chainIDOf(ctx, *cf)
	if err != nil {
		return err
	}

	pub, teeID, err := fetchKey(*pf)
	if err != nil {
		return err
	}

	// -tee asserts which machine answered; it never overrides what /info served, which
	// would send the plaintext to whoever answered the port. verify-tee runs a separate
	// request, so it cannot speak for the key this call fetched — hence the flag.
	if *teeIDFlag != "" {
		want := common.HexToAddress(*teeIDFlag)
		if want == (common.Address{}) {
			return errors.New("-tee is not a usable address")
		}
		if want != teeID {
			return errors.Errorf(
				"%s served a public key deriving to %s, but -tee names %s — "+
					"the key was substituted between verification and now; "+
					"do NOT send a credential to this endpoint",
				strings.TrimRight(*pf, "/"), teeID.Hex(), want.Hex())
		}
	}
	if teeID == (common.Address{}) {
		return errors.New("could not determine teeId; pass -tee")
	}

	// Encrypt to the enclave. From here the credential exists in plaintext only
	// inside the target TEE.
	ciphertext, err := ecies.Encrypt(rand.Reader, ecies.ImportECDSAPublic(pub), credential, nil, nil)
	if err != nil {
		return errors.Errorf("encrypting to the TEE key: %s", err)
	}

	env := envelope.Envelope{
		ChainID:  chainID,
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
	fmt.Printf("  chainId  %s\n", env.ChainID)
	fmt.Printf("  ref      %s\n", env.Ref)
	fmt.Printf("  version  %d\n", env.Version)
	fmt.Printf("  expires  %s (%s from now)\n",
		time.Unix(int64(env.IssuedAt), 0).Add(envelope.Freshness).UTC().Format(time.RFC3339),
		envelope.Freshness)
	fmt.Printf("\nRef %q is authorised by the %s set: at least %d distinct signature(s) —\n",
		env.Ref, protocol.AdminPolicy().RoleFor(env.Ref), protocol.AdminPolicy().MinThresholdFor(env.Ref))
	fmt.Printf("the published threshold for this machine may be higher. The credential is\n")
	fmt.Printf("already encrypted, so this file is safe to send to the signers.\n")
	return nil
}

// chainIDOf asks the chain node which chain it serves. The envelope is bound
// to it, and the enclave refuses one for another chain with the same one-line
// rejection it gives everything else, so the id is read rather than typed.
func chainIDOf(ctx context.Context, nodeURL string) (*big.Int, error) {
	client, err := ethclient.DialContext(ctx, nodeURL)
	if err != nil {
		return nil, errors.Errorf("connecting to the chain node %s: %s", nodeURL, err)
	}
	defer client.Close()
	id, err := client.ChainID(ctx)
	if err != nil {
		return nil, errors.Errorf("reading the chain id from %s: %s", nodeURL, err)
	}
	return id, nil
}

// --- sign ---

func sign(args []string) error {
	fs := flag.NewFlagSet("send-secret sign", flag.ContinueOnError)
	in := fs.String("in", "envelope.json", "envelope file")
	out := fs.String("out", "", "output file (defaults to -in)")
	if err := fs.Parse(args); err != nil {
		return err
	}
	adminKey := os.Getenv("ADMIN_KEY")
	if adminKey == "" {
		return errors.New("ADMIN_KEY is required: the signing admin's private key, hex")
	}

	var f envelopeFile
	if err := readJSON(*in, &f); err != nil {
		return err
	}

	key, err := crypto.HexToECDSA(strings.TrimPrefix(adminKey, "0x"))
	if err != nil {
		return errors.New("ADMIN_KEY is not a private key")
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
		if recovered, ok := eip191.Recover(body, raw); ok && recovered == signer {
			return errors.Errorf("%s has already signed; M-of-N needs DISTINCT signers", signer.Hex())
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

// --- signers ---

// signers recovers every signature on an envelope to the address that made it,
// offline. With -admins it names each one against the set that governs the
// envelope's ref and says whether the distinct admins reach that set's
// threshold, which is the question to settle before a submit spends the window:
// the enclave answers every shortfall with the same one-line rejection. The
// same check serves a rehearsal, where each admin signs a practice envelope and
// the recovered address shows whether the key they will sign with is the one
// the admins file names.
//
// It reports on the signatures the file carries; what the body commits to is
// read from the file as it stands.
func signers(args []string) error {
	fs := flag.NewFlagSet("send-secret signers", flag.ContinueOnError)
	in := fs.String("in", "envelope.json", "envelope file")
	adminsPath := fs.String("admins", "",
		"the published admins file (deploy/config/admins/<chain>.json); checks each signer against it")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var f envelopeFile
	if err := readJSON(*in, &f); err != nil {
		return err
	}
	body, err := hexBytes(f.Body)
	if err != nil {
		return errors.Errorf("envelope body is not hex: %s", err)
	}
	role := protocol.AdminPolicy().RoleFor(f.Ref)

	var set map[common.Address]bool
	threshold := 0
	if *adminsPath != "" {
		admins, t, err := readAdminSet(*adminsPath, role)
		if err != nil {
			return err
		}
		set, threshold = admins, t
	}

	fmt.Printf("ref %s on machine %s — governed by the %s set\n", f.Ref, f.TeeID, role)
	seen := map[common.Address]bool{}
	for i, s := range f.Signatures {
		raw, err := hexBytes(s)
		signer, ok := common.Address{}, false
		if err == nil {
			signer, ok = eip191.Recover(body, raw)
		}
		switch {
		case !ok:
			fmt.Printf("  %d  does not recover — not a signature over this envelope\n", i+1)
		case set == nil:
			fmt.Printf("  %d  %s\n", i+1, signer.Hex())
		case !set[signer]:
			fmt.Printf("  %d  %s  NOT in the %s set\n", i+1, signer.Hex(), role)
		case seen[signer]:
			fmt.Printf("  %d  %s  admin, but already counted\n", i+1, signer.Hex())
		default:
			fmt.Printf("  %d  %s  admin\n", i+1, signer.Hex())
			seen[signer] = true
		}
	}

	if set == nil {
		fmt.Printf("%d signature(s); pass -admins to check them against the published set\n", len(f.Signatures))
		return nil
	}
	fmt.Printf("%d distinct %s admin(s) of the %d needed\n", len(seen), role, threshold)
	if len(seen) < threshold {
		return errors.Errorf("not enough: %d more distinct %s admin signature(s) before this can be submitted",
			threshold-len(seen), role)
	}
	return nil
}

// readAdminSet reads one role's signer set out of an admins file in the shape
// send-admins publishes: {"backing": {threshold, admins}, "providers": {...}}.
func readAdminSet(path, role string) (map[common.Address]bool, int, error) {
	var file map[string]json.RawMessage
	if err := readJSON(path, &file); err != nil {
		return nil, 0, errors.Errorf("reading %s: %s", path, err)
	}
	raw, ok := file[role]
	if !ok {
		return nil, 0, errors.Errorf("%s has no %s set", path, role)
	}
	var entry struct {
		Threshold int      `json:"threshold"`
		Admins    []string `json:"admins"`
	}
	if err := json.Unmarshal(raw, &entry); err != nil {
		return nil, 0, errors.Errorf("%s: the %s set is malformed: %s", path, role, err)
	}
	set := make(map[common.Address]bool, len(entry.Admins))
	for _, a := range entry.Admins {
		if !common.IsHexAddress(a) {
			return nil, 0, errors.Errorf("%s: %q is not an address", path, a)
		}
		set[common.HexToAddress(a)] = true
	}
	return set, entry.Threshold, nil
}

// --- submit ---

func submit(args []string) error {
	fs := flag.NewFlagSet("send-secret submit", flag.ContinueOnError)
	in := fs.String("in", "envelope.json", "envelope file")
	pf := fs.String("p", configs.ExtensionProxyURL, "the machine's proxy url; POSTs to /direct")
	extF := fs.String("extension", "",
		"post to this extension's own /action instead of a proxy (the development harness)")
	wait := fs.Duration("wait", 60*time.Second,
		"how long to wait for the machine's own result; 0 returns as soon as the proxy accepts")
	if err := fs.Parse(args); err != nil {
		return err
	}

	var f envelopeFile
	if err := readJSON(*in, &f); err != nil {
		return err
	}
	// The floor for the set this ref falls under, not the machine's actual threshold:
	// fewer than this can never be accepted anywhere, so catch it before the trip.
	floor := protocol.AdminPolicy().MinThresholdFor(f.Ref)
	if len(f.Signatures) < floor {
		return errors.Errorf("have %d signatures, need at least %d for the %s set",
			len(f.Signatures), floor, protocol.AdminPolicy().RoleFor(f.Ref))
	}
	// More is not safer: the extension drops an over-signed envelope before it
	// looks at the threshold, and says only that it was rejected. Catching it
	// here is the difference between a fixable message and a silent bounce.
	if len(f.Signatures) > envelope.MaxSignatures {
		return errors.Errorf("have %d signatures and the extension accepts at most %d; drop the extras",
			len(f.Signatures), envelope.MaxSignatures)
	}

	age := time.Since(time.Unix(int64(f.IssuedAt), 0))
	if age > envelope.Freshness {
		return errors.Errorf("envelope is %s old and the window is %s; rebuild and re-sign",
			age.Truncate(time.Second), envelope.Freshness)
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
		return errors.New("bad chainId in envelope")
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

	ctx, cancel := context.WithTimeout(context.Background(), requestTimeout+*wait)
	defer cancel()

	if *extF != "" {
		_, res, err := direct.PostToExtension(ctx, *extF,
			protocol.OPTypeTeeOracle, protocol.OPCommandSetSecret, message)
		if err != nil {
			return err
		}
		if res.Status != direct.StatusSucceeded {
			return errors.Errorf("rejected: %s", strings.TrimSpace(res.Log))
		}
		fmt.Println("accepted")
		return nil
	}

	proxy := strings.TrimRight(*pf, "/")
	id, err := direct.Post(ctx, proxy, os.Getenv("DIRECT_API_KEY"),
		protocol.OPTypeTeeOracle, protocol.OPCommandSetSecret, message)
	if err != nil {
		return err
	}
	fmt.Printf("submitted to %s/direct as action %s\n", proxy, id.Hex())
	if *wait <= 0 {
		fmt.Println("not waiting for the result; the extension applies it when the machine next polls the direct queue")
		return nil
	}

	// The wait is what turns "the proxy accepted it" into "the enclave holds
	// it"; without it a state read straight after the submit can race the
	// machine and report a credential missing that lands a moment later.
	res, err := direct.Await(ctx, proxy, id, teetypes.Submit, *wait)
	if err != nil {
		return errors.Errorf("no verdict on action %s within %s (%s); the machine may still apply it — "+
			"check its state before resubmitting", id.Hex(), *wait, err)
	}
	if res.Status != direct.StatusSucceeded {
		return errors.Errorf("rejected by the machine: %s", strings.TrimSpace(res.Log))
	}
	fmt.Printf("applied by the machine (action %s)\n", id.Hex())
	return nil
}

// --- helpers ---

// fetchKey reads the machine's public key off its proxy's /info and derives the
// teeId from it. The response carries no teeId field: the machine identity is
// the address of the attested publicKey, the same derivation fccutils.TeeProxyId
// and tee-proxy's ParseTeeID use. The proxy's own signature over the info is not
// checked here; -tee is what pins the machine.
func fetchKey(base string) (*ecdsa.PublicKey, common.Address, error) {
	info, err := fccutils.TeeInfo(strings.TrimRight(base, "/"))
	if err != nil {
		return nil, common.Address{}, errors.Errorf("fetching /info: %s", err)
	}
	// teetypes.ParsePubKey rather than a local decoding, so the coordinates are
	// read by the same declaration tee-node serialises them from, and the
	// on-curve check is the one it applies.
	pub, err := teetypes.ParsePubKey(info.TeeInfo.PublicKey)
	if err != nil {
		return nil, common.Address{}, errors.Errorf("/info public key: %s", err)
	}
	return pub, crypto.PubkeyToAddress(*pub), nil
}

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
