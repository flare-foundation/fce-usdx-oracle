package extension

import (
	"context"
	"encoding/json"
	"net/http"

	"fce-usdx-oracle/internal/failure"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// The three provisioning commands. Each is valid over exactly ONE action type,
// and each states its own guard rather than inheriting one: over direct,
// SET_ENDPOINTS and SET_ADMINS would skip the sender's onlyGovernance check;
// on-chain, SET_SECRET would make a ciphertext permanent and public.
//
// That per-command guard used to be the second of two fences. The first was the
// op type — USDX_CONFIG could not be confused with USDX_ORACLE — and it is gone
// now that every command shares TEE_ORACLE. What is left is below, so none of
// it may be relaxed into a shared default. Every path returns nil data and a
// fixed log.

// processSetEndpoints applies a governance-published endpoint configuration.
func (e *Extension) processSetEndpoints(
	action teetypes.Action, df *instruction.DataFixed,
) (int, []byte) {
	if action.Data.Type != teetypes.Instruction {
		return respond(e, action, df, failure.WrongActionType)
	}
	if err := e.provisioner.ApplyEndpoints(df.OriginalMessage); err != nil {
		// The detail names groups, thresholds and secret refs — names, never
		// values, and public on-chain anyway — so it goes to the log only.
		logger.Errorf("SET_ENDPOINTS rejected: %v", err)
		return respond(e, action, df, failure.ProvisionRejected)
	}
	logger.Infof("SET_ENDPOINTS applied")
	return ok(action, df)
}

// processSetAdmins installs the role-tagged admin signer sets.
func (e *Extension) processSetAdmins(
	action teetypes.Action, df *instruction.DataFixed,
) (int, []byte) {
	if action.Data.Type != teetypes.Instruction {
		return respond(e, action, df, failure.WrongActionType)
	}
	if err := e.provisioner.ApplyAdmins(df.OriginalMessage); err != nil {
		// The detail names versions, roles, thresholds and admin addresses — all
		// public on-chain in the very payload this rejects — so it goes to the
		// log only, on the same terms as SET_ENDPOINTS.
		logger.Errorf("SET_ADMINS rejected: %v", err)
		return respond(e, action, df, failure.ProvisionRejected)
	}
	logger.Infof("SET_ADMINS applied")
	return ok(action, df)
}

// processSetSecret installs one credential from a signed envelope.
func (e *Extension) processSetSecret(
	action teetypes.Action, df *instruction.DataFixed,
) (int, []byte) {
	if action.Data.Type != teetypes.Direct {
		return respond(e, action, df, failure.WrongActionType)
	}
	if err := e.provisioner.ApplySecret(context.Background(), df.OriginalMessage); err != nil {
		// No detail: the causes are exactly what an attacker wants to distinguish.
		logger.Errorf("SET_SECRET rejected")
		return respond(e, action, df, failure.ProvisionRejected)
	}
	logger.Infof("SET_SECRET applied")
	return ok(action, df)
}

func respond(e *Extension, action teetypes.Action, df *instruction.DataFixed, f failure.Failure) (int, []byte) {
	ar := e.fail(action, df, f)
	b, _ := json.Marshal(ar)
	return http.StatusOK, b
}

// ok returns a status-1 result with no data. Provisioning never returns a
// payload: anything returned would be a channel for leaking state.
func ok(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	ar := buildResult(action, df, nil, 1, nil)
	b, _ := json.Marshal(ar)
	return http.StatusOK, b
}
