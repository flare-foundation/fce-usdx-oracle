package extension

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	teeutils "github.com/flare-foundation/tee-node/pkg/utils"
)

// processUsdxConfig routes USDX_CONFIG provisioning commands. Each is valid
// over exactly one action type, enforced here rather than assumed: SET_ENDPOINTS
// is authorised by the sender's owner-only check, so a direct action would skip
// it; SET_SECRET is authorised by M-of-N admin signatures, and on-chain would
// put a ciphertext in permanent public storage for no benefit.
//
// Every path returns nil data and a fixed log, so provisioning cannot be used
// to probe the admin set or which machine is which.
func (e *Extension) processUsdxConfig(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	if e.provisioner == nil {
		return respond(e, action, df, failure.NotConfigured)
	}

	switch {
	case df.OPCommand == teeutils.ToHash(config.OPCommandSetEndpoints):
		if action.Data.Type != teetypes.Instruction {
			return respond(e, action, df, failure.WrongActionType)
		}
		if err := e.provisioner.ApplyEndpoints(df.OriginalMessage); err != nil {
			// The detail names chains, thresholds and secret refs — names, never
			// values, and public on-chain anyway — so it goes to the log only.
			logger.Errorf("SET_ENDPOINTS rejected: %v", err)
			return respond(e, action, df, failure.ProvisionRejected)
		}
		logger.Infof("SET_ENDPOINTS applied")
		return ok(action, df)

	case df.OPCommand == teeutils.ToHash(config.OPCommandSetSecret):
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

	default:
		return http.StatusNotImplemented, []byte(fmt.Sprintf(
			"unsupported op command: received %s, expected one of [%s (%s), %s (%s)]",
			df.OPCommand.Hex(),
			teeutils.ToHash(config.OPCommandSetEndpoints).Hex(), config.OPCommandSetEndpoints,
			teeutils.ToHash(config.OPCommandSetSecret).Hex(), config.OPCommandSetSecret,
		))
	}
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
