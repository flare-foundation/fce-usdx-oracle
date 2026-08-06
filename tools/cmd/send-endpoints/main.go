// Command send-endpoints publishes an endpoint configuration on-chain through
// the instruction sender, so the addressed TEE machine receives it as a
// SET_ENDPOINTS instruction.
//
// Credentials never go through this path — the config is calldata and
// therefore public; secrets travel over the direct channel via usdxprov.
//
// The machine's teeId is not a field on the proxy's /info response; it is
// derived from the attested publicKey there (fccutils.GetTeeProxyID), the
// same derivation the registration flow uses.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"os"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	instructionSenderF := flag.String("instructionSender", "", "instructionSender address")
	configF := flag.String("config", "", "endpoints configuration JSON file, delivered verbatim")
	flag.Parse()

	if *instructionSenderF == "" || *configF == "" {
		fccutils.FatalWithCause(errors.New("both -instructionSender and -config are required"))
	}
	instructionSenderAddress := common.HexToAddress(*instructionSenderF)

	configBytes, err := os.ReadFile(*configF)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("reading config file: %s", err))
	}
	if !json.Valid(configBytes) {
		fccutils.FatalWithCause(errors.Errorf("%s is not valid JSON", *configF))
	}

	var prettyConfig bytes.Buffer
	if err := json.Indent(&prettyConfig, configBytes, "", "  "); err != nil {
		fccutils.FatalWithCause(errors.Errorf("formatting config JSON: %s", err))
	}
	logger.Infof("Config from %s (%d bytes, delivered verbatim on-chain):", *configF, len(configBytes))
	logger.Info(prettyConfig.String())

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	teeID, _, err := fccutils.GetTeeProxyID(*pf)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("deriving TEE identity from %s/info: %s", *pf, err))
	}
	logger.Infof("TEE identity derived from attested publicKey: %s", teeID.Hex())

	instructionID, txHash, err := instrutils.SetEndpoints(testSupport, instructionSenderAddress, teeID, configBytes)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Endpoints published on-chain:")
	logger.Infof("  tx:          %s", txHash.Hex())
	logger.Infof("  instruction: %s", instructionID.Hex())
	logger.Infof("  configHash:  %s", crypto.Keccak256Hash(configBytes).Hex())
	logger.Infof("The machine applies it on delivery; follow with:")
	logger.Infof("  docker compose logs extension-tee | grep SET_ENDPOINTS")
}
