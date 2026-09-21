package extension

import (
	"fmt"
	"net/http"

	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/pkg/protocol"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/tee/instruction"
	"github.com/flare-foundation/tee-node/pkg/processorutils"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// processAction parses whichever envelope the action carries and hands it to
// that channel's router.
//
// The envelope is chosen by Data.Type, never guessed: parsing one as the other
// does not fail — encoding/json drops unknown fields — it yields an EMPTY
// payload. An unrecognised type is refused rather than treated as an
// instruction, because the instruction router is the privileged one.
func (e *Extension) processAction(action teetypes.Action) (int, []byte) {
	switch action.Data.Type {
	case teetypes.Instruction:
		df, err := processorutils.Parse[instruction.DataFixed](action.Data.Message)
		if err != nil {
			return http.StatusBadRequest, fmt.Appendf(nil, "decoding fixed data: %v", err)
		}
		if code, body, bad := checkOPType(df.OPType); bad {
			return code, body
		}
		// The action id and the id inside the cosigned payload name the same
		// instruction by construction. A disagreement means the envelope and its
		// contents describe different instructions, which nothing legitimate
		// produces, so it is refused rather than reconciled.
		if action.Data.ID != df.InstructionID {
			return http.StatusBadRequest, []byte("action id does not match the instruction id")
		}
		return e.processInstruction(action, df)

	case teetypes.Direct:
		di, err := processorutils.Parse[teetypes.DirectInstruction](action.Data.Message)
		if err != nil {
			return http.StatusBadRequest, fmt.Appendf(nil, "decoding direct instruction: %v", err)
		}
		if code, body, bad := checkOPType(di.OPType); bad {
			return code, body
		}
		return e.processDirect(action, di)

	default:
		return http.StatusBadRequest, fmt.Appendf(nil,
			"unknown action type %q", action.Data.Type)
	}
}

// checkOPType reports whether the action was raised for this extension,
// returning the 501 response to send when it was not.
//
// The body reaches ActionResult.Log, which is world-readable: tee-node converts
// a non-200 into an error and processorutils.Invalid places it there. Echoing
// the received opType is nevertheless safe, since it is the value the caller
// supplied and the rest are constants compiled into this build. Handlers that
// process a payload disclose nothing about it, because there the input is
// attacker-chosen and the response would describe this machine's state.
func checkOPType(got common.Hash) (int, []byte, bool) {
	if got == protocol.OPTypeTeeOracle {
		return 0, nil, false
	}
	return http.StatusNotImplemented, fmt.Appendf(nil,
		"unsupported op type: received %s, expected %s (%s)",
		got.Hex(), protocol.OPTypeTeeOracle.Hex(), protocol.OpName(protocol.OPTypeTeeOracle),
	), true
}

// processInstruction routes the commands raised on-chain through
// TeeOracleInstructionsSender. Arrival on this channel is itself the
// authorisation: the sender's governance check has run and the data providers
// have cosigned the instruction. These commands must remain unreachable from
// processDirect.
func (e *Extension) processInstruction(
	action teetypes.Action, df *instruction.DataFixed,
) (int, []byte) {
	switch df.OPCommand {
	case protocol.OPCommandGetFeed:
		// Requested through requestFeedUpdate(), which pays the registry fee and
		// has the cosigning quorum behind it.
		return e.result(action, df.OPCommand, e.processGetFeed(df))

	case protocol.OPCommandSetEndpoints:
		if e.provisioner == nil {
			return e.result(action, df.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, df.OPCommand, e.processSetEndpoints(df))

	case protocol.OPCommandSetAdmins:
		if e.provisioner == nil {
			return e.result(action, df.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, df.OPCommand, e.processSetAdmins(df))

	case protocol.OPCommandGetReadiness, protocol.OPCommandSetSecret:
		// Direct-only. Named explicitly so the refusal reports the wrong channel
		// rather than an unknown command.
		return e.result(action, df.OPCommand, refuse(failure.WrongActionType))

	default:
		return unsupportedCommand(df.OPCommand)
	}
}

// processDirect routes the commands raised on the proxy. Nothing here is
// authorised by having arrived: SET_SECRET carries its own M-of-N admin
// signatures, and GET_READINESS grants nothing.
func (e *Extension) processDirect(
	action teetypes.Action, di *teetypes.DirectInstruction,
) (int, []byte) {
	switch di.OPCommand {
	case protocol.OPCommandGetReadiness:
		// A price is worth making permissionless; an operational status report is
		// not, and it is stale within minutes.
		return e.result(action, di.OPCommand, e.processGetReadiness(di, action.Data.ID))

	case protocol.OPCommandSetSecret:
		if e.provisioner == nil {
			return e.result(action, di.OPCommand, refuse(failure.NotConfigured))
		}
		return e.result(action, di.OPCommand, e.processSetSecret(di))

	case protocol.OPCommandGetFeed, protocol.OPCommandSetEndpoints, protocol.OPCommandSetAdmins:
		// Instruction-only. Over this channel GET_FEED would hand the same signed
		// update to anyone holding the proxy API key, and the two SET_ commands
		// would skip the sender's onlyGovernance check entirely.
		return e.result(action, di.OPCommand, refuse(failure.WrongActionType))

	default:
		return unsupportedCommand(di.OPCommand)
	}
}

// unsupportedCommand returns the 501 response for an unrecognised command,
// naming every command this build serves. Public on the same terms as
// checkOPType, and listed because the reader is diagnosing a mismatch.
func unsupportedCommand(cmd common.Hash) (int, []byte) {
	return http.StatusNotImplemented, fmt.Appendf(nil,
		"unsupported op command: received %s, expected one of [%s (%s), %s (%s), %s (%s), %s (%s), %s (%s)]",
		cmd.Hex(),
		protocol.OPCommandGetFeed.Hex(), protocol.OpName(protocol.OPCommandGetFeed),
		protocol.OPCommandGetReadiness.Hex(), protocol.OpName(protocol.OPCommandGetReadiness),
		protocol.OPCommandSetEndpoints.Hex(), protocol.OpName(protocol.OPCommandSetEndpoints),
		protocol.OPCommandSetAdmins.Hex(), protocol.OpName(protocol.OPCommandSetAdmins),
		protocol.OPCommandSetSecret.Hex(), protocol.OpName(protocol.OPCommandSetSecret),
	)
}
