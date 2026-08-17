// Command verify-tee answers one question before a credential is handed to a
// machine: is this proxy fronting an enclave running code we accept?
//
// The question matters because the next step encrypts a credential to a public
// key this same proxy served. A proxy that lied about that key would receive the
// plaintext itself, so reading a code hash out of the proxy's own /info and
// comparing it to an expectation proves nothing on its own — the same untrusted
// source supplied both halves.
//
// What breaks the circularity is that the teeId is DERIVED from the attested
// public key, and the chain records which teeIds are registered, at what status,
// under which extension, and which code hashes that extension accepts. So:
//
//	/info  → attestation → code hash        (parsed from the quote, not the JSON)
//	/info  → public key  → teeId            (derived, not asserted)
//	chain  → teeId is registered, PRODUCTION, in extension E
//	chain  → extension E accepts that code hash on that platform
//	you    → the code hash is the one you were told to expect
//
// Substitute the key and the derived teeId stops matching a registered machine.
// Substitute the attestation and the code hash stops matching what the chain
// accepts. Both must hold, and neither is under the proxy's control.
//
// Exits non-zero on the first failed check, so a shell script can gate on it.
//
// # Passing the answer on
//
// Gating is necessary but not sufficient. This command and the encryption that
// follows it are separate processes making separate requests, so a proxy can
// serve an honest key here and a substituted one to `usdxprov build` a moment
// later — and build encrypts to whatever it is handed. -print-tee-id writes the
// ATTESTED teeId to stdout so the caller can pass it to `usdxprov build -tee`,
// which then refuses any key deriving to a different address. Without that hand-
// off, verification proves something true about a request nobody encrypted to.
package main

import (
	"context"
	"flag"
	"fmt"
	"io"
	"os"
	"strings"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
)

// productionStatus mirrors ITeeMachineRegistry.TeeStatus.PRODUCTION. A machine
// below this is registered but not cleared to serve.
const productionStatus = 2

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	expectedCodeHash := flag.String("code-hash", "",
		"the code hash you expect this machine to be running (0x…); "+
			"obtain it out-of-band, never from the machine you are checking")
	quiet := flag.Bool("quiet", false, "print only failures")
	printTeeID := flag.Bool("print-tee-id", false,
		"on success, write ONLY the attested teeId to stdout (all other output goes to "+
			"stderr) so a script can pass it to `usdxprov build -tee`")
	flag.Parse()

	// With -print-tee-id stdout carries one address and nothing else, so the human
	// report moves to stderr rather than being suppressed — the scripted path must
	// not be the one nobody watches.
	out := io.Writer(os.Stdout)
	if *printTeeID {
		out = os.Stderr
	}

	failed := false
	check := func(ok bool, label, detail string) {
		if ok {
			if !*quiet {
				fmt.Fprintf(out, "  ok    %s\n        %s\n", label, detail)
			}
			return
		}
		fmt.Fprintf(os.Stderr, "  FAIL  %s\n        %s\n", label, detail)
		failed = true
	}

	if !*quiet {
		fmt.Fprintf(out, "Verifying the machine behind %s\n\n", *pf)
	}

	// 1. The attestation itself. GetCodeHashAndPlatform parses the quote and
	//    cross-checks it against the self-reported MachineData, so a proxy
	//    claiming a code hash its attestation does not support fails here.
	teeInfo, err := fccutils.TeeInfo(*pf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot read %s/info: %v\n", *pf, err)
		os.Exit(1)
	}

	if os.Getenv("SIMULATED_TEE") == "true" {
		fmt.Fprintln(os.Stderr,
			"  WARN  SIMULATED_TEE=true: the code hash is a fixed test constant and\n"+
				"        NOTHING below is evidence about a real enclave. Never upload a\n"+
				"        production credential from this shell.")
	}

	codeHash, platform, err := fccutils.GetCodeHashAndPlatform(teeInfo)
	check(err == nil, "attestation parses and matches the reported machine data",
		fmt.Sprintf("%v", errOr(err, "code hash "+codeHash.Hex())))
	if err != nil {
		os.Exit(1)
	}

	// 2. The code hash is the one the caller was told to expect. Supplied by the
	//    operator out-of-band; without it every remaining check only proves the
	//    machine is *some* registered machine, not that it runs *your* build.
	if *expectedCodeHash == "" {
		fmt.Fprintln(os.Stderr,
			"  WARN  no -code-hash given, so this does not check WHICH build is running.\n"+
				"        Pass the hash you were given out-of-band to make this meaningful.")
	} else {
		want := common.HexToHash(*expectedCodeHash)
		check(want == codeHash, "code hash is the expected one",
			fmt.Sprintf("expected %s, attested %s", want.Hex(), codeHash.Hex()))
	}

	// 3. The teeId is DERIVED from the attested public key, so it cannot be
	//    asserted independently of the key a credential would be encrypted to.
	teeID, _, err := fccutils.TeeProxyId(teeInfo)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot derive the teeId from the attested key: %v\n", err)
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintf(out, "  ---   teeId derived from the attested public key: %s\n", teeID.Hex())
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fmt.Fprintf(os.Stderr, "cannot reach the chain: %v\n", err)
		os.Exit(1)
	}
	opts := &bind.CallOpts{Context: context.Background()}

	// 4. That derived teeId is a machine the chain knows, cleared to serve.
	status, err := s.TeeMachineRegistry.GetTeeMachineStatus(opts, teeID)
	check(err == nil && status == productionStatus,
		"the derived teeId is registered and at PRODUCTION",
		fmt.Sprintf("status %d (want %d)%s", status, productionStatus, suffix(err)))

	extID, err := s.TeeMachineRegistry.GetExtensionId(opts, teeID)
	if err != nil {
		check(false, "the machine belongs to an extension", err.Error())
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintf(out, "  ---   extension id: %s\n", extID.String())
	}

	// 5. The extension accepts this code hash on this platform. This is the
	//    check that ties the attested build to on-chain governance rather than
	//    to anything the proxy said.
	supported, err := s.TeeExtensionRegistry.IsCodeHashPlatformSupported(opts, extID, codeHash, platform)
	check(err == nil && supported,
		"the extension accepts this code hash on this platform",
		fmt.Sprintf("codeHash %s platform %s%s", codeHash.Hex(), platform.Hex(), suffix(err)))

	if failed {
		fmt.Fprintln(os.Stderr, "\nVERIFICATION FAILED — do not send a credential to this machine.")
		os.Exit(1)
	}
	if !*quiet {
		fmt.Fprintf(out, "\nVerified. teeId %s is running an accepted build.\n", teeID.Hex())
	}

	// Last, and only on success: the one line stdout carries. Reached only after
	// every check above passed, so a caller capturing it in $( ) either gets an
	// attested id or a non-zero exit and an empty string.
	if *printTeeID {
		fmt.Println(teeID.Hex())
	}
}

func errOr(err error, ok string) string {
	if err != nil {
		return err.Error()
	}
	return ok
}

func suffix(err error) string {
	if err != nil {
		return "; " + strings.TrimSpace(err.Error())
	}
	return ""
}
