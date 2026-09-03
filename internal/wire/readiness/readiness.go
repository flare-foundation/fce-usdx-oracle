// Package readiness encodes the signed readiness payload.
//
//	body = abi.encode(domain, instructionId, timestamp, payloadHash)
//	data = abi.encode(body, signature, payload)
//
// The payload is JSON (pkg/types.ReadinessReport), included alongside the body
// for flexibility, not within it—it's variable-length and for operators, not contracts.
// The body includes a hash of the payload, so the signature covers the exact bytes
// received; consumers verify by comparing hashes, not re-marshalling.
//
// # Why sign this?
//
// Unsigned state is already available via GET /state. Signing is what distinguishes
// "what this machine says" from "what the enclave says," the same standard as price
// data. Otherwise, exposing the extension's port would suffice.
//
// # Why a separate domain?
//
// Different domains prevent readiness answers from being misinterpreted as prices,
// or vice versa, even if routed incorrectly. The same key signs both, so domain
// separation must be at the payload level.
package readiness

import (
	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is versioned on the same rule as the price report: a change to the
// body layout must change this string, so old signatures cannot be
// reinterpreted under a new schema.
const DomainName = "USDX_ORACLE_READINESS_V1"

// BodyLength is the encoded body's length: four static ABI words.
const BodyLength = 4 * 32

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Report is the signed header. It carries no teeId: the signer is recovered from
// the signature, so naming the machine would add a field that is only ever
// redundant. The price report omits it for the same reason.
type Report struct {
	// InstructionID is the action being answered. With Timestamp it is what
	// stops a captured answer being replayed later as a current one — the field
	// an operator trusts least is the one that ages.
	InstructionID common.Hash

	// Timestamp is when the enclave answered, not when it last observed. The
	// observation's own age is inside the payload.
	Timestamp uint64

	// PayloadHash is keccak256 of the JSON payload as emitted.
	PayloadHash common.Hash
}

var bodyArgs = mustArgs(
	"bytes32", // domain
	"bytes32", // instructionId
	"uint64",  // timestamp
	"bytes32", // payloadHash
)

var dataArgs = mustArgs(
	"bytes", // body
	"bytes", // signature
	"bytes", // payload
)

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("readiness: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// EncodeBody returns abi.encode(report) — the exact bytes to be signed.
func EncodeBody(r Report) ([]byte, error) {
	// A zero timestamp or payload hash would make the freshness and payload
	// bindings vacuous, which is worse than refusing to answer.
	if r.Timestamp == 0 {
		return nil, failure.Internal
	}
	if r.PayloadHash == (common.Hash{}) {
		return nil, failure.Internal
	}

	body, err := bodyArgs.Pack(Domain(), r.InstructionID, r.Timestamp, r.PayloadHash)
	if err != nil {
		return nil, failure.Internal
	}
	return body, nil
}

// EncodeData wraps a signed body and its payload into the ActionResult payload.
// body and payload are both carried as raw bytes so a consumer verifies over
// exactly what it received.
func EncodeData(body, signature, payload []byte) ([]byte, error) {
	if len(body) != BodyLength || len(signature) == 0 || len(payload) == 0 {
		return nil, failure.Internal
	}
	data, err := dataArgs.Pack(body, signature, payload)
	if err != nil {
		return nil, failure.Internal
	}
	return data, nil
}

// DecodeData splits an ActionResult payload into body, signature and payload. It
// deliberately does not unmarshal, so the caller hashes the same bytes the
// signature covers.
func DecodeData(data []byte) (body, signature, payload []byte, err error) {
	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 3 {
		return nil, nil, nil, failure.Internal
	}

	body, ok := values[0].([]byte)
	if !ok || len(body) != BodyLength {
		return nil, nil, nil, failure.Internal
	}
	signature, ok = values[1].([]byte)
	if !ok || len(signature) == 0 {
		return nil, nil, nil, failure.Internal
	}
	payload, ok = values[2].([]byte)
	if !ok || len(payload) == 0 {
		return nil, nil, nil, failure.Internal
	}
	return body, signature, payload, nil
}
