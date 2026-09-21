// Package configpayload encodes and decodes the two configuration payloads
// TeeOracleInstructionsSender dispatches, and the commitment a PRIVATE endpoint
// publishes in place of its URL.
//
//	SET_ENDPOINTS message = abi.encode(Endpoints{version, feedId, groups})
//	SET_ADMINS    message = abi.encode(Admins{version, feedId, roles})
//	urlHash               = keccak256(abi.encode(salt, url))
//
// The struct layouts are the abigen bindings for upstream's TeeOracleStructs
// ABI-only re-export (pkg/contracts/teeoraclestructs), so they come from the
// same compiled ABI the sender uses. The wrappers are assembled inside the
// contract and never appear as call arguments, so that re-export is the only
// ABI they reach at all: without it a publisher could not know, in advance, the
// hash every subsequent feed update has to carry.
//
// Both payloads name a FEED. One sender serves every feed of its extension and
// keeps commitments per feed and machine, so a configuration is only meaningful
// alongside the feed it was published for.
//
// Both payloads are DYNAMIC, so their encoding is not canonical. The publisher
// encodes to predict the hash the sender will record; the enclave never
// re-encodes, it hashes the message exactly as dispatched. Both directions are
// pinned to Solidity's own bytes in deploy/testdata/wire, which is what catches
// an encoder that differs from the contract's by so much as a padding word: the
// failure would otherwise be silent and total, every feed update reverting
// StaleEndpoints against a hash no machine can satisfy.
package configpayload

import (
	"errors"
	"fmt"
	"math"

	"fce-usdx-oracle/pkg/contracts/teeoraclestructs"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// EndpointKind mirrors the on-chain enum.
const (
	KindPublic  = uint8(0)
	KindPrivate = uint8(1)
)

// ErrMalformed is the one error the decoders return: a caller inside the
// enclave maps it onto its own vocabulary, and a caller outside it learns
// nothing about which byte was wrong, which is the point.
var ErrMalformed = errors.New("configpayload: not a configuration payload")

// The wire types, aliased so callers never spell out the generated names. abigen
// emits its own copy of every nested struct into each package that mentions
// one, so the identical EndpointGroup exists in the sender binding too as a
// distinct named type; taking all of them from one package keeps a payload
// buildable without casts.
type (
	Endpoint  = teeoraclestructs.ITeeOracleInstructionsSenderEndpoint
	Group     = teeoraclestructs.ITeeOracleInstructionsSenderEndpointGroup
	Endpoints = teeoraclestructs.ITeeOracleInstructionsSenderEndpoints
	Role      = teeoraclestructs.ITeeOracleInstructionsSenderAdminRole
	Admins    = teeoraclestructs.ITeeOracleInstructionsSenderAdmins
)

// endpointsArgs and adminsArgs are the top-level tuple argument of the generated
// ABI's endpointsStruct and adminsStruct methods: the same abi.Arguments abigen
// derived, so Pack and Unpack here cannot disagree with the binding's types.
var (
	structsABI    = mustStructsABI()
	endpointsArgs = structMethodInputs("endpointsStruct")
	adminsArgs    = structMethodInputs("adminsStruct")
)

func mustStructsABI() *abi.ABI {
	parsed, err := teeoraclestructs.TeeOracleStructsMetaData.GetAbi()
	if err != nil {
		panic("configpayload: parsing TeeOracleStructs ABI: " + err.Error())
	}
	return parsed
}

func structMethodInputs(method string) abi.Arguments {
	m, ok := structsABI.Methods[method]
	if !ok {
		panic("configpayload: TeeOracleStructs ABI has no method " + method)
	}
	return m.Inputs
}

// PrivateURL is a PRIVATE endpoint's commitment preimage, in both directions.
//
// Declared here rather than taken from the bindings: the chain holds only
// keccak256 of these bytes, as Endpoint.urlHash, so there is no on-chain struct
// to derive the layout from. The format is a convention between the publisher
// and the enclave, pinned by TestCommitPrivateURLMatchesSolidity.
type PrivateURL struct {
	Salt common.Hash
	Url  string
}

// argSpec is one ABI argument's name and type. The name is load-bearing:
// Arguments.Copy maps argument names onto struct fields, and an unnamed
// argument cannot be copied into a struct at all.
type argSpec struct{ name, typ string }

// privateURLArgs is abi.encode(salt, url). Salted by construction: a bare hash
// of a low-entropy URL is brute-forceable from chain data, and the URL is on
// chain only as this commitment.
var privateURLArgs = mustArgs(
	argSpec{"salt", "bytes32"},
	argSpec{"url", "string"},
)

func mustArgs(specs ...argSpec) abi.Arguments {
	args := make(abi.Arguments, len(specs))
	for i, spec := range specs {
		ty, err := abi.NewType(spec.typ, "", nil)
		if err != nil {
			panic("configpayload: invalid ABI type " + spec.typ + ": " + err.Error())
		}
		args[i] = abi.Argument{Name: spec.name, Type: ty}
	}
	return args
}

// EncodePrivateURL returns the blob a PRIVATE endpoint's URL is provisioned as.
// Not calldata: it travels as a SET_SECRET envelope payload, and the enclave
// recomputes the commitment from it.
func EncodePrivateURL(u PrivateURL) ([]byte, error) {
	if u.Salt == (common.Hash{}) {
		return nil, errors.New("configpayload: commitment salt is zero")
	}
	return privateURLArgs.Pack(u.Salt, u.Url)
}

// CommitPrivateURL returns the urlHash a published PRIVATE endpoint must carry.
// The salt must be the one provisioned alongside the URL: the enclave recomputes
// this and refuses the endpoint if it disagrees.
func CommitPrivateURL(u PrivateURL) (common.Hash, error) {
	preimage, err := EncodePrivateURL(u)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(preimage), nil
}

// DecodePrivateURL reverses EncodePrivateURL. It judges the shape and nothing
// else; whether the commitment matches is the caller's check.
func DecodePrivateURL(preimage []byte) (PrivateURL, bool) {
	values, err := privateURLArgs.Unpack(preimage)
	if err != nil || len(values) != 2 {
		return PrivateURL{}, false
	}
	var out PrivateURL
	if err := privateURLArgs.Copy(&out, values); err != nil {
		return PrivateURL{}, false
	}
	return out, true
}

// DecodeEndpoints reads a SET_ENDPOINTS message.
//
// Unpack returns the tuple as an anonymous struct that go-ethereum builds from
// the ABI components. Copying it into a named struct goes through a one-field
// WRAPPER because Arguments.Copy treats a struct destination as "unpack the
// single return value into this struct's first field": handed the payload
// struct directly it would try to write the whole tuple into Version.
// abi.ConvertType would also work and panics on a mismatch, which is not a
// thing to do to a provisioning payload inside an enclave.
func DecodeEndpoints(payload []byte) (Endpoints, error) {
	values, err := endpointsArgs.Unpack(payload)
	if err != nil || len(values) != 1 {
		return Endpoints{}, ErrMalformed
	}
	var out struct{ Value Endpoints }
	if err := endpointsArgs.Copy(&out, values); err != nil {
		return Endpoints{}, ErrMalformed
	}
	return out.Value, nil
}

// DecodeAdmins reads a SET_ADMINS message, on the same terms.
func DecodeAdmins(payload []byte) (Admins, error) {
	values, err := adminsArgs.Unpack(payload)
	if err != nil || len(values) != 1 {
		return Admins{}, ErrMalformed
	}
	var out struct{ Value Admins }
	if err := adminsArgs.Copy(&out, values); err != nil {
		return Admins{}, ErrMalformed
	}
	return out.Value, nil
}

// EncodeEndpoints returns the exact message setEndpoints dispatches for this
// feed, version and set of groups. The contract assigns versions from 1 and the
// enclave treats 0 as "never applied", so version 0 describes a payload that can
// never exist and is refused.
func EncodeEndpoints(version uint64, feedID [21]byte, groups []Group) ([]byte, error) {
	if version == 0 {
		return nil, errors.New("configpayload: version must be non-zero")
	}
	if feedID == ([21]byte{}) {
		return nil, errors.New("configpayload: feed id is zero")
	}
	if len(groups) == 0 {
		return nil, errors.New("configpayload: no endpoint groups")
	}
	return endpointsArgs.Pack(Endpoints{Version: version, FeedId: feedID, Groups: groups})
}

// EncodeAdmins returns the exact message setAdmins dispatches.
func EncodeAdmins(version uint64, feedID [21]byte, roles []Role) ([]byte, error) {
	if version == 0 {
		return nil, errors.New("configpayload: version must be non-zero")
	}
	if feedID == ([21]byte{}) {
		return nil, errors.New("configpayload: feed id is zero")
	}
	if len(roles) == 0 {
		return nil, errors.New("configpayload: no admin roles")
	}
	return adminsArgs.Pack(Admins{Version: version, FeedId: feedID, Roles: roles})
}

// EndpointsHash predicts latestEndpointsHash for a publication at version.
// Compare it against the sender's getter after publishing, and against what the
// machine reports over GET_READINESS.
func EndpointsHash(version uint64, feedID [21]byte, groups []Group) (common.Hash, error) {
	message, err := EncodeEndpoints(version, feedID, groups)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(message), nil
}

// AdminsHash predicts latestAdminsHash on the same terms.
func AdminsHash(version uint64, feedID [21]byte, roles []Role) (common.Hash, error) {
	message, err := EncodeAdmins(version, feedID, roles)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(message), nil
}

// Tag renders a group or role name the way Solidity's bytes32("name") does.
// Names longer than 32 bytes are an error rather than a silent truncation: the
// contract would accept the truncated tag and the enclave would reject it as
// unknown, which reads as a schema disagreement rather than a typo.
func Tag(name string) ([32]byte, error) {
	var tag [32]byte
	if name == "" {
		return tag, errors.New("configpayload: tag is empty")
	}
	if len(name) > 32 {
		return tag, fmt.Errorf("configpayload: tag %q is longer than 32 bytes", name)
	}
	copy(tag[:], name)
	return tag, nil
}

// TagString reverses Tag: the trailing zero padding is stripped, and anything
// non-printable makes the tag unusable, which a caller reports as an unknown tag
// rather than echoing. The zero tag is not a name.
func TagString(tag [32]byte) (string, bool) {
	end := len(tag)
	for end > 0 && tag[end-1] == 0 {
		end--
	}
	if end == 0 {
		return "", false
	}
	for _, b := range tag[:end] {
		if b < 0x20 || b > 0x7e {
			return "", false
		}
	}
	return string(tag[:end]), true
}

// TagName is TagString for rendering what a chain returned, where an unusable
// tag is shown as empty rather than refused.
func TagName(tag [32]byte) string {
	name, _ := TagString(tag)
	return name
}

// Narrow converts a wire threshold to an int. The wire type is uint64 and every
// consumer downstream is an int, so the narrowing is guarded rather than
// assumed: an absurd threshold must be rejected, not wrapped into a small one.
func Narrow(v uint64) (int, bool) {
	if v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

// Threshold is Narrow's inverse for an operator-supplied value, rejecting
// anything that could not be satisfied.
func Threshold(v int) (uint64, error) {
	if v < 1 || v > math.MaxInt32 {
		return 0, fmt.Errorf("configpayload: threshold %d is out of range", v)
	}
	return uint64(v), nil
}
