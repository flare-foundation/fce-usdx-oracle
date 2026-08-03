package utils

import (
	"context"
	"math/big"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/usdxoracle"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

func DeployInstructionSender(s *support.Support) (common.Address, *usdxoracle.UsdxOracleInstructionSender, error) {
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("failed to create transactor: %s", err)
	}

	// Both registry args are the FlareTeeManager diamond proxy: the diamond
	// routes ExtensionManager and MachineManager calls to the right facets.
	address, tx, contract, err := usdxoracle.DeployUsdxOracleInstructionSender(
		opts, s.ChainClient, s.Addresses.FlareTeeManager, s.Addresses.FlareTeeManager,
	)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("failed to deploy contract: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("deployment tx not mined within 2 minutes (tx: %s): %s", tx.Hash().Hex(), err)
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		return common.Address{}, nil, errors.New("contract deployment failed")
	}

	return address, contract, nil
}

func SetExtensionId(s *support.Support, instructionSenderAddress common.Address) error {
	sender, err := usdxoracle.NewUsdxOracleInstructionSender(instructionSenderAddress, s.ChainClient)
	if err != nil {
		return errors.Errorf("failed to bind contract: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return errors.Errorf("failed to create transactor: %s", err)
	}

	tx, err := sender.SetExtensionId(opts)
	if err != nil {
		reason := fccutils.DecodeRevertReason(err)
		if reason == "" {
			parsed, _ := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
			if parsed != nil {
				callData, packErr := parsed.Pack("setExtensionId")
				if packErr == nil {
					from := crypto.PubkeyToAddress(s.Prv.PublicKey)
					reason = fccutils.SimulateAndDecodeRevert(
						s.ChainClient, from, instructionSenderAddress, nil, callData,
					)
				}
			}
		}
		if reason != "" {
			return errors.Errorf("failed to call setExtensionId: %s (revert reason: %s)", err, reason)
		}
		return errors.Errorf("failed to call setExtensionId: %s", err)
	}

	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return errors.Errorf("failed waiting for transaction: %s", err)
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		parsed, _ := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
		if parsed != nil {
			callData, packErr := parsed.Pack("setExtensionId")
			if packErr == nil {
				from := crypto.PubkeyToAddress(s.Prv.PublicKey)
				reason := fccutils.SimulateAndDecodeRevert(
					s.ChainClient, from, instructionSenderAddress, nil, callData,
				)
				if reason != "" {
					return errors.Errorf("setExtensionId transaction failed (revert reason: %s)", reason)
				}
			}
		}
		return errors.New("setExtensionId transaction failed")
	}

	return nil
}


// instructionFeeWei is the per-instruction fee charged by the extension registry.
const instructionFeeWei = 1000000

// RequestPriceUpdate calls requestPriceUpdate() on the instruction sender and
// returns the resulting instruction ID and transaction hash.
//
// The call takes no arguments: the extension pins the target chain, store and
// feed id in its own configuration rather than accepting them from the caller
// (see contracts/InstructionSender.sol).
func RequestPriceUpdate(s *support.Support, instructionSenderAddress common.Address) (common.Hash, common.Hash, error) {
	sender, err := usdxoracle.NewUsdxOracleInstructionSender(instructionSenderAddress, s.ChainClient)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to bind contract: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to create transactor: %s", err)
	}
	opts.Value = big.NewInt(instructionFeeWei) // must match the registry's required fee

	// simulateRevert re-runs the call with eth_call to recover a revert reason,
	// which the raw send/receipt path does not surface.
	simulateRevert := func() string {
		parsed, abiErr := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
		if abiErr != nil || parsed == nil {
			return ""
		}
		callData, packErr := parsed.Pack("requestPriceUpdate")
		if packErr != nil {
			return ""
		}
		from := crypto.PubkeyToAddress(s.Prv.PublicKey)
		return fccutils.SimulateAndDecodeRevert(
			s.ChainClient, from, instructionSenderAddress,
			big.NewInt(instructionFeeWei), callData,
		)
	}

	tx, err := sender.RequestPriceUpdate(opts)
	if err != nil {
		reason := fccutils.DecodeRevertReason(err)
		if reason == "" {
			reason = simulateRevert()
		}
		if reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s (revert reason: %s)", err, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s", err)
	}

	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed waiting for transaction: %s", err)
	}

	if receipt.Status != 1 {
		if reason := simulateRevert(); reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status %d (revert reason: %s)", receipt.Status, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status: %d", receipt.Status)
	}

	// Scan for TeeInstructionsSent rather than assuming it is the first log.
	// requestPriceUpdate() emits its own PriceUpdateRequested event alongside the
	// registry's, so a positional assumption would break the moment the emit
	// order changes.
	for _, l := range receipt.Logs {
		if l == nil {
			continue
		}
		instructionSent, parseErr := s.TeeVerification.ParseTeeInstructionsSent(*l)
		if parseErr == nil {
			return instructionSent.InstructionId, receipt.TxHash, nil
		}
	}

	return common.Hash{}, common.Hash{}, errors.Errorf(
		"no TeeInstructionsSent event in receipt (%d logs) for tx %s", len(receipt.Logs), receipt.TxHash.Hex())
}
