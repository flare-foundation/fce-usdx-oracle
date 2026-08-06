// Package pricereport decodes the USDX oracle's signed price payload.
//
// The wire format, as returned in ActionResult.Data:
//
//	body = abi.encode(bytes32 domain, uint256 price, int8 decimals,
//	                  uint64 observedAt, bytes32 instructionId, bytes32 configHash)
//	data = abi.encode(bytes body, bytes signature)
//
// UsdxPriceStore.submitPrice takes (body, signature) — the two halves of data —
// so decoding this is exactly what a relayer does, and the store does the
// equivalent on-chain.
//
// # Why this lives in tools/ and not in the extension
//
// The layout is declared here rather than imported from go/internal/report so
// that tools/ stays independent of any one implementation: this asserts on the
// *wire contract*, and must run unchanged against the Go, Python and TypeScript
// extensions alike (docs/extension-contract.md). It is a second, deliberate
// declaration of the same layout — which is also why it is one package and not
// a copy in every command that needs it.
//
// # Never re-encode the body
//
// Body is carried verbatim. The store verifies the signature over exactly the
// bytes it received and only then decodes them, so re-encoding from the fields
// below would make acceptance depend on the relayer's encoder matching the
// TEE's byte for byte. Any divergence would then present as an invalid
// signature rather than as the encoding bug it is.
package pricereport

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// DomainName is the schema version the signature is bound to. A change to the
// payload layout must change this string, so that an old signature cannot be
// reinterpreted under a new schema.
const DomainName = "USDX_ORACLE_PRICE_V1"

// BodyLength is the encoded body: six static ABI words.
const BodyLength = 6 * 32

// Domain returns keccak256(DomainName) — the value a valid report carries in
// its first word, and the one UsdxPriceStore.DOMAIN holds.
func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Report is one decoded price report.
type Report struct {
	Domain        common.Hash
	Price         *big.Int
	Decimals      int8
	ObservedAt    uint64
	InstructionID common.Hash
	ConfigHash    common.Hash

	// Body is the signed bytes, exactly as received. Pass this to submitPrice.
	Body []byte

	// Signature is 65 bytes with v in {27, 28}.
	Signature []byte
}

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			// Only reachable via a typo in the literals below, which this
			// package's tests catch immediately.
			panic("pricereport: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

var (
	dataArgs = mustArgs("bytes", "bytes")
	bodyArgs = mustArgs("bytes32", "uint256", "int8", "uint64", "bytes32", "bytes32")
)

// Decode splits ActionResult.Data and decodes the signed body.
//
// It verifies the domain separator, so a payload built for another schema is
// rejected here rather than silently misread as a price.
func Decode(data []byte) (Report, error) {
	var r Report

	outer, err := dataArgs.Unpack(data)
	if err != nil || len(outer) != 2 {
		return r, errors.Errorf("data is not abi.encode(bytes,bytes): %v", err)
	}
	body, ok := outer[0].([]byte)
	if !ok {
		return r, errors.New("data[0] is not bytes")
	}
	signature, ok := outer[1].([]byte)
	if !ok {
		return r, errors.New("data[1] is not bytes")
	}
	if len(body) != BodyLength {
		return r, errors.Errorf("body is %d bytes, expected %d", len(body), BodyLength)
	}

	fields, err := bodyArgs.Unpack(body)
	if err != nil || len(fields) != 6 {
		return r, errors.Errorf("body is not a PriceReport: %v", err)
	}

	domain, _ := fields[0].([32]byte)
	price, _ := fields[1].(*big.Int)
	decimals, _ := fields[2].(int8)
	observedAt, _ := fields[3].(uint64)
	instructionID, _ := fields[4].([32]byte)
	configHash, _ := fields[5].([32]byte)

	if price == nil {
		return r, errors.New("price did not decode as uint256")
	}
	if common.Hash(domain) != Domain() {
		return r, errors.Errorf("wrong domain separator: got %s, want %s",
			common.Hash(domain).Hex(), Domain().Hex())
	}

	return Report{
		Domain:        domain,
		Price:         price,
		Decimals:      decimals,
		ObservedAt:    observedAt,
		InstructionID: instructionID,
		ConfigHash:    configHash,
		Body:          body,
		Signature:     signature,
	}, nil
}

// Signer recovers the TEE identity that signed the report, exactly as
// UsdxPriceStore._recover does: EIP-191 over keccak256(body), with v in
// {27, 28}. Recovering it off-chain is what lets a relayer say *which* machine
// a rejected report came from.
func (r Report) Signer() (common.Address, error) {
	if len(r.Signature) != 65 {
		return common.Address{}, errors.Errorf(
			"BadSignatureLength: %d bytes, the store requires 65", len(r.Signature))
	}

	// go-ethereum recovers with v in {0,1}; the store requires the {27,28} form
	// that the extension normalises to. Copied rather than mutated in place:
	// Signature is relayed verbatim and must not be disturbed.
	sig := make([]byte, 65)
	copy(sig, r.Signature)
	switch sig[64] {
	case 27, 28:
		sig[64] -= 27
	default:
		return common.Address{}, errors.Errorf(
			"BadRecoveryId: v is %d, the store requires 27 or 28", r.Signature[64])
	}

	pub, err := crypto.SigToPub(EIP191Digest(r.Body), sig)
	if err != nil {
		return common.Address{}, errors.Errorf("UnknownSigner: signature does not recover: %s", err)
	}
	return crypto.PubkeyToAddress(*pub), nil
}

// EIP191Digest is what the signature covers:
// keccak256("\x19Ethereum Signed Message:\n32" || keccak256(body)).
func EIP191Digest(body []byte) []byte {
	inner := crypto.Keccak256(body)
	return crypto.Keccak256(
		[]byte(fmt.Sprintf("\x19Ethereum Signed Message:\n%d", len(inner))),
		inner,
	)
}

// Human renders the fixed-point price for display only. The uint256 is the
// value; this is never fed back into anything.
func (r Report) Human() string {
	if r.Price == nil || r.Decimals < 0 || r.Decimals > 36 {
		return "?"
	}
	scale := new(big.Int).Exp(big.NewInt(10), big.NewInt(int64(r.Decimals)), nil)
	whole, frac := new(big.Int), new(big.Int)
	whole.QuoRem(r.Price, scale, frac)

	digits := frac.String()
	for len(digits) < int(r.Decimals) {
		digits = "0" + digits
	}
	return whole.String() + "." + digits
}
