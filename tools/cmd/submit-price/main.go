// Command submit-price runs one keeper cycle by hand.
//
//	trigger   requestPriceUpdate() on UsdxOracleInstructionSender
//	collect   poll the TEE proxy for the ActionResult
//	relay     abi.decode the payload and call UsdxPriceStore.submitPrice
//
// The same code the keeper runs on a timer, plus operator-facing detail: the
// decoded report, and what a consumer sees afterwards.
//
// Usage:
//
//	go run ./cmd/submit-price -instructionSender 0x…            # request, wait, submit
//	go run ./cmd/submit-price -instructionId 0x…                # relay an existing result
//	go run ./cmd/submit-price -instructionSender 0x… -dry-run   # preflight only
//	go run ./cmd/submit-price -instructionSender 0x… -direct -p https://machine-b/  # one named machine
//
// The -direct form is also the commissioning step for a new TEE machine: an
// on-chain request goes to a random machine, so it is the only way to make a
// CHOSEN machine sign and land a price, which is what records its config hash in
// the store. See "Commissioning a machine" in docs/usdx-oracle-keeper.md.
package main

import (
	"context"
	"flag"
	"os"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/contracts/usdxfeed"
	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/pricereport"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	senderF := flag.String("instructionSender", "",
		"UsdxOracleInstructionSender address; required unless -instructionId is given")
	storeF := flag.String("store", "",
		"UsdxPriceStore address (default: the UsdxPriceStore entry in the addresses file)")
	feedF := flag.String("feed", "",
		"UsdxCustomFeed address, read back after submission (default: from the addresses file)")
	instructionF := flag.String("instructionId", "",
		"relay the result of this instruction instead of requesting a new one")
	pollF := flag.Duration("pollTimeout", 5*time.Minute,
		"how long to wait for a terminal action result")
	submitF := flag.Duration("submitTimeout", 3*time.Minute,
		"how long to wait for the submitPrice receipt")
	dryRunF := flag.Bool("dry-run", false,
		"decode and run the preflight checks, but send no transaction")
	directF := flag.Bool("direct", false,
		"raise GET_PRICE on the proxy at -p instead of on-chain: no registry fee, no trigger "+
			"transaction, and the request reaches THAT machine rather than a random one")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for -direct (default: $DIRECT_API_KEY)")
	flag.Parse()

	if *senderF == "" && *instructionF == "" {
		fccutils.FatalWithCause(errors.New(
			"one of -instructionSender (to request a price) or -instructionId (to relay one) is required"))
	}
	if *directF && *apiKeyF == "" {
		fccutils.FatalWithCause(errors.New(
			"-direct needs an API key: pass -directApiKey or set DIRECT_API_KEY"))
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "UsdxPriceStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("UsdxPriceStore: %s", storeAddr.Hex())

	ctx := context.Background()

	// Relaying a specific instruction skips the trigger, so it bypasses the
	// keeper's cycle, which always requests a fresh price.
	if *instructionF != "" {
		// A direct action's result is filed under "submit", an instruction's
		// under "threshold". Getting this wrong polls a 404 to the deadline.
		tag := teetypes.Threshold
		if *directF {
			tag = teetypes.Submit
		}
		relayExisting(ctx, s, storeAddr, *pf, common.HexToHash(*instructionF), tag, *pollF, *submitF, *dryRunF)
		reportStoredState(s, storeAddr, *feedF, *af)
		return
	}

	cfg := keeper.Config{
		KeeperID:      "submit-price",
		Sender:        common.HexToAddress(*senderF),
		Store:         storeAddr,
		Proxy:         *pf,
		Direct:        *directF,
		DirectAPIKey:  *apiKeyF,
		PollTimeout:   *pollF,
		SubmitTimeout: *submitF,
		DryRun:        *dryRunF,
	}

	rec, err := keeper.RunCycle(ctx, s, cfg)
	logger.Infof("outcome: %s", rec.Outcome)
	if rec.Price != "" {
		logger.Infof("  price:      %s at %d decimals", rec.Price, rec.Decimals)
		logger.Infof("  observedAt: %d (%s)", rec.ObservedAt,
			time.Unix(int64(rec.ObservedAt), 0).UTC().Format(time.RFC3339))
	}
	if rec.SubmitTx != "" {
		logger.Infof("  tx:         %s", rec.SubmitTx)
	}

	if err != nil && !rec.Outcome.Advanced() {
		// A lost race is not a failure: the price landed, submitted by someone else.
		fccutils.FatalWithCause(err)
	}
	if *dryRunF {
		logger.Infof("Dry run: preflight passed, no transaction sent.")
		return
	}

	reportStoredState(s, storeAddr, *feedF, *af)
}

// relayExisting is the recovery path for a cycle interrupted after its trigger.
func relayExisting(
	ctx context.Context, s *support.Support, storeAddr common.Address, proxy string,
	instructionID common.Hash, tag teetypes.SubmissionTag,
	pollTimeout, submitTimeout time.Duration, dryRun bool,
) {
	logger.Infof("Relaying an existing action: %s (submissionTag %s)", instructionID.Hex(), tag)

	result, err := keeper.PollResult(ctx, proxy, instructionID, tag, pollTimeout)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	if result.Status != 1 {
		fccutils.FatalWithCause(errors.Errorf(
			"the extension published nothing (status %d): %s", result.Status, result.Log))
	}

	report, err := pricereport.Decode(result.Data)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("decoding the action result: %s", err))
	}
	logReport(report)

	if err := keeper.Preflight(s, storeAddr, report); err != nil {
		fccutils.FatalWithCause(err)
	}
	if dryRun {
		logger.Infof("Dry run: preflight passed, no transaction sent.")
		return
	}

	store, err := usdxstore.NewUsdxPriceStore(storeAddr, s.ChainClient)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("binding the store: %s", err))
	}
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Submitting...")
	tx, err := store.SubmitPrice(opts, report.Body, report.Signature)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("submitPrice rejected before broadcast: %s", err))
	}

	ctx, cancel := context.WithTimeout(ctx, submitTimeout)
	defer cancel()
	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("tx %s was not mined: %s", tx.Hash().Hex(), err))
	}
	if receipt.Status != 1 {
		fccutils.FatalWithCause(errors.Errorf(
			"tx %s reverted — re-run with -dry-run to see which guard rejects it", tx.Hash().Hex()))
	}
	logger.Infof("Accepted in %s", tx.Hash().Hex())
}

// reportStoredState reads back what a consumer would now see.
func reportStoredState(s *support.Support, storeAddr common.Address, feedFlag, addressesFile string) {
	opts := &bind.CallOpts{Context: context.Background()}

	store, err := usdxstore.NewUsdxPriceStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		logger.Warnf("binding the store: %s", err)
		return
	}

	latest, err := store.Latest(opts)
	if err != nil {
		logger.Warnf("reading the store back: %s", err)
		return
	}
	logger.Infof("Store now reports: price=%s decimals=%d timestamp=%d (%s)",
		latest.Price, latest.Decimals, latest.Timestamp,
		time.Unix(int64(latest.Timestamp), 0).UTC().Format(time.RFC3339))

	feedAddr, err := configs.ResolveContract(feedFlag, addressesFile, "UsdxCustomFeed")
	if err != nil {
		logger.Warnf("no feed address to read back: %s", err)
		return
	}
	feed, err := usdxfeed.NewUsdxCustomFeedCaller(feedAddr, s.ChainClient)
	if err != nil {
		logger.Warnf("binding the feed: %s", err)
		return
	}
	// getCurrentFeed is payable and so not a view in the binding; exercised here
	// through the feed id and the store it reads.
	id, err := feed.FeedId(opts)
	if err != nil {
		logger.Warnf("reading the feed id: %s", err)
		return
	}
	logger.Infof("UsdxCustomFeed %s serves feed id 0x%x", feedAddr.Hex(), id)
}

func logReport(r pricereport.Report) {
	logger.Infof("Price report decoded:")
	logger.Infof("  price:         %s (%s at %d decimals)", r.Price, r.Human(), r.Decimals)
	logger.Infof("  observedAt:    %d (%s)", r.ObservedAt,
		time.Unix(int64(r.ObservedAt), 0).UTC().Format(time.RFC3339))
	logger.Infof("  instructionId: %s", r.InstructionID.Hex())
	logger.Infof("  configHash:    %s", r.ConfigHash.Hex())
	logger.Infof("  adminsHash:    %s", r.AdminsHash.Hex())
	logger.Infof("  body:          %d bytes, signature: %d bytes", len(r.Body), len(r.Signature))
}
