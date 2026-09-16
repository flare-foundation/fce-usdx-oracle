package main

import (
	"encoding/hex"
	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"
	"flag"
	"os"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url (used to query TEE info)")
	hf := flag.String("h", "", "host url to register on-chain (defaults to -p if not set)")
	epf := flag.String("ep", "http://localhost:6662", "external proxy url (for FTDC)")
	instructionF := flag.String("i", "", "instructionID")
	command := flag.String("command", "rap", "command (rap)")
	stateFile := flag.String("state", "../deploy/config/register-tee.state", "state file for resume support")
	resume := flag.Bool("resume", false, "resume from state file (default: start fresh)")

	flag.Parse()

	// Default: start fresh. Only resume if --resume is explicitly passed.
	if !*resume {
		if err := os.Remove(*stateFile); err != nil && !os.IsNotExist(err) {
			logger.Warnf("WARNING: failed to remove stale state file: %v", err)
		}
	}

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// The registry takes a registration only from the machine's own initial
	// owner, and the same address takes the machine to production, so this is
	// the machine owner's key rather than the deployment key. They are one key
	// on a test deployment and separate ones wherever the extension owner is not
	// an allowed machine owner, which is the usual arrangement.
	keySource := "DEPLOYMENT_PRIVATE_KEY (default)"
	if key, keyErr := fccutils.OverrideKey("MACHINE_OWNER_KEY"); keyErr != nil {
		fccutils.FatalWithCause(keyErr)
	} else if key != nil {
		testSupport.Prv = key
		keySource = "MACHINE_OWNER_KEY"
	}
	sender := crypto.PubkeyToAddress(testSupport.Prv.PublicKey)

	// get teeID from proxy
	teeInfo, err := fccutils.TeeInfo(*pf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// Checked here rather than left to the chain: the registry answers a
	// mismatch with OnlyOwner, which says nothing about which of the two
	// addresses is wrong, and the machine's owner is fixed in its launch
	// environment where it cannot be corrected from here.
	if owner := teeInfo.MachineData.InitialOwner; owner != sender {
		fccutils.FatalWithCause(errors.Errorf(
			"this key is %s but the machine reports %s as its initial owner, and only that "+
				"address may register it — set MACHINE_OWNER_KEY to its key, or launch the "+
				"machine with INITIAL_OWNER=%s", sender.Hex(), owner.Hex(), sender.Hex()))
	}
	logger.Infof("Using key: %s (machine owner: %s)", keySource, sender.Hex())

	teeID, _, err := fccutils.TeeProxyId(teeInfo)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	ftdcTeeID, _, err := fccutils.GetTeeProxyID(*epf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// to check if things are ok
	_, _, err = fccutils.GetCodeHashAndPlatform(teeInfo)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	hostURL := *hf
	if hostURL == "" {
		hostURL = *pf
	}

	logger.Infof("Registration of TEE with ID %s", hex.EncodeToString(teeID[:]))
	err = fccutils.RegisterNode(testSupport, teeInfo, hostURL, *epf, ftdcTeeID, *command, *instructionF, *stateFile)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Registered TEE node with id %s", teeID)
}
