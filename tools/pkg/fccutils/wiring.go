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
//     extension's instructions sender to the diamond. The register-tee
//     availability check dispatches through this sender, so it must land BEFORE
//     post-build.sh. The verifier is set to the zero address: this extension
//     answers GET /state with the empty state, and with no verifier the registry
//     accepts exactly that and refuses both tee-node's "could not read the
//     extension" sentinel and any other state.
//  2. AddAllowedTeeMachineOwners(machineOwner) — registerTee reverts
//     NotAllowedExtensionOwner for a machine whose owner is not on this list.
//     The address is the machines' initial owner, which is not this caller
//     wherever the two roles are held by different keys; allowlisting the
//     caller instead would widen the set of addresses that may register a
//     machine under this extension, and still leave the real owner out.
//
// The extension's supported key types are deliberately not touched. They exist
// for wallet-key operations, which this extension has none of: its machines
// sign with their enclave keys, and upstream's checklist for a reserved
// extension does not list them either.
//
// Both steps are idempotent — a re-run against an already-wired extension is a
// pair of no-ops, so a failure partway through is fixed by re-running.
func WireReservedExtension(
	s *support.Support, privKey *ecdsa.PrivateKey,
	extensionId *big.Int, instructionsSender, machineOwner common.Address,
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

	// Step 1: publish the instructions sender. The same call sets the state
	// verifier, and this extension registers none: it answers GET /state with
	// the empty state, which the registry accepts on its own while still
	// refusing tee-node's "could not read the extension" sentinel. Either
	// address differing from what is published means a new call.
	var noStateVerifier common.Address
	currentSender, err := s.TeeExtensionRegistry.GetTeeExtensionInstructionsSender(callOpts, extensionId)
	if err != nil {
		return errors.Errorf("reading the current instructions sender: %s", err)
	}
	currentVerifier, err := s.TeeExtensionRegistry.GetTeeExtensionStateVerifier(callOpts, extensionId)
	if err != nil {
		return errors.Errorf("reading the current state verifier: %s", err)
	}
	if currentSender == instructionsSender && currentVerifier == noStateVerifier {
		logger.Infof("Instructions sender already %s and no state verifier set, skipping",
			instructionsSender.Hex())
	} else {
		if currentSender != (common.Address{}) && currentSender != instructionsSender {
			logger.Infof("Replacing instructions sender %s with %s", currentSender.Hex(), instructionsSender.Hex())
		}
		if currentVerifier != noStateVerifier {
			logger.Infof("Clearing the state verifier (was %s): the extension's state is empty, "+
				"which the registry accepts without one", currentVerifier.Hex())
		}
		tx, txErr := s.TeeExtensionRegistry.SetExtensionContracts(opts, extensionId, noStateVerifier, instructionsSender)
		if txErr != nil {
			return errors.Errorf("SetExtensionContracts failed: %s", txErr)
		}
		if _, txErr = support.CheckTx(tx, s.ChainClient); txErr != nil {
			return errors.Errorf("%s", txErr)
		}
		logger.Infof("Extension contracts set: sender %s, no state verifier (tx %s)",
			instructionsSender.Hex(), tx.Hash().Hex())
	}

	// Step 2: allow the machines' owner to register them for this extension.
	alreadyMachineOwner, err := s.TeeOwnerAllowlist.IsAllowedTeeMachineOwner(callOpts, extensionId, machineOwner)
	if err != nil {
		return errors.Errorf("checking TEE machine owner status: %s", err)
	}
	if alreadyMachineOwner {
		logger.Infof("%s already allowed as TEE machine owner, skipping", machineOwner.Hex())
	} else {
		tx, txErr := s.TeeOwnerAllowlist.AddAllowedTeeMachineOwners(opts, extensionId, []common.Address{machineOwner})
		if txErr != nil {
			return errors.Errorf("AddAllowedTeeMachineOwners failed: %s", txErr)
		}
		if _, txErr = support.CheckTx(tx, s.ChainClient); txErr != nil {
			return errors.Errorf("%s", txErr)
		}
		logger.Infof("%s allowed as TEE machine owner (tx %s)", machineOwner.Hex(), tx.Hash().Hex())
	}

	return nil
}
