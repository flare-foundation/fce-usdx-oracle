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

// processUsdxConfig routes USDX_CONFIG provisioning commands.
//
// Each command is valid over exactly one action type, and that is enforced here
// rather than assumed. The two channels carry different authorisation:
//
//   - SET_ENDPOINTS arrives as an on-chain instruction, authorised by the
//     owner-only check in the instruction sender. Accepting it as a direct
//     action would skip that check entirely — anyone able to reach the proxy
//     could repoint the data sources.
//   - SET_SECRET arrives as a direct action, authorised by M-of-N admin
//     signatures on the envelope. Accepting it on-chain would put a credential
//     ciphertext in permanent public storage for no benefit.
//
// Both return nil data and a fixed log on every path. Provisioning must not be
// usable as an oracle for probing the admin set, the freshness window, or which
// machine is which.
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
			// The detail names chain names, thresholds and secret refs — names,
			// never values, and all of it is public on-chain anyway — so it goes
			// to the log only.
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
			// Deliberately NOT logged with detail: the causes are exactly what an
			// attacker would want to distinguish.
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

// respond records a provisioning failure and returns a status-0 result with no
// data.
func respond(e *Extension, action teetypes.Action, df *instruction.DataFixed, f failure.Failure) (int, []byte) {
	ar := e.fail(action, df, f)
	b, _ := json.Marshal(ar)
	return http.StatusOK, b
}

// ok returns a status-1 result with no data. Provisioning never returns a
// payload — there is nothing a caller needs back, and anything returned would
// be a channel for leaking state.
func ok(action teetypes.Action, df *instruction.DataFixed) (int, []byte) {
	ar := buildResult(action, df, nil, 1, nil)
	b, _ := json.Marshal(ar)
	return http.StatusOK, b
}
