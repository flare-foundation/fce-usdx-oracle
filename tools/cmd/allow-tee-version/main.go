package main

import (
	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"
	"flag"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"os"
	"strings"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "proxy url")
	versionF := flag.String("version", "v0.1.0", "version")
	expectedF := flag.String("code-hash", os.Getenv("EXPECTED_CODE_HASH"),
		"the code hash to allow, known out of band (the deployed image's id); the machine's own "+
			"report must match it (default: EXPECTED_CODE_HASH)")
	flag.Parse()

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// get teeID from proxy
	teeInfo, err := fccutils.TeeInfo(*pf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// addTeeVersion is the extension owner's, which is the key every tool signs
	// with unless it names another role's.
	privKey := testSupport.Prv
	logger.Infof("Using EXTENSION_OWNER_KEY (%s)", crypto.PubkeyToAddress(privKey.PublicKey).Hex())

	teeID, _, err := fccutils.TeeProxyId(teeInfo)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Code hash:    %s (source: proxy /info)", teeInfo.MachineData.CodeHash.Hex())
	logger.Infof("Platform:     %s (source: proxy /info)", teeInfo.MachineData.Platform.Hex())
	logger.Infof("Extension ID: %s", teeInfo.MachineData.ExtensionID.Big().String())
	logger.Infof("TEE ID:       %s", teeID.Hex())
	logger.Infof("Version:      %s", *versionF)
	// The hash allowed here is what the registry will accept for the extension,
	// so it must be the build the operator means, not whatever the machine says
	// it runs: given out of band, the machine's report has to match it; without
	// it, a simulated machine reports tee-node's constant and nothing is learnt,
	// while a real one is allowed on its own word, which is said out loud.
	if *expectedF != "" {
		if !strings.EqualFold(strings.TrimPrefix(*expectedF, "0x"), strings.TrimPrefix(teeInfo.MachineData.CodeHash.Hex(), "0x")) {
			logger.Fatalf("the machine reports code hash %s, EXPECTED_CODE_HASH is %s — not allowing it", teeInfo.MachineData.CodeHash.Hex(), *expectedF)
		}
		logger.Infof("Code hash matches EXPECTED_CODE_HASH")
	} else if os.Getenv("SIMULATED_TEE") != "true" {
		logger.Warnf("WARN: allowing the code hash the machine itself reports; set EXPECTED_CODE_HASH to the deployed image's id to pin the build")
	}

	// Idempotency: skip if this codeHash+platform combo is already registered and active.
	// Avoids sending a tx that would revert with VersionAlreadyExists() on re-runs.
	supported, err := testSupport.TeeExtensionRegistry.IsCodeHashPlatformSupported(
		nil,
		teeInfo.MachineData.ExtensionID.Big(),
		teeInfo.MachineData.CodeHash,
		teeInfo.MachineData.Platform,
	)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	if supported {
		logger.Infof("version already registered for this code hash + platform, skipping")
		return
	}

	err = fccutils.AddTeeVersion(testSupport, privKey, teeInfo.MachineData.ExtensionID.Big(), teeInfo.MachineData.CodeHash, teeInfo.MachineData.Platform, *versionF)
	if err != nil {
		if strings.Contains(err.Error(), "VersionAlreadyExists") {
			logger.Infof("version already registered, skipping")
		} else {
			fccutils.FatalWithCause(err)
		}
	}
}
