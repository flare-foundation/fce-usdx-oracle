// Command set-store-extension repoints UsdxPriceStore at a new extension
// generation, so a re-registration does not force a store redeploy (which would
// reset the stored price and invalidate the addCustomFeeds registration).
//
// Governance-only. Both values move together: a re-registration mints a new id
// AND a new sender, so changing one alone would leave the store checking a
// config hash published by a contract nobody uses.
//
//	go run ./cmd/set-store-extension -store 0x… -extensionId 0x… -instructionSender 0x…
package main

import (
	"context"
	"flag"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
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
	storeF := flag.String("store", "", "UsdxPriceStore address")
	extensionIDF := flag.String("extensionId", "", "new extension id (hex), from pre-build.sh")
	senderF := flag.String("instructionSender", "", "new UsdxOracleInstructionSender address")
	flag.Parse()

	if *storeF == "" || *extensionIDF == "" || *senderF == "" {
		fccutils.FatalWithCause(errors.New(
			"-store, -extensionId and -instructionSender are all required"))
	}

	extensionID, ok := new(big.Int).SetString(strings.TrimPrefix(*extensionIDF, "0x"), 16)
	if !ok || extensionID.Sign() <= 0 {
		fccutils.FatalWithCause(errors.Errorf(
			"invalid -extensionId %q; pass the value pre-build.sh printed", *extensionIDF))
	}
	if !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("invalid -instructionSender %q", *senderF))
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr := common.HexToAddress(*storeF)
	store, err := usdxstore.NewUsdxPriceStore(storeAddr, s.ChainClient)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("binding the store: %s", err))
	}

	opts := &bind.CallOpts{Context: context.Background()}
	caller := crypto.PubkeyToAddress(s.Prv.PublicKey)

	governance, err := store.Governance(opts)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf(
			"reading governance from %s: %s — check the address is a UsdxPriceStore", storeAddr.Hex(), err))
	}
	if governance != caller {
		fccutils.FatalWithCause(errors.Errorf(
			"NotGovernance: the store's governance is %s, this key is %s",
			governance.Hex(), caller.Hex()))
	}

	oldID, _ := store.ExtensionId(opts)
	oldSource, _ := store.ConfigSource(opts)
	logger.Infof("Store:        %s", storeAddr.Hex())
	logger.Infof("  extensionId:  %s -> %s", oldID, extensionID)
	logger.Infof("  configSource: %s -> %s", oldSource.Hex(), *senderF)

	// setExtension does not touch the stored price: it keeps ageing under the
	// previous generation's value. Use setInvalidated if consumers must not read
	// it in the meantime.
	if price, err := store.Price(opts); err == nil && price.Sign() > 0 {
		observedAt, _ := store.ObservedAt(opts)
		logger.Warnf("The stored price %s (observedAt %d) survives this change and keeps ageing; "+
			"use setInvalidated if it must not be read until the new generation publishes.",
			price, observedAt)
	}

	txOpts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	tx, err := store.SetExtension(txOpts, extensionID, common.HexToAddress(*senderF))
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("setExtension rejected before broadcast: %s", err))
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("tx %s not mined: %s", tx.Hash().Hex(), err))
	}
	if receipt.Status != types.ReceiptStatusSuccessful {
		fccutils.FatalWithCause(errors.Errorf("tx %s reverted", tx.Hash().Hex()))
	}

	logger.Infof("Repointed in %s", tx.Hash().Hex())
}
