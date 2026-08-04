// Package report encodes the signed price payload.
//
// # What is signed, and why each field is in it
//
//	struct PriceReport {
//	    bytes32 domain;         // keccak256("USDX_ORACLE_PRICE_V1")
//	    uint256 price;
//	    int8    decimals;
//	    uint64  observedAt;
//	    bytes32 instructionId;
//	    bytes32 endpointsHash;  // keccak256 of the active SET_ENDPOINTS payload
//	}
//
//	body = abi.encode(report)
//	data = abi.encode(body, signature)
//
// FCC is explicitly "fire and forget": no ordering guarantee, no delivery
// guarantee, no replay protection beyond what a payload arranges for itself.
//
//   - **domain** separates this signature from any other thing the TEE identity
//     key ever signs. Without it, a signature produced for some future USDX
//     instruction could be replayed as a price.
//   - **observedAt** is the store's monotonicity key, so it MUST be inside the
//     signed body. If it were outside, a relayer could rewrite it and either
//     replay an old price as fresh or push a real price out of order.
//   - **instructionId** ties the report to the instruction that requested it.
//   - **endpointsHash** commits to the configuration this price was computed
//     under. UsdxPriceStore compares it against what setEndpoints published for
//     this machine, so a machine running a stale or divergent configuration
//     cannot submit at all. Without it the on-chain config is advisory: it
//     records what was *sent*, never what a machine is actually *running*, and
//     with several machines a missed setEndpoints call leaves one publishing
//     perfectly valid prices from the wrong configuration.
//
// # No target binding, by decision
//
// The payload carries no chain id, store address or feed id. WHERE a report is
// accepted is settled by the store's trust anchor instead: a signature is only
// ever accepted from a TEE machine currently registered to the store's
// extension (UsdxPriceStore._requireRegisteredTee). The payload binds WHAT was
// observed; the registry check plus governance bind where it lands. The
// trade — a genuinely-signed report can be replayed into another deployment
// registered under the same extension, though nobody can forge a price — was
// made deliberately before launch; see the findings doc.
//
// # Carry the body, never re-encode it
//
// data carries `body` as raw bytes rather than the struct's fields. The store
// verifies the signature over exactly the bytes it received, then decodes them.
// Re-encoding on the way in would make verification depend on the encoder
// agreeing byte-for-byte with the TEE's, and any future divergence — a field
// reordering, a padding difference — would present as an invalid signature
// rather than as the encoding bug it is.
//
// # Price only
//
// Supply and backing components are excluded: they may be commercially
// sensitive to Hex (spec §4.1), and this payload is public and permanent. The
// consequence, recorded in the spec, is that consumers cannot recompute
// min(1, B/S) from on-chain data.
package report

import (
	"math/big"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is the domain separator's preimage. Versioned: a change to the
// payload layout must change this string, so that old signatures cannot be
// reinterpreted under the new schema.
const DomainName = "USDX_ORACLE_PRICE_V1"

// BodyLength is the encoded body's length: six static ABI words.
const BodyLength = 6 * 32

// Domain returns keccak256(DomainName).
func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// PriceReport is the signed observation.
type PriceReport struct {
	Price         *big.Int
	Decimals      int8
	ObservedAt    uint64
	InstructionID common.Hash

	// EndpointsHash is keccak256 of the raw SET_ENDPOINTS payload currently in
	// force. Hashed as received, never re-serialised: json.Marshal will not
	// reproduce byte-identical input, so a re-encode could never match
	// keccak256(_config) from the transaction.
	EndpointsHash common.Hash
}

// bodyArgs is the ABI layout of the struct's fields, in declaration order.
//
// Every field is a static type, so the encoding is five 32-byte words with no
// offsets — which is what makes the golden-vector test in this package able to
// assert the layout word by word.
var bodyArgs = mustArgs(
	"bytes32", // domain
	"uint256", // price
	"int8",    // decimals
	"uint64",  // observedAt
	"bytes32", // instructionId
	"bytes32", // endpointsHash
)

// dataArgs is the outer envelope: (bytes body, bytes signature).
var dataArgs = mustArgs("bytes", "bytes")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			// Only reachable via a typo in the literals above, which the
			// package's tests would catch immediately.
			panic("report: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// EncodeBody returns abi.encode(report) — the exact bytes to be signed.
func EncodeBody(r PriceReport) ([]byte, error) {
	if r.Price == nil || r.Price.Sign() < 0 {
		return nil, failure.Internal
	}
	if r.Decimals < 0 {
		return nil, failure.Internal
	}
	if r.ObservedAt == 0 {
		// Zero is the store's "never seen" sentinel; a report carrying it could
		// never satisfy strict monotonicity.
		return nil, failure.Internal
	}

	if r.EndpointsHash == (common.Hash{}) {
		// Zero is the store's "no configuration published" sentinel, so a report
		// carrying it could never be accepted.
		return nil, failure.Internal
	}

	body, err := bodyArgs.Pack(
		Domain(),
		r.Price,
		r.Decimals,
		r.ObservedAt,
		r.InstructionID,
		r.EndpointsHash,
	)
	if err != nil {
		return nil, failure.Internal
	}
	return body, nil
}

// EncodeData wraps a signed body into the ActionResult payload:
// abi.encode(body, signature).
func EncodeData(body, signature []byte) ([]byte, error) {
	if len(body) != BodyLength || len(signature) == 0 {
		return nil, failure.Internal
	}
	data, err := dataArgs.Pack(body, signature)
	if err != nil {
		return nil, failure.Internal
	}
	return data, nil
}

// DecodeData splits an ActionResult payload back into body and signature.
// Used by tests and tooling; the store does the equivalent on-chain.
func DecodeData(data []byte) (body, signature []byte, err error) {
	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 2 {
		return nil, nil, failure.Internal
	}
	body, ok := values[0].([]byte)
	if !ok {
		return nil, nil, failure.Internal
	}
	signature, ok = values[1].([]byte)
	if !ok {
		return nil, nil, failure.Internal
	}
	return body, signature, nil
}

// DecodeBody reverses EncodeBody. It verifies the domain separator, so a
// payload signed for a different schema is rejected here rather than silently
// misread.
func DecodeBody(body []byte) (PriceReport, error) {
	values, err := bodyArgs.Unpack(body)
	if err != nil || len(values) != 6 {
		return PriceReport{}, failure.Internal
	}

	domain, ok := values[0].([32]byte)
	if !ok || common.Hash(domain) != Domain() {
		return PriceReport{}, failure.Internal
	}

	priceV, _ := values[1].(*big.Int)
	decimals, _ := values[2].(int8)
	observedAt, _ := values[3].(uint64)
	instructionID, _ := values[4].([32]byte)
	endpointsHash, _ := values[5].([32]byte)

	if priceV == nil {
		return PriceReport{}, failure.Internal
	}

	return PriceReport{
		Price:         priceV,
		Decimals:      decimals,
		ObservedAt:    observedAt,
		InstructionID: common.Hash(instructionID),
		EndpointsHash: common.Hash(endpointsHash),
	}, nil
}
