package fccutils

import (
	"context"
	"crypto/ecdsa"
	"math/big"

	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// SetTeeGovernance registers the TEE governance signer set + threshold via
// ExtensionGovernance.SetNewTeeGovernance.
//
// The node derives its governanceHash from the SAME (signers, threshold) pair,
// and MachineManager.register only accepts a machine whose signed
// governanceHash matches — so this must stay consistent with the node's env.
//
// Idempotent: skipped when the on-chain hash already matches.
func SetTeeGovernance(s *support.Support, privKey *ecdsa.PrivateKey, extensionId *big.Int, signers []common.Address, threshold uint64) error {
	if len(signers) == 0 {
		return errors.New("at least one governance signer is required (set GOVERNANCE_SIGNERS)")
	}
	if threshold == 0 || threshold > uint64(len(signers)) {
		return errors.Errorf("invalid GOVERNANCE_THRESHOLD %d for %d signer(s)", threshold, len(signers))
	}

	desiredHash, err := types.GovernanceHash(signers, threshold)
	if err != nil {
		return errors.Errorf("computing governance hash: %s", err)
	}

	callOpts := &bind.CallOpts{Context: context.Background()}
	if current, err := s.TeeExtensionGovernance.GetLatestTeeGovernanceHash(callOpts, extensionId); err == nil {
		if common.Hash(current) == desiredHash {
			logger.Infof("TEE governance already set for extension %s (hash %s), skipping", extensionId.String(), desiredHash.Hex())
			return nil
		}
	}

	opts, err := bind.NewKeyedTransactorWithChainID(privKey, s.ChainID)
	if err != nil {
		return errors.Errorf("%s", err)
	}

	tx, err := s.TeeExtensionGovernance.SetNewTeeGovernance(opts, extensionId, signers, threshold)
	if err != nil {
		return errors.Errorf("SetNewTeeGovernance failed: %s", err)
	}

	if _, err := support.CheckTx(tx, s.ChainClient); err != nil {
		return errors.Errorf("%s", err)
	}

	logger.Infof("TEE governance set for extension %s: %d signer(s), threshold %d (hash %s)",
		extensionId.String(), len(signers), threshold, desiredHash.Hex())
	return nil
}
