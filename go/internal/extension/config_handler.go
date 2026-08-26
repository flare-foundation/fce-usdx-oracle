package extension

import (
	"context"

	"fce-usdx-oracle/internal/failure"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// The three provisioning commands. Each is valid over exactly ONE action type:
// over direct, SET_ENDPOINTS and SET_ADMINS would skip the sender's
// onlyGovernance check; on-chain, SET_SECRET would make a ciphertext permanent
// and public.
//
// That is enforced by which router reaches them — processInstruction for the
// first two, processDirect for the third — so it is not restated here. The op
// type used to be a second fence, and is gone now that every command shares
// TEE_ORACLE, which is why the routers may never grow a shared default arm.
// Every path returns nil data and a fixed log.

// processSetEndpoints applies a governance-published endpoint configuration.
func (e *Extension) processSetEndpoints(df *instruction.DataFixed) outcome {
	if err := e.provisioner.ApplyEndpoints(df.OriginalMessage); err != nil {
		// The detail names groups, thresholds and secret refs — names, never
		// values, and public on-chain anyway — so it goes to the log only.
		logger.Errorf("SET_ENDPOINTS rejected: %v", err)
		return refuse(failure.ProvisionRejected)
	}
	logger.Infof("SET_ENDPOINTS applied")
	return succeed(nil)
}

// processSetAdmins installs the role-tagged admin signer sets.
func (e *Extension) processSetAdmins(df *instruction.DataFixed) outcome {
	if err := e.provisioner.ApplyAdmins(df.OriginalMessage); err != nil {
		// The detail names versions, roles, thresholds and admin addresses — all
		// public on-chain in the very payload this rejects — so it goes to the
		// log only, on the same terms as SET_ENDPOINTS.
		logger.Errorf("SET_ADMINS rejected: %v", err)
		return refuse(failure.ProvisionRejected)
	}
	logger.Infof("SET_ADMINS applied")
	return succeed(nil)
}

// processSetSecret installs one credential from a signed envelope.
func (e *Extension) processSetSecret(di *teetypes.DirectInstruction) outcome {
	if err := e.provisioner.ApplySecret(context.Background(), di.Message); err != nil {
		// No detail: the causes are exactly what an attacker wants to distinguish.
		logger.Errorf("SET_SECRET rejected")
		return refuse(failure.ProvisionRejected)
	}
	logger.Infof("SET_SECRET applied")
	return succeed(nil)
}
