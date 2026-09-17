// Command wire-extension performs the extension-owner wiring of a reserved
// extension after flare-smart-contracts-v2 has deployed its contracts and
// Flare governance has run registerReserved(extensionId, owner).
//
// It makes three direct owner calls on the FlareTeeManager diamond, each
// idempotent:
//
//  1. setExtensionContracts(extensionId, verifier, sender) — register-tee's
//     availability check dispatches through this sender, so run this BEFORE
//     post-build.sh. The verifier is set to the zero address, which is what the
//     registry needs for an extension whose state is empty.
//  2. AddAllowedTeeMachineOwners(extensionId, [machine owner]) — the address the
//     machines are launched with, which is the extension owner only where one
//     key holds both roles.
//
// The instructions sender resolves from -sender, or from the
// TeeOracleInstructionsSender entry in the addresses file (put there by
// scripts/import-upstream-addresses.sh after the upstream deploy). The machine
// owner resolves from -machineOwner, or from MACHINE_OWNER_KEY, or from
// INITIAL_OWNER, and finally from the signing key where none of those is set.
//
//	go run ./cmd/wire-extension            # addresses and chain from .env; -a / -c override
package main

import (
	"flag"
	"math/big"
	"os"
	"strings"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	extensionIDF := flag.String("extensionId", configs.ExtensionID, "extension id to wire (default: EXTENSION_ID from .env)")
	senderF := flag.String("sender", "",
		"TeeOracleInstructionsSender address (default: the TeeOracleInstructionsSender "+
			"entry in the addresses file)")
	machineOwnerF := flag.String("machineOwner", "",
		"the address the machines are launched with, allowed to register them (default: "+
			"MACHINE_OWNER_KEY's address, else INITIAL_OWNER, else this key's)")
	flag.Parse()

	extensionID, ok := new(big.Int).SetString(*extensionIDF, 0)
	if !ok || extensionID.Sign() <= 0 {
		fccutils.FatalWithCause(errors.Errorf("%q is not a positive extension id", *extensionIDF))
	}

	senderAddr, err := configs.ResolveContract(*senderF, *af, "TeeOracleInstructionsSender")
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the instructions sender: %s — run "+
			"scripts/import-upstream-addresses.sh after the upstream deploy, or pass -sender", err))
	}
	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// THIS address must be the owner governance registered with registerReserved.
	privKey := testSupport.Prv
	owner := crypto.PubkeyToAddress(privKey.PublicKey)

	// The machines' owner is a different role and usually a different address,
	// so it is resolved rather than assumed: the flag, then the key that signs
	// the registrations, then the address the machines are launched with, and
	// only then this caller — which is right just where one key holds both.
	machineOwner := owner
	machineOwnerFrom := "EXTENSION_OWNER_KEY (both roles on one key)"
	if key, keyErr := fccutils.OverrideKey("MACHINE_OWNER_KEY"); keyErr != nil {
		fccutils.FatalWithCause(keyErr)
	} else if key != nil {
		machineOwner = crypto.PubkeyToAddress(key.PublicKey)
		machineOwnerFrom = "MACHINE_OWNER_KEY"
	} else if io := strings.TrimSpace(os.Getenv("INITIAL_OWNER")); io != "" {
		if !common.IsHexAddress(io) {
			fccutils.FatalWithCause(errors.Errorf("INITIAL_OWNER %q is not an address", io))
		}
		machineOwner = common.HexToAddress(io)
		machineOwnerFrom = "INITIAL_OWNER"
	}
	if *machineOwnerF != "" {
		if !common.IsHexAddress(*machineOwnerF) {
			fccutils.FatalWithCause(errors.Errorf("-machineOwner %q is not an address", *machineOwnerF))
		}
		machineOwner = common.HexToAddress(*machineOwnerF)
		machineOwnerFrom = "-machineOwner"
	}

	logger.Infof("Using EXTENSION_OWNER_KEY (owner: %s)", owner.Hex())
	logger.Infof("Extension id:        %s", extensionID.String())
	logger.Infof("Instructions sender: %s", senderAddr.Hex())
	logger.Infof("Machine owner:       %s (from %s)", machineOwner.Hex(), machineOwnerFrom)

	if err := fccutils.WireReservedExtension(testSupport, privKey, extensionID, senderAddr, machineOwner); err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Extension %s wired. Next: build/deploy the machine image with EXTENSION_ID=%s, "+
		"then run scripts/post-build.sh.", extensionID.String(), extensionID.String())
}
