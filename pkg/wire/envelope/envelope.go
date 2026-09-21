// Package envelope encodes the signed provisioning envelope a SET_SECRET action
// carries.
//
//	body = abi.encode(domain, chainId, teeId, version, issuedAt, ref, keccak256(payload))
//	data = abi.encode(body, signatures, payload)
//
// The body is what a threshold of distinct admins signs, under EIP-191. Each
// field binds the envelope to something: chainId and teeId to one enclave, so a
// captured envelope cannot be redirected; version to a strictly increasing
// series per ref, so an old one cannot roll a credential back; issuedAt to a
// freshness window, the one replay bar that survives a restart; ref to the slot
// the secret fills, which decides the admin set that must sign; and the payload
// hash to the exact ciphertext, so a decoded envelope cannot disagree with what
// its admins signed. The domain is distinct from the price report's, so the two
// signatures cannot cross.
//
// The payload is the secret encrypted to the machine's public key, so an
// unsigned envelope is safe to hand between the people who sign it. Verification
// against the installed admin sets is the enclave's, in
// internal/provisioning/envelope; this package is the format both sides speak.
package envelope

import (
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is versioned on the same rule as the other signed payloads: a
// change to the body layout must change this string, so old signatures cannot
// be reinterpreted under a new schema.
const DomainName = "USDX_ORACLE_PROVISION_V1"

// Freshness bounds issuedAt: long enough to collect M signatures across
// timezones, short enough that a captured envelope expires quickly.
const Freshness = time.Hour

// ClockSkew is how far into the future an issuedAt may sit before it is
// refused as a broken clock rather than a fresh envelope.
const ClockSkew = 2 * time.Minute

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// MaxSignatures bounds recovery work before any cryptography runs; anyone who
// can deliver a direct action reaches the verifier. It is also the most a
// threshold can be, since a higher one installs cleanly and is then
// unsatisfiable forever.
const MaxSignatures = 16

// The two errors this package returns. The enclave maps both onto its own
// closed vocabulary at the boundary, and says nothing more to the caller.
var (
	// ErrInvalid is an envelope that cannot be encoded.
	ErrInvalid = errors.New("envelope: invalid envelope")
	// ErrMalformed is a message that does not decode as this layout, or whose
	// payload is not the one its body commits to.
	ErrMalformed = errors.New("envelope: not a provisioning envelope")
)

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Envelope is one provisioning message, decoded or about to be encoded.
type Envelope struct {
	ChainID  *big.Int
	TeeID    common.Address
	Version  uint64
	IssuedAt uint64
	Ref      string

	// The ciphertext the signatures commit to. The signed body carries
	// keccak256(Payload) rather than the payload itself, so the commitment is
	// derived here on encoding and checked against the body on decoding: a
	// decoded Envelope cannot disagree with what its admins signed.
	Payload []byte
}

var bodyArgs = mustArgs(
	argSpec{"Domain", "bytes32"},
	argSpec{"ChainID", "uint256"},
	argSpec{"TeeID", "address"},
	argSpec{"Version", "uint64"},
	argSpec{"IssuedAt", "uint64"},
	argSpec{"Ref", "string"},
	argSpec{"PayloadHash", "bytes32"},
)

var outerArgs = mustArgs(
	argSpec{"Body", "bytes"},
	argSpec{"Signatures", "bytes[]"},
	argSpec{"Payload", "bytes"},
)

// wireBody and wireOuter are the decoded forms of the two argument lists. Their
// field names are what Arguments.Copy matches the argument names against, so
// each layout is declared once rather than once per direction.
type wireBody struct {
	Domain      common.Hash
	ChainID     *big.Int
	TeeID       common.Address
	Version     uint64
	IssuedAt    uint64
	Ref         string
	PayloadHash common.Hash
}

type wireOuter struct {
	Body       []byte
	Signatures [][]byte
	Payload    []byte
}

// argSpec is one ABI argument's name and type. Names do not affect the encoding;
// they are what Arguments.Copy maps onto struct fields, and an unnamed argument
// cannot be copied into a struct at all.
type argSpec struct{ name, typ string }

func mustArgs(specs ...argSpec) abi.Arguments {
	args := make(abi.Arguments, len(specs))
	for i, spec := range specs {
		ty, err := abi.NewType(spec.typ, "", nil)
		if err != nil {
			panic("envelope: invalid ABI type " + spec.typ + ": " + err.Error())
		}
		args[i] = abi.Argument{Name: spec.name, Type: ty}
	}
	return args
}

// EncodeBody returns the exact bytes admins sign. The signer and the verifier
// both call this, so the two cannot disagree by a byte.
func EncodeBody(e Envelope) ([]byte, error) {
	if e.ChainID == nil {
		return nil, fmt.Errorf("%w: chain id is required", ErrInvalid)
	}
	if len(e.Payload) == 0 {
		return nil, fmt.Errorf("%w: payload is required", ErrInvalid)
	}
	return bodyArgs.Pack(
		Domain(), e.ChainID, e.TeeID, e.Version, e.IssuedAt, e.Ref, crypto.Keccak256Hash(e.Payload),
	)
}

// Encode builds the wire message: abi.encode(body, signatures, payload).
func Encode(e Envelope, signatures [][]byte) ([]byte, error) {
	body, err := EncodeBody(e)
	if err != nil {
		return nil, err
	}
	return outerArgs.Pack(body, signatures, e.Payload)
}

// Decode reverses Encode. It checks the shape, the domain and that the payload
// delivered alongside is the one the body commits to; whether the signatures
// are enough, and from whom, is the verifier's question.
func Decode(message []byte) (Envelope, [][]byte, error) {
	values, err := outerArgs.Unpack(message)
	if err != nil || len(values) != 3 {
		return Envelope{}, nil, fmt.Errorf("%w: bad outer encoding", ErrMalformed)
	}

	// Copy matches on the argument names, so a field added or reordered above is
	// an error here rather than a silently zeroed value.
	var outer wireOuter
	if err := outerArgs.Copy(&outer, values); err != nil {
		return Envelope{}, nil, fmt.Errorf("%w: bad outer encoding", ErrMalformed)
	}

	values, err = bodyArgs.Unpack(outer.Body)
	if err != nil || len(values) != 7 {
		return Envelope{}, nil, fmt.Errorf("%w: bad body encoding", ErrMalformed)
	}

	var body wireBody
	if err := bodyArgs.Copy(&body, values); err != nil {
		return Envelope{}, nil, fmt.Errorf("%w: bad body encoding", ErrMalformed)
	}

	if body.Domain != Domain() {
		return Envelope{}, nil, fmt.Errorf("%w: wrong domain", ErrMalformed)
	}

	// The signatures cover the hash, not the payload, so the payload delivered
	// alongside must be the one the body commits to.
	if len(outer.Payload) == 0 || crypto.Keccak256Hash(outer.Payload) != body.PayloadHash {
		return Envelope{}, nil, fmt.Errorf("%w: payload does not match the signed hash", ErrMalformed)
	}

	return Envelope{
		ChainID:  body.ChainID,
		TeeID:    body.TeeID,
		Version:  body.Version,
		IssuedAt: body.IssuedAt,
		Ref:      strings.TrimSpace(body.Ref),
		Payload:  outer.Payload,
	}, outer.Signatures, nil
}
