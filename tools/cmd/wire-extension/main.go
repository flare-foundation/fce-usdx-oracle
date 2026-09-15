// Command wire-extension performs the extension-owner wiring of a reserved
// extension after flare-smart-contracts-v2 has deployed its contracts and
// Flare governance has run registerReserved(extensionId, owner).
//
// It makes three direct owner calls on the FlareTeeManager diamond, each
// idempotent:
//
//  1. setExtensionContracts(extensionId, verifier, sender) — register-tee's
//     availability check dispatches through this sender and passes the
//     machine's attested state through this verifier, so run this BEFORE
//     post-build.sh.
//  2. AddAllowedTeeMachineOwners(extensionId, [owner])
//  3. addSupportedKeyTypes(extensionId, [EVM])
//
// The instructions sender resolves from -sender, or from the
// TeeOracleInstructionsSender entry in the addresses file (put there by
// scripts/import-upstream-addresses.sh after the upstream deploy).
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
	verifierF := flag.String("stateVerifier", "",
		"UsdxTeeExtensionStateVerifier address (default: the UsdxTeeExtensionStateVerifier "+
			"entry in the addresses file); the registry passes every machine's attested state "+
			"through it, so without one no machine reaches production")
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
	verifierAddr, err := configs.ResolveContract(*verifierF, *af, "UsdxTeeExtensionStateVerifier")
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the state verifier: %s — deploy it "+
			"(deploy-oracle-local.sh on the simulation; contracts/script/DeployStateVerifier.s.sol "+
			"elsewhere) and add its address to the addresses file, or pass -stateVerifier", err))
	}

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// Same key resolution as allow-tee-version: EXTENSION_OWNER_KEY overrides,
	// otherwise DEPLOYMENT_PRIVATE_KEY. Whichever it is, THIS address must be
	// the owner governance registered with registerReserved.
	privKey := testSupport.Prv
	keySource := "DEPLOYMENT_PRIVATE_KEY (default)"
	if pk := os.Getenv("EXTENSION_OWNER_KEY"); pk != "" {
		pk = strings.TrimPrefix(strings.TrimPrefix(pk, "0x"), "0X")
		privKey, err = crypto.HexToECDSA(pk)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("EXTENSION_OWNER_KEY: %s", err))
		}
		keySource = "EXTENSION_OWNER_KEY"
	}

	logger.Infof("Using key: %s (owner: %s)", keySource, crypto.PubkeyToAddress(privKey.PublicKey).Hex())
	logger.Infof("Extension id:        %s", extensionID.String())
	logger.Infof("Instructions sender: %s", senderAddr.Hex())
	logger.Infof("State verifier:      %s", verifierAddr.Hex())

	if err := fccutils.WireReservedExtension(testSupport, privKey, extensionID, senderAddr, verifierAddr); err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Extension %s wired. Next: build/deploy the machine image with EXTENSION_ID=%s, "+
		"then run scripts/post-build.sh.", extensionID.String(), extensionID.String())
}
