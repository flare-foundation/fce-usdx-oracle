// Package report encodes the signed price payload.
//
//	body = abi.encode(domain, price, decimals, observedAt, instructionId,
//	                  endpointsHash, adminsHash)
//	data = abi.encode(body, signature)
//
// FCC is fire-and-forget, so the payload arranges its own replay protection:
// domain separates this signature from anything else the identity key signs,
// observedAt is the store's monotonicity key, endpointsHash commits to the
// configuration the price was computed under, and adminsHash commits to the
// signer set that could have installed the credentials used to compute it.
// See docs/usdx-oracle-rationale.md.
//
// adminsHash was appended WITHOUT bumping DomainName, which the rule below
// otherwise requires. That is a deliberate development-phase exception: the
// extension and UsdxPriceStore are redeployed together, so no mixed pair is
// ever live. BodyLength is what enforces it — a peer on the six-field layout
// fails the length check on both sides rather than decoding a body that is
// missing its admin commitment. Bump the domain the moment the two can ship
// independently.
package report

import (
	"math/big"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is versioned: a change to the payload layout must change this
// string so old signatures cannot be reinterpreted under the new schema.
const DomainName = "USDX_ORACLE_PRICE_V1"

// BodyLength is the encoded body's length: seven static ABI words.
const BodyLength = 7 * 32

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

type PriceReport struct {
	Price         *big.Int
	Decimals      int8
	ObservedAt    uint64
	InstructionID common.Hash

	// keccak256 of the raw SET_ENDPOINTS payload as received, never
	// re-serialised: json.Marshal will not reproduce byte-identical input.
	EndpointsHash common.Hash

	// keccak256 of the raw SET_ADMINS payload as received, on the same terms.
	AdminsHash common.Hash
}

var bodyArgs = mustArgs(
	"bytes32", // domain
	"uint256", // price
	"int8",    // decimals
	"uint64",  // observedAt
	"bytes32", // instructionId
	"bytes32", // endpointsHash
	"bytes32", // adminsHash
)

var dataArgs = mustArgs("bytes", "bytes")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
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
	// Zero observedAt, zero endpointsHash and zero adminsHash are the store's
	// "never seen", "no configuration published" and "no admin set published"
	// sentinels; none could ever be accepted.
	if r.ObservedAt == 0 {
		return nil, failure.Internal
	}
	if r.EndpointsHash == (common.Hash{}) {
		return nil, failure.Internal
	}
	if r.AdminsHash == (common.Hash{}) {
		return nil, failure.Internal
	}

	body, err := bodyArgs.Pack(
		Domain(),
		r.Price,
		r.Decimals,
		r.ObservedAt,
		r.InstructionID,
		r.EndpointsHash,
		r.AdminsHash,
	)
	if err != nil {
		return nil, failure.Internal
	}
	return body, nil
}

// EncodeData wraps a signed body into the ActionResult payload. body is carried
// as raw bytes so the store verifies the signature over exactly what it
// received; re-encoding would surface an encoder divergence as a bad signature.
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

// DecodeBody reverses EncodeBody, verifying the domain separator so a payload
// signed for a different schema is rejected rather than silently misread.
func DecodeBody(body []byte) (PriceReport, error) {
	// Length first: Unpack would accept a longer buffer and read only the words
	// it expects, so a body from a peer on a later layout must be rejected here
	// rather than silently truncated. See the domain note in the package doc.
	if len(body) != BodyLength {
		return PriceReport{}, failure.Internal
	}

	values, err := bodyArgs.Unpack(body)
	if err != nil || len(values) != 7 {
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
	adminsHash, _ := values[6].([32]byte)

	if priceV == nil {
		return PriceReport{}, failure.Internal
	}

	return PriceReport{
		Price:         priceV,
		Decimals:      decimals,
		ObservedAt:    observedAt,
		InstructionID: common.Hash(instructionID),
		EndpointsHash: common.Hash(endpointsHash),
		AdminsHash:    common.Hash(adminsHash),
	}, nil
}
