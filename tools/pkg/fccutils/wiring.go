package fccutils

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/wallets"
	"github.com/pkg/errors"
)

// WireReservedExtension performs the extension-owner half of a reserved-id
// registration. Flare governance runs registerReserved(extensionId, owner) on
// the diamond first (timelocked, cannot be scripted from any repo); everything
// here is a direct owner call that follows it.
//
// The contracts themselves — TeeOracleInstructionsSender and TeeOracleFeedStore
// — are deployed from flare-smart-contracts-v2 by its own script
// (deployment/scripts/DeployTeeOracle.s.sol there), so there is nothing to
// deploy here, only wire:
//
//  1. setExtensionContracts(extensionId, verifier, sender) — publishes the
//     extension's instructions sender and state verifier to the diamond. The
//     register-tee availability check dispatches through this sender, so it
//     must land BEFORE post-build.sh; and the registry passes the machine's
//     attested extension state through the verifier on that check, refusing an
//     empty verifier address unless the state is empty too. tee-node reports
//     the extension's state, so the verifier (UsdxTeeExtensionStateVerifier,
//     deployed from contracts/) is required for any machine to reach production.
//  2. AddAllowedTeeMachineOwners — registerTee reverts NotAllowedExtensionOwner
//     for a machine whose owner is not on this list.
//  3. addSupportedKeyTypes([EVM]) — parity with what register() did for
//     minted (non-reserved) extensions.
//
// Every step is idempotent — a re-run against an already-wired extension is a
// sequence of no-ops, so a failure partway through is fixed by re-running.
func WireReservedExtension(
	s *support.Support, privKey *ecdsa.PrivateKey,
	extensionId *big.Int, instructionsSender, stateVerifier common.Address,
) error {
	owner := crypto.PubkeyToAddress(privKey.PublicKey)
	callOpts := &bind.CallOpts{From: owner, Context: context.Background()}

	// Fail fast before paying for anything. A reserved id shows its owner once
	// registerReserved has landed; a zero owner (or an error) means governance
	// has not run it yet — nothing below can succeed before it does.
	registeredOwner, err := s.TeeExtensionRegistry.GetExtensionOwner(callOpts, extensionId)
	if err != nil {
		return errors.Errorf("reading the owner of extension %s: %s — has governance run "+
			"registerReserved(%s, owner) yet?", extensionId.String(), err, extensionId.String())
	}
	if registeredOwner == (common.Address{}) {
		return errors.Errorf("extension %s has no owner — governance must run "+
			"registerReserved(%s, %s) before this step", extensionId.String(), extensionId.String(), owner.Hex())
	}
	if registeredOwner != owner {
		return errors.Errorf("extension %s is owned by %s, not by this key (%s) — only the "+
			"registered owner can wire it; check which address was handed to registerReserved",
			extensionId.String(), registeredOwner.Hex(), owner.Hex())
	}
	logger.Infof("Extension %s is registered; owner %s confirmed", extensionId.String(), owner.Hex())

	opts, err := bind.NewKeyedTransactorWithChainID(privKey, s.ChainID)
	if err != nil {
		return errors.Errorf("%s", err)
	}

	// Step 1: publish the instructions sender and the state verifier. One call
	// sets both, so either differing means a new call.
	currentSender, err := s.TeeExtensionRegistry.GetTeeExtensionInstructionsSender(callOpts, extensionId)
	if err != nil {
		return errors.Errorf("reading the current instructions sender: %s", err)
	}
	currentVerifier, err := s.TeeExtensionRegistry.GetTeeExtensionStateVerifier(callOpts, extensionId)
	if err != nil {
		return errors.Errorf("reading the current state verifier: %s", err)
	}
	if currentSender == instructionsSender && currentVerifier == stateVerifier {
		logger.Infof("Instructions sender already %s and state verifier already %s, skipping",
			instructionsSender.Hex(), stateVerifier.Hex())
	} else {
		if currentSender != (common.Address{}) && currentSender != instructionsSender {
			logger.Infof("Replacing instructions sender %s with %s", currentSender.Hex(), instructionsSender.Hex())
		}
		if currentVerifier != stateVerifier {
			logger.Infof("Setting state verifier %s (was %s)", stateVerifier.Hex(), currentVerifier.Hex())
		}
		tx, txErr := s.TeeExtensionRegistry.SetExtensionContracts(opts, extensionId, stateVerifier, instructionsSender)
		if txErr != nil {
			return errors.Errorf("SetExtensionContracts failed: %s", txErr)
		}
		if _, txErr = support.CheckTx(tx, s.ChainClient); txErr != nil {
			return errors.Errorf("%s", txErr)
		}
		logger.Infof("Extension contracts set: sender %s, state verifier %s (tx %s)",
			instructionsSender.Hex(), stateVerifier.Hex(), tx.Hash().Hex())
	}

	// Step 2: allow the owner to register TEE machines for this extension.
	alreadyMachineOwner, err := s.TeeOwnerAllowlist.IsAllowedTeeMachineOwner(callOpts, extensionId, owner)
	if err != nil {
		return errors.Errorf("checking TEE machine owner status: %s", err)
	}
	if alreadyMachineOwner {
		logger.Infof("%s already allowed as TEE machine owner, skipping", owner.Hex())
	} else {
		tx, txErr := s.TeeOwnerAllowlist.AddAllowedTeeMachineOwners(opts, extensionId, []common.Address{owner})
		if txErr != nil {
			return errors.Errorf("AddAllowedTeeMachineOwners failed: %s", txErr)
		}
		if _, txErr = support.CheckTx(tx, s.ChainClient); txErr != nil {
			return errors.Errorf("%s", txErr)
		}
		logger.Infof("%s allowed as TEE machine owner (tx %s)", owner.Hex(), tx.Hash().Hex())
	}

	// Step 3: support the EVM key type on the extension, as register() did for
	// minted extensions. The TEE oracle itself signs with the machine's enclave
	// key; this keeps wallet-key operations available should they ever be used.
	evmSupported, err := s.TeeExtensionRegistry.IsKeyTypeSupported(callOpts, extensionId, wallets.EVMType)
	if err != nil {
		return errors.Errorf("checking EVM key type support: %s", err)
	}
	if evmSupported {
		logger.Infof("Key type EVM already supported, skipping")
	} else {
		tx, txErr := s.TeeExtensionRegistry.AddSupportedKeyTypes(opts, extensionId, [][32]byte{wallets.EVMType})
		if txErr != nil {
			return errors.Errorf("AddSupportedKeyTypes failed: %s", txErr)
		}
		if _, txErr = support.CheckTx(tx, s.ChainClient); txErr != nil {
			return errors.Errorf("%s", txErr)
		}
		logger.Infof("Key type EVM added (tx %s)", tx.Hash().Hex())
	}

	return nil
}
