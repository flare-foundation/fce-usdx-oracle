package main

import (
	"encoding/json"
	"flag"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

// Expected response shape for the USDX oracle's GET_PRICE operation.
//
// These are deliberately declared here rather than imported from the extension:
// this tool asserts on the *wire format*, and must run unchanged against every
// language implementation (see docs/extension-contract.md). Keeping them local
// is what lets tools/ stay independent of any one implementation.

type getPriceResponse struct {
	Price      string `json:"price"`
	Decimals   int8   `json:"decimals"`
	ObservedAt uint64 `json:"observedAt"`
}

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	instructionSenderF := flag.String("instructionSender", "", "instructionSender address")
	flag.Parse()

	instructionSenderAddress := common.HexToAddress(*instructionSenderF)

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// --- Generic: configure contract -----------------------------------------
	logger.Infof("Setting extension ID on instruction sender...")
	err = instrutils.SetExtensionId(testSupport, instructionSenderAddress)
	if err != nil {
		if strings.Contains(err.Error(), "already set") || strings.Contains(err.Error(), "Extension ID already set") {
			logger.Infof("Extension ID already set on contract, continuing")
		} else {
			logger.Errorf("setExtensionId failed: %s", err)
			fccutils.FatalWithCause(errors.Errorf(
				"setExtensionId failed — is the extension registered? Check that pre-build.sh completed successfully. Error: %s", err))
		}
	}

	// --- Test case 1: Request a price update ---
	logger.Infof("Sending GET_PRICE instruction...")

	instructionId, _, err := instrutils.RequestPriceUpdate(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("Instruction sent. ID: %s", instructionId.Hex())

	time.Sleep(5 * time.Second)

	first, err := verifyPriceResult(*pf, instructionId)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("Test passed: GET_PRICE instruction processed successfully")

	// --- Test case 2: A second request must produce a fresh, consistent result ---
	//
	// The price is a function of observed inputs, not of call count, so this
	// checks the second observation is internally consistent rather than that it
	// changed. It also proves the extension serves repeat instructions rather
	// than answering once and wedging.
	logger.Infof("Sending a second GET_PRICE instruction...")

	secondInstructionId, _, err := instrutils.RequestPriceUpdate(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("Instruction sent. ID: %s", secondInstructionId.Hex())

	if secondInstructionId == instructionId {
		fccutils.FatalWithCause(errors.New("second instruction reused the first instruction ID"))
	}

	time.Sleep(5 * time.Second)

	second, err := verifyPriceResult(*pf, secondInstructionId)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	if second.Decimals != first.Decimals {
		fccutils.FatalWithCause(errors.Errorf(
			"decimals changed between observations: %d then %d — the scale is fixed and consumers rely on it",
			first.Decimals, second.Decimals))
	}
	logger.Infof("Test passed: second GET_PRICE instruction processed successfully")

	logger.Infof("All tests passed.")
}

func verifyPriceResult(proxyURL string, instructionId common.Hash) (getPriceResponse, error) {
	var resp getPriceResponse

	// --- Generic: poll proxy for result (do not modify) ---
	actionResponse, err := fccutils.ActionResult(proxyURL, instructionId)
	if err != nil {
		return resp, err
	}
	actionResult := actionResponse.Result

	if actionResult.Status == 0 {
		return resp, errors.Errorf("instruction processing failed: %s", actionResult.Log)
	}
	if actionResult.Status == 2 {
		return resp, errors.New("instruction still pending after polling, expected completed")
	}

	if len(actionResult.Data) == 0 {
		return resp, errors.New("expected response data but got none")
	}

	err = json.Unmarshal(actionResult.Data, &resp)
	if err != nil {
		return resp, errors.Errorf("failed to unmarshal response: %s", err)
	}

	// Price is a decimal string carrying a uint256. Parse it as a big.Int rather
	// than a float: at the on-chain scale a float64 silently loses precision, and
	// a "valid-looking" price that fails to round-trip is exactly the bug this
	// assertion exists to catch.
	price, ok := new(big.Int).SetString(resp.Price, 10)
	if !ok {
		return resp, errors.Errorf("price %q is not a base-10 integer", resp.Price)
	}
	if price.Sign() <= 0 {
		return resp, errors.Errorf("expected a positive price, got %s", resp.Price)
	}

	if resp.Decimals < 0 {
		return resp, errors.Errorf("expected non-negative decimals, got %d", resp.Decimals)
	}

	// price must not exceed 1.0 — USDX redeems at par, so over-collateralisation
	// is capped rather than reported as a premium (spec §3.2).
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(resp.Decimals)), nil)
	if price.Cmp(one) > 0 {
		return resp, errors.Errorf(
			"price %s exceeds 1.0 at %d decimals (max %s) — the formula caps at par",
			resp.Price, resp.Decimals, one.String())
	}

	if resp.ObservedAt == 0 {
		return resp, errors.New("expected a non-zero observedAt: it is the price store's monotonicity key")
	}

	logger.Infof("Response data: %+v", resp)

	return resp, nil
}
