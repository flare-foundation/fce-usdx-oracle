// Command deploy-store deploys UsdxPriceStore and UsdxCustomFeed.
//
// It runs from scripts/extension-setup.sh, which sits between pre-build.sh (which
// registers the extension and produces EXTENSION_ID) and the image build. That
// ordering is what makes the store address available to pin into the image:
//
//	pre-build.sh        deploy InstructionSender, register extension -> EXTENSION_ID
//	extension-setup.sh  THIS: deploy store + feed -> STORE address
//	start-services.sh   build image with STORE pinned -> code hash
//	post-build.sh       register the TEE machine
//
// The store address must be pinned rather than injected: the signed report binds
// it (see internal/report), so an operator who could change it at runtime could
// obtain TEE signatures over reports bound to a store they control.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/contracts/usdxfeed"
	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	extensionIDF := flag.String("extensionId", "", "extension id (hex), from pre-build.sh")
	feedCategoryF := flag.Uint("feedCategory", 32, "feed category byte, must be 32..63")
	feedNameF := flag.String("feedName", "USDX/USD", "feed name, 1..20 ASCII bytes")
	decimalsF := flag.Int("decimals", 8, "price decimals")
	teeStatusF := flag.Uint("requiredTeeStatus", 1,
		"getTeeMachineStatus value a signer must report — CONFIRM against flare-smart-contracts-v2")
	minPriceF := flag.String("minPrice", "50000000", "minimum acceptable price, scaled")
	maxPriceF := flag.String("maxPrice", "100000000", "maximum acceptable price, scaled")
	maxAgeF := flag.Uint64("maxAge", 3600, "maximum report age in seconds")
	maxSkewF := flag.Uint64("maxSkew", 300, "tolerated clock skew in seconds")
	governanceF := flag.String("governance", "", "governance address (defaults to the deployer)")
	flag.Parse()

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	extensionID, ok := new(big.Int).SetString(strings.TrimPrefix(*extensionIDF, "0x"), 16)
	if !ok || extensionID.Sign() <= 0 {
		fccutils.FatalWithCause(errors.Errorf(
			"invalid -extensionId %q; run pre-build.sh first and pass the value it prints", *extensionIDF))
	}

	feedID, err := buildFeedID(byte(*feedCategoryF), *feedNameF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	minPrice, ok := new(big.Int).SetString(*minPriceF, 10)
	if !ok {
		fccutils.FatalWithCause(errors.New("invalid -minPrice"))
	}
	maxPrice, ok := new(big.Int).SetString(*maxPriceF, 10)
	if !ok {
		fccutils.FatalWithCause(errors.New("invalid -maxPrice"))
	}
	if minPrice.Cmp(maxPrice) > 0 {
		fccutils.FatalWithCause(errors.New("-minPrice exceeds -maxPrice"))
	}

	governance := common.HexToAddress(*governanceF)
	if *governanceF == "" {
		governance = crypto.PubkeyToAddress(s.Prv.PublicKey)
	}
	if governance == (common.Address{}) {
		fccutils.FatalWithCause(errors.New("governance address resolves to zero"))
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Deploying UsdxPriceStore (extensionId=%s, requiredTeeStatus=%d)...",
		extensionID, *teeStatusF)

	storeAddr, tx, _, err := usdxstore.DeployUsdxPriceStore(
		opts, s.ChainClient,
		s.Addresses.FlareTeeManager,
		extensionID,
		uint8(*teeStatusF),
		feedID,
		int8(*decimalsF),
		governance,
		minPrice, maxPrice,
		*maxAgeF, *maxSkewF,
	)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("store deployment failed: %s", err))
	}
	if err := waitMined(s, tx); err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("UsdxPriceStore deployed at %s", storeAddr.Hex())

	logger.Infof("Deploying UsdxCustomFeed...")
	feedAddr, tx, _, err := usdxfeed.DeployUsdxCustomFeed(opts, s.ChainClient, storeAddr, feedID)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("feed deployment failed: %s", err))
	}
	if err := waitMined(s, tx); err != nil {
		fccutils.FatalWithCause(err)
	}
	logger.Infof("UsdxCustomFeed deployed at %s", feedAddr.Hex())

	logger.Infof("Feed id: 0x%x", feedID)
	logger.Infof("Register the feed with Flare governance via addCustomFeeds — " +
		"that is a proposal with a timelock, not a deploy step.")

	// Last line is the store address, so the calling script can capture it the
	// same way pre-build.sh captures the instruction sender.
	fmt.Println(storeAddr.Hex())
}

// buildFeedID mirrors FtsoFeedIdConverter.getFeedId: 1 category byte plus up to
// 20 ASCII name bytes, right-padded with zeros.
func buildFeedID(category byte, name string) ([21]byte, error) {
	var id [21]byte
	if category < 32 || category >= 64 {
		return id, errors.Errorf(
			"feed category %d is outside the custom range 32..63; FtsoV2 rejects it as "+
				"\"invalid feed category\"", category)
	}
	if name == "" || len(name) > 20 {
		return id, errors.Errorf("feed name must be 1..20 bytes, got %d", len(name))
	}
	for i := 0; i < len(name); i++ {
		if name[i] < 0x20 || name[i] > 0x7E {
			return id, errors.New("feed name must be printable ASCII")
		}
	}
	id[0] = category
	copy(id[1:], name)
	return id, nil
}

func waitMined(s *support.Support, tx *types.Transaction) error {
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		return errors.Errorf("tx %s not mined within 2 minutes: %s", tx.Hash().Hex(), err)
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		return errors.Errorf("tx %s reverted", tx.Hash().Hex())
	}
	return nil
}
