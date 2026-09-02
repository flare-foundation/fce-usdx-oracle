// Package oraclepayload builds the two configuration payloads
// TeeOracleInstructionsSender dispatches, and predicts the hash it will record.
//
//	SET_ENDPOINTS message = abi.encode(Endpoints{version, groups})
//	SET_ADMINS    message = abi.encode(Admins{version, roles})
//
// The publishing tools need both halves for different reasons. The GROUPS and
// ROLES go on the wire as typed call arguments — `setEndpoints(address[],
// EndpointGroup[], address)` — so the generated bindings pack those. The
// WRAPPER, version and feed id included, is assembled by the contract and never
// appears as a call argument; it reaches an ABI only through upstream's
// TeeOracleStructs re-export. That is the reason this package exists: without
// it a publisher cannot know, in advance, the hash every subsequent feed update
// will have to carry.
//
// Both wrappers name a FEED, because one sender serves every feed of its
// extension and keeps commitments per feed and machine. Predicting a hash for
// the wrong feed is as wrong as predicting it for the wrong version.
//
// Both encodings are DYNAMIC. Unlike the feed update there is no single valid
// byte string for a given configuration, so an encoder that differs from the
// contract's by so much as a padding word produces a different hash — and the
// failure is silent and total: expectedEndpointsHash is recorded, the machine
// applies the payload it actually received, and every submitFeedUpdate then
// reverts StaleEndpoints. oraclepayload_test.go pins this against bytes
// Solidity produced, which is the only check that can catch it.
//
// This restates the schema rather than importing the enclave's copy in
// go/internal/provisioning/store, keeping tools/ an independent cross-check of the
// extension rather than a mirror of it. The two are kept honest by asserting
// both against the same Solidity fixture, not by sharing code.
package oraclepayload

import (
	"math"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestructs"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// EndpointKind mirrors the on-chain enum.
const (
	KindPublic  = uint8(0)
	KindPrivate = uint8(1)
)

// Every struct below is an abigen type, aliased so callers building a payload
// never have to spell out the generated names.
//
// They all come from teeoraclestructs, upstream's ABI-only re-export. Two
// reasons. The WRAPPERS are assembled inside the contract and never appear as
// call arguments, so that re-export is the only ABI they reach at all — before
// it existed they had to be hand-mirrored here, exactly the second source of
// truth a dynamic encoding punishes. And abigen emits its own copy of every
// nested struct into each package that mentions one, so the identical
// EndpointGroup exists in teeoraclesender too, as a distinct named type Go will
// not convert between in a slice; taking all of them from one package keeps a
// payload buildable without casts.
//
// The typed binding methods in teeoraclesender want their own copies. go-ethereum
// packs by ABI component name rather than by named type, so the untyped
// bind.BoundContract path accepts these as they are.
type (
	Group    = teeoraclestructs.ITeeOracleInstructionsSenderEndpointGroup
	Endpoint = teeoraclestructs.ITeeOracleInstructionsSenderEndpoint
	Role     = teeoraclestructs.ITeeOracleInstructionsSenderAdminRole

	endpoints   = teeoraclestructs.ITeeOracleInstructionsSenderEndpoints
	admins      = teeoraclestructs.ITeeOracleInstructionsSenderAdmins
	feedRequest = teeoraclestructs.ITeeOracleInstructionsSenderFeedUpdateRequest
)

var endpointsArgs = abi.Arguments{{Type: mustTuple([]abi.ArgumentMarshaling{
	{Name: "version", Type: "uint64"},
	{Name: "feedId", Type: "bytes21"},
	{Name: "groups", Type: "tuple[]", Components: []abi.ArgumentMarshaling{
		{Name: "group", Type: "bytes32"},
		{Name: "threshold", Type: "uint64"},
		{Name: "endpoints", Type: "tuple[]", Components: []abi.ArgumentMarshaling{
			{Name: "kind", Type: "uint8"},
			{Name: "url", Type: "string"},
			{Name: "urlHash", Type: "bytes32"},
			{Name: "secretRef", Type: "string"},
		}},
	}},
})}}

var adminsArgs = abi.Arguments{{Type: mustTuple([]abi.ArgumentMarshaling{
	{Name: "version", Type: "uint64"},
	{Name: "feedId", Type: "bytes21"},
	{Name: "roles", Type: "tuple[]", Components: []abi.ArgumentMarshaling{
		{Name: "role", Type: "bytes32"},
		{Name: "threshold", Type: "uint64"},
		{Name: "admins", Type: "address[]"},
	}},
})}}

// feedRequestArgs is abi.encode(FeedUpdateRequest) — the GET_FEED instruction
// message. One static field, so it encodes inline as a single word.
var feedRequestArgs = abi.Arguments{{Type: mustTuple([]abi.ArgumentMarshaling{
	{Name: "feedId", Type: "bytes21"},
})}}

// privateURLArgs is the commitment preimage for a PRIVATE endpoint:
// abi.encode(salt, url). Salted because a bare hash of a low-entropy URL is
// brute-forceable from chain data, and the commitment is public.
var privateURLArgs = mustArgs("bytes32", "string")

func mustTuple(components []abi.ArgumentMarshaling) abi.Type {
	t, err := abi.NewType("tuple", "", components)
	if err != nil {
		panic("oraclepayload: invalid tuple type: " + err.Error())
	}
	return t
}

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("oraclepayload: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// Tag renders a group or role name the way Solidity's bytes32("name") does.
// Names longer than 32 bytes are an error rather than a silent truncation: the
// contract would accept the truncated tag and the extension would reject it as
// unknown, which reads as a schema disagreement rather than a typo.
func Tag(name string) ([32]byte, error) {
	var tag [32]byte
	if name == "" {
		return tag, errors.New("tag is empty")
	}
	if len(name) > 32 {
		return tag, errors.Errorf("tag %q is longer than 32 bytes", name)
	}
	copy(tag[:], name)
	return tag, nil
}

// TagName reverses Tag, for rendering what a chain returned.
func TagName(tag [32]byte) string {
	end := len(tag)
	for end > 0 && tag[end-1] == 0 {
		end--
	}
	return string(tag[:end])
}

// EncodeEndpoints returns the exact message setEndpoints will dispatch for this
// feed, version and set of groups.
func EncodeEndpoints(version uint64, feedID [21]byte, groups []Group) ([]byte, error) {
	if version == 0 {
		// The contract assigns versions from 1, and the extension treats 0 as
		// "never applied". Predicting a hash for version 0 would be predicting a
		// payload that can never exist.
		return nil, errors.New("version must be non-zero")
	}
	if feedID == ([21]byte{}) {
		return nil, errors.New("feed id is zero")
	}
	if len(groups) == 0 {
		return nil, errors.New("no endpoint groups")
	}
	return endpointsArgs.Pack(endpoints{Version: version, FeedId: feedID, Groups: groups})
}

// EncodeAdmins returns the exact message setAdmins will dispatch.
func EncodeAdmins(version uint64, feedID [21]byte, roles []Role) ([]byte, error) {
	if version == 0 {
		return nil, errors.New("version must be non-zero")
	}
	if feedID == ([21]byte{}) {
		return nil, errors.New("feed id is zero")
	}
	if len(roles) == 0 {
		return nil, errors.New("no admin roles")
	}
	return adminsArgs.Pack(admins{Version: version, FeedId: feedID, Roles: roles})
}

// EndpointsHash predicts expectedEndpointsHash for a publication at version.
// Compare it against the sender's getter after publishing, and against what the
// machine reports over GET_READINESS.
func EndpointsHash(version uint64, feedID [21]byte, groups []Group) (common.Hash, error) {
	message, err := EncodeEndpoints(version, feedID, groups)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(message), nil
}

// AdminsHash predicts expectedAdminsHash on the same terms.
func AdminsHash(version uint64, feedID [21]byte, roles []Role) (common.Hash, error) {
	message, err := EncodeAdmins(version, feedID, roles)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(message), nil
}

// EncodeFeedRequest returns the GET_FEED instruction message the sender
// dispatches for a feed. It replaced the old one-byte 0x01 envelope when a
// single sender started serving several feeds.
func EncodeFeedRequest(feedID [21]byte) ([]byte, error) {
	if feedID == ([21]byte{}) {
		return nil, errors.New("feed id is zero")
	}
	return feedRequestArgs.Pack(feedRequest{FeedId: feedID})
}

// CommitPrivateURL returns the urlHash a PRIVATE endpoint must publish for a
// URL that will be provisioned off-chain under the matching secret ref. The
// salt must be the one provisioned alongside the URL — the enclave recomputes
// this and refuses the endpoint if it disagrees.
func CommitPrivateURL(salt common.Hash, url string) (common.Hash, error) {
	if salt == (common.Hash{}) {
		return common.Hash{}, errors.New("commitment salt is zero")
	}
	preimage, err := privateURLArgs.Pack(salt, url)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(preimage), nil
}

// EncodePrivateURL returns the blob provisioned to the machine for a PRIVATE
// endpoint. Not calldata — it travels as a SET_SECRET envelope payload.
func EncodePrivateURL(salt common.Hash, url string) ([]byte, error) {
	if salt == (common.Hash{}) {
		return nil, errors.New("commitment salt is zero")
	}
	return privateURLArgs.Pack(salt, url)
}

// Threshold narrows an operator-supplied threshold to the wire's uint64,
// rejecting anything that could not be satisfied.
func Threshold(v int) (uint64, error) {
	if v < 1 || v > math.MaxInt32 {
		return 0, errors.Errorf("threshold %d is out of range", v)
	}
	return uint64(v), nil
}
