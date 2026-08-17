package main

import (
	"flag"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/pricereport"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

// Decoding lives in tools/pkg/pricereport, which declares the wire layout
// independently of any one implementation. What stays here are the assertions:
// whether a real machine's payload is one UsdxPriceStore would accept.

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

	// --- Test case 2: a second request must produce a consistent result ---
	// The price depends on observed inputs, not call count, so this checks internal
	// consistency and that the extension serves repeats rather than wedging.
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

func verifyPriceResult(proxyURL string, instructionId common.Hash) (pricereport.Report, error) {
	var resp pricereport.Report

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

	// Decode verifies the domain separator: a wrong one means the payload was
	// built for another schema.
	resp, err = pricereport.Decode(actionResult.Data)
	if err != nil {
		return resp, errors.Errorf("failed to decode response: %s", err)
	}

	// Without this a result could be replayed against a different request.
	if resp.InstructionID != instructionId {
		return resp, errors.Errorf(
			"report is for instruction %s, expected %s", resp.InstructionID.Hex(), instructionId.Hex())
	}

	// v normalised to 27/28 so the store can use stock ECDSA recovery.
	if len(resp.Signature) != 65 {
		return resp, errors.Errorf("expected a 65-byte signature, got %d bytes", len(resp.Signature))
	}
	if v := resp.Signature[64]; v != 27 && v != 28 {
		return resp, errors.Errorf("expected signature v in {27,28}, got %d", v)
	}

	// Commits the report to the configuration it was computed under. Zero is the
	// store's "no configuration published" sentinel.
	if resp.ConfigHash == (common.Hash{}) {
		return resp, errors.New("expected a non-zero endpoints hash: the store rejects a zero as unconfigured")
	}

	// Compared as a big.Int, not a float: at the on-chain scale a float64
	// silently loses precision.
	if resp.Price.Sign() <= 0 {
		return resp, errors.Errorf("expected a positive price, got %s", resp.Price.String())
	}

	if resp.Decimals < 0 {
		return resp, errors.Errorf("expected non-negative decimals, got %d", resp.Decimals)
	}

	// USDX redeems at par, so over-collateralisation is capped, not a premium.
	one := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(resp.Decimals)), nil)
	if resp.Price.Cmp(one) > 0 {
		return resp, errors.Errorf(
			"price %s exceeds 1.0 at %d decimals (max %s) — the formula caps at par",
			resp.Price.String(), resp.Decimals, one.String())
	}

	if resp.ObservedAt == 0 {
		return resp, errors.New("expected a non-zero observedAt: it is the price store's monotonicity key")
	}

	// What a relayer does before submitting, so a malformed signature is caught
	// here rather than as an opaque revert.
	teeID, err := resp.Signer()
	if err != nil {
		return resp, errors.Errorf("report does not recover to a signer: %s", err)
	}

	logger.Infof("Price report: price=%s (%s) decimals=%d observedAt=%d instructionId=%s configHash=%s signer=%s",
		resp.Price.String(), resp.Human(), resp.Decimals, resp.ObservedAt,
		resp.InstructionID.Hex(), resp.ConfigHash.Hex(), teeID.Hex())

	return resp, nil
}
