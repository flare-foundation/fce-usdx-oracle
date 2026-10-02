// Command verify-tee answers one question before a credential is handed to a
// machine: is this proxy fronting an enclave of OUR extension, running code we
// accept?
//
//	go run ./cmd/verify-tee -p https://proxy.example -codeHash 0x… [-extensionId 1] [-printTeeId]
//
// The question matters because the next step encrypts a credential to a public
// key this same proxy served. A proxy that lied about that key would receive the
// plaintext itself, so reading a code hash out of the proxy's own /info and
// comparing it to an expectation proves nothing on its own — the same untrusted
// source supplied both halves.
//
// What breaks the circularity is that the teeId is DERIVED from the public key
// the proxy serves, and the chain holds the registry's own record of that
// teeId: its status, its extension, and the code hash and platform it was
// registered with. Registration verified the machine's attestation, and
// toProduction re-checked the pair against the extension's allowed set, so that
// record is authoritative and the attestation token is not verified locally.
// So:
//
//	/info  → public key → teeId              (derived, not asserted)
//	chain  → teeId is registered and at PRODUCTION
//	chain  → teeId belongs to extension E, and E is the one you expect
//	chain  → the code hash and platform the registry recorded for teeId
//	chain  → E accepts that code hash on that platform
//	you    → that code hash is the one you were told to expect
//	/info  → the token's claimed code hash and platform agree with the record
//
// The last line is a cross-check only: the token's claims are parsed without
// verifying its signature, and a disagreement fails the run because it means
// the proxy is serving an attestation for a different build than the machine it
// names. Substitute the key and the derived teeId stops matching a registered
// machine of the expected extension; the record it does match is not under the
// proxy's control.
//
// The expected extension comes from -extensionId, which defaults to EXTENSION_ID
// (.env). With neither set the command refuses: any PRODUCTION machine of any
// extension that allows the code hash would otherwise pass.
//
// Exits non-zero when any check fails, or when the machine or the chain cannot
// be read, so a shell script can gate on it.
//
// # Passing the answer on
//
// Gating is necessary but not sufficient. This command and the encryption that
// follows it are separate processes making separate requests, so a proxy can
// serve an honest key here and a substituted one to `send-secret build` a moment
// later — and build encrypts to whatever it is handed. -printTeeId writes the
// verified teeId to stdout so the caller can pass it to `send-secret build -tee`,
// which then refuses any key deriving to a different address. Without that hand-
// off, verification proves something true about a request nobody encrypted to.
package main

import (
	"context"
	"encoding/hex"
	"flag"
	"fmt"
	"io"
	"math/big"
	"os"
	"strings"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	extensionIDF := flag.String("extensionId", configs.ExtensionID,
		"the extension the machine must belong to, decimal or 0x-hex; defaults to EXTENSION_ID "+
			"from .env, and with neither set the command refuses")
	expectedCodeHash := flag.String("codeHash", "",
		"the code hash you expect this machine to be running (0x…, 32 bytes); "+
			"obtain it out-of-band, never from the machine you are checking")
	quiet := flag.Bool("quiet", false, "print only failures")
	printTeeID := flag.Bool("printTeeId", false,
		"on success, write ONLY the verified teeId to stdout (all other output goes to "+
			"stderr) so a script can pass it to send-secret build -tee")
	flag.Parse()

	// Both are inputs, so both are judged before anything is asked of the proxy
	// or the chain.
	wantExtension, err := parseExtensionID(*extensionIDF)
	if err != nil {
		refuse(err)
	}
	var wantCodeHash *common.Hash
	if *expectedCodeHash != "" {
		h, err := parseHash32(*expectedCodeHash)
		if err != nil {
			refuse(errors.Errorf("-codeHash: %s", err))
		}
		wantCodeHash = &h
	}

	// With -printTeeId stdout carries one address and nothing else, so the human
	// report moves to stderr rather than being suppressed — the scripted path must
	// not be the one nobody watches.
	out := io.Writer(os.Stdout)
	if *printTeeID {
		out = os.Stderr
	}
	info := func(format string, args ...any) {
		if !*quiet {
			_, _ = fmt.Fprintf(out, format, args...)
		}
	}

	failed := false
	check := func(ok bool, label, detail string) {
		if ok {
			info("  ok    %s\n        %s\n", label, detail)
			return
		}
		fmt.Fprintf(os.Stderr, "  FAIL  %s\n        %s\n", label, detail)
		failed = true
	}
	fatal := func(format string, args ...any) {
		fmt.Fprintf(os.Stderr, format+"\n", args...)
		fmt.Fprintln(os.Stderr, "\nVERIFICATION FAILED — do not send a credential to this machine.")
		os.Exit(1)
	}

	info("Verifying the machine behind %s for extension %s\n\n", *pf, wantExtension)

	teeInfo, err := fccutils.TeeInfo(*pf)
	if err != nil {
		fatal("cannot read %s/info: %v", *pf, err)
	}

	if os.Getenv("SIMULATED_TEE") == "true" {
		fmt.Fprintln(os.Stderr,
			"  WARN  SIMULATED_TEE=true: the code hash is a fixed test constant and\n"+
				"        NOTHING below is evidence about a real enclave. Never upload a\n"+
				"        production credential from this shell.")
	}

	// 1. The teeId is DERIVED from the public key the proxy serves, so it cannot
	//    be asserted independently of the key a credential would be encrypted to.
	teeID, _, err := fccutils.TeeProxyId(teeInfo)
	if err != nil {
		fatal("cannot derive the teeId from the served public key: %v", err)
	}
	info("  ---   teeId derived from the served public key: %s\n", teeID.Hex())

	s, err := support.ReadOnlySupport(*af, *cf)
	if err != nil {
		fatal("cannot reach the chain: %v", err)
	}
	opts := &bind.CallOpts{Context: context.Background()}

	// 2. That teeId is a machine the registry knows, cleared to serve. The
	//    registry reverts for an id it has never registered.
	record, err := s.TeeMachineRegistry.GetTeeMachineWithAttestationData(opts, teeID)
	if err != nil {
		fatal("the registry has no record of teeId %s: %s", teeID.Hex(), strings.TrimSpace(err.Error()))
	}
	if record.TeeId != teeID {
		fatal("the registry answered for %s when asked for %s", record.TeeId.Hex(), teeID.Hex())
	}
	status, err := s.TeeMachineRegistry.GetTeeMachineStatus(opts, teeID)
	check(err == nil && status == fccutils.TeeStatusProduction,
		"the derived teeId is registered and at PRODUCTION",
		fmt.Sprintf("status %s%s", fccutils.TeeStatusName(status), suffix(err)))

	// 3. The machine belongs to the extension we mean. Without this, a
	//    PRODUCTION machine of ANY extension that allows the code hash passes.
	extID, err := s.TeeMachineRegistry.GetExtensionId(opts, teeID)
	if err != nil {
		fatal("cannot read the machine's extension id: %s", strings.TrimSpace(err.Error()))
	}
	check(extID.Cmp(wantExtension) == 0,
		"the machine belongs to the expected extension",
		fmt.Sprintf("registered under extension %s, expected %s", extID, wantExtension))

	// 4. The build, as the registry recorded it when it verified the machine's
	//    attestation. This, not the token, is what every check below judges.
	codeHash, platform := common.Hash(record.CodeHash), common.Hash(record.Platform)
	info("  ---   registry record: codeHash %s platform %s\n", codeHash.Hex(), platform.Hex())

	// 5. The code hash is the one the caller was told to expect. Supplied by the
	//    operator out-of-band; without it every remaining check only proves the
	//    machine runs *some* build the extension accepts, not *your* build.
	if wantCodeHash == nil {
		fmt.Fprintln(os.Stderr,
			"  WARN  no -codeHash given, so this does not check WHICH build is running.\n"+
				"        Pass the hash you were given out-of-band to make this meaningful.")
	} else {
		check(*wantCodeHash == codeHash, "the registered code hash is the expected one",
			fmt.Sprintf("expected %s, registered %s", wantCodeHash.Hex(), codeHash.Hex()))
	}

	// 6. The expected extension still accepts that code hash on that platform.
	//    Asked of the expected extension rather than the machine's, so a failed
	//    check 3 cannot be papered over by a permissive neighbour.
	supported, err := s.TeeExtensionRegistry.IsCodeHashPlatformSupported(opts, wantExtension, codeHash, platform)
	check(err == nil && supported,
		"the extension accepts the registered code hash on the registered platform",
		fmt.Sprintf("extension %s, codeHash %s, platform %s%s", wantExtension, codeHash.Hex(), platform.Hex(), suffix(err)))

	// 7. Cross-check: what the proxy's attestation token claims must agree with
	//    the record. The claims are parsed, not verified, so agreement proves
	//    nothing by itself; disagreement means the proxy serves an attestation
	//    for a different build than the machine it names.
	claimedCodeHash, claimedPlatform, err := fccutils.GetCodeHashAndPlatform(teeInfo)
	if err != nil {
		check(false, "the attestation token's claims parse and match the reported machine data", err.Error())
	} else {
		check(claimedCodeHash == codeHash && claimedPlatform == platform,
			"the attestation token's claims agree with the registry record",
			fmt.Sprintf("token claims codeHash %s platform %s", claimedCodeHash.Hex(), claimedPlatform.Hex()))
	}

	if failed {
		fmt.Fprintln(os.Stderr, "\nVERIFICATION FAILED — do not send a credential to this machine.")
		os.Exit(1)
	}
	info("\nVerified. teeId %s is a PRODUCTION machine of extension %s running an accepted build.\n",
		teeID.Hex(), wantExtension)

	// Last, and only on success: the one line stdout carries. Reached only after
	// every check above passed, so a caller capturing it in $( ) either gets a
	// verified id or a non-zero exit and an empty string.
	if *printTeeID {
		fmt.Println(teeID.Hex())
	}
}

// refuse reports an unusable input and exits before anything is asked of the
// proxy or the chain.
func refuse(err error) {
	fmt.Fprintf(os.Stderr, "verify-tee: %s\n", err)
	os.Exit(1)
}

// parseExtensionID reads the expected extension id. Empty is refused rather
// than defaulted: there is no extension id that is right everywhere.
func parseExtensionID(raw string) (*big.Int, error) {
	raw = strings.TrimSpace(raw)
	if raw == "" {
		return nil, errors.New("no expected extension id: pass -extensionId or set EXTENSION_ID in .env — " +
			"without one, a PRODUCTION machine of any extension that allows the code hash would pass")
	}
	id, ok := new(big.Int).SetString(raw, 0)
	if !ok || id.Sign() <= 0 {
		return nil, errors.Errorf("extension id %q is not a positive integer", raw)
	}
	return id, nil
}

// parseHash32 reads exactly 32 bytes of hex, with or without 0x.
// common.HexToHash silently truncates and pads anything else, which would turn
// a typo into a valid-looking hash for a build nobody is running.
func parseHash32(raw string) (common.Hash, error) {
	text := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "0x"), "0X")
	b, err := hex.DecodeString(text)
	if err != nil || len(b) != common.HashLength {
		return common.Hash{}, errors.Errorf("%q is not 32 bytes of hex", raw)
	}
	return common.BytesToHash(b), nil
}

func suffix(err error) string {
	if err != nil {
		return "; " + strings.TrimSpace(err.Error())
	}
	return ""
}
