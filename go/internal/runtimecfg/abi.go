package runtimecfg

import (
	"math"

	teeoraclestructs "fce-usdx-oracle/pkg/contracts/teeoraclestructs"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The wire structs are the abigen-generated bindings for upstream's
// TeeOracleStructs ABI-only re-export (see pkg/contracts/teeoraclestructs,
// produced by scripts/generate-bindings.sh from the vendored interface). The
// struct layout therefore comes from the same compiled ABI the sender uses, not
// from a hand-written copy that could drift. The aliases below keep the rest of
// this package free of the generated type names.
//
// Both payloads name a FEED. One instructions sender serves every feed of its
// extension, with commitments and versions kept per feed and machine, so a
// configuration is only meaningful alongside the feed it was published for.
// This extension serves exactly one, and refuses a payload naming another
// rather than applying it — a machine that accepted a sibling feed's provider
// set would compute its own feed's price from it.
//
// Unlike the feed update, these payloads are DYNAMIC — they carry strings and
// arrays — so their encoding is not canonical and must never be recomputed.
// The hash the store compares against is keccak256 of the message exactly as
// dispatched; Store.ApplyEndpoints and Store.ApplyAdmins hash the bytes they
// were handed and nothing else.

// EndpointKind mirrors the on-chain enum.
const (
	KindPublic  = uint8(0)
	KindPrivate = uint8(1)
)

type wireEndpoint = teeoraclestructs.ITeeOracleInstructionsSenderEndpoint
type wireEndpointGroup = teeoraclestructs.ITeeOracleInstructionsSenderEndpointGroup
type wireEndpoints = teeoraclestructs.ITeeOracleInstructionsSenderEndpoints
type wireAdminRole = teeoraclestructs.ITeeOracleInstructionsSenderAdminRole
type wireAdmins = teeoraclestructs.ITeeOracleInstructionsSenderAdmins

// endpointsArgs / adminsArgs are the top-level tuple argument of the generated
// ABI's endpointsStruct / adminsStruct methods — the same abi.Arguments abigen
// derived, so Pack/Unpack here cannot disagree with the binding's struct types.
var (
	structsABI    = mustStructsABI()
	endpointsArgs = structMethodInputs("endpointsStruct")
	adminsArgs    = structMethodInputs("adminsStruct")
)

func mustStructsABI() *abi.ABI {
	parsed, err := teeoraclestructs.TeeOracleStructsMetaData.GetAbi()
	if err != nil {
		panic("runtimecfg: parsing TeeOracleStructs ABI: " + err.Error())
	}
	return parsed
}

func structMethodInputs(method string) abi.Arguments {
	m, ok := structsABI.Methods[method]
	if !ok {
		panic("runtimecfg: TeeOracleStructs ABI has no method " + method)
	}
	return m.Inputs
}

// PrivateURL is a PRIVATE endpoint's commitment preimage, in both directions:
// EncodePrivateURL packs one, decodePrivateURL returns one.
//
// Declared here rather than aliased from the generated bindings above: the
// chain holds only keccak256 of these bytes, as Endpoint.urlHash, so there is
// no on-chain struct to derive the layout from. The format is a convention
// between the publisher and this build, pinned by
// TestCommitPrivateURLMatchesSolidity.
type PrivateURL struct {
	Salt common.Hash
	Url  string
}

// argSpec is one ABI argument's name and type. The name is load-bearing:
// Arguments.Copy maps argument names onto struct fields, and an unnamed
// argument cannot be copied into a struct at all.
type argSpec struct{ name, typ string }

// privateURLArgs is the commitment preimage for a PRIVATE endpoint:
// abi.encode(salt, url). Salted by construction — a bare hash of a low-entropy
// URL is brute-forceable from chain data, and the URL is on-chain only as this
// commitment.
var privateURLArgs = mustArgs(
	argSpec{"salt", "bytes32"},
	argSpec{"url", "string"},
)

func mustArgs(specs ...argSpec) abi.Arguments {
	args := make(abi.Arguments, len(specs))
	for i, spec := range specs {
		ty, err := abi.NewType(spec.typ, "", nil)
		if err != nil {
			panic("runtimecfg: invalid ABI type " + spec.typ + ": " + err.Error())
		}
		args[i] = abi.Argument{Name: spec.name, Type: ty}
	}
	return args
}

// EncodePrivateURL returns the commitment preimage a PRIVATE endpoint's URL is
// provisioned as. Exported so the provisioning CLI produces byte-identical
// input to what verification recomputes.
func EncodePrivateURL(u PrivateURL) ([]byte, error) {
	return privateURLArgs.Pack(u.Salt, u.Url)
}

// CommitPrivateURL returns the urlHash a published PRIVATE endpoint must carry.
func CommitPrivateURL(u PrivateURL) (common.Hash, error) {
	preimage, err := EncodePrivateURL(u)
	if err != nil {
		return common.Hash{}, err
	}
	return crypto.Keccak256Hash(preimage), nil
}

// decodePrivateURL reverses EncodePrivateURL.
func decodePrivateURL(preimage []byte) (PrivateURL, bool) {
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

// Unpack returns the tuple as an anonymous struct that go-ethereum builds from
// the ABI components. Copying it into a named struct goes through a one-field
// WRAPPER because Arguments.Copy treats a struct destination as "unpack the
// single return value into this struct's first field" — handed the payload
// struct directly it would try to write the whole tuple into Version. The
// wrapper makes the destination that first field. abi.ConvertType would also
// work and panics on a mismatch, which is not a thing to do to a provisioning
// payload inside an enclave.
func decodeEndpoints(payload []byte) (wireEndpoints, error) {
	values, err := endpointsArgs.Unpack(payload)
	if err != nil || len(values) != 1 {
		return wireEndpoints{}, errBadPayload
	}
	var out struct{ Value wireEndpoints }
	if err := endpointsArgs.Copy(&out, values); err != nil {
		return wireEndpoints{}, errBadPayload
	}
	return out.Value, nil
}

func decodeAdmins(payload []byte) (wireAdmins, error) {
	values, err := adminsArgs.Unpack(payload)
	if err != nil || len(values) != 1 {
		return wireAdmins{}, errBadPayload
	}
	var out struct{ Value wireAdmins }
	if err := adminsArgs.Copy(&out, values); err != nil {
		return wireAdmins{}, errBadPayload
	}
	return out.Value, nil
}

// tagString renders a bytes32 tag as the ASCII name it is meant to be: the
// trailing zero padding is stripped, and anything non-printable makes the tag
// unusable, which the caller reports as an unknown tag rather than echoing.
func tagString(tag [32]byte) (string, bool) {
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

// narrow converts a wire threshold to an int. The wire type is uint64 and every
// consumer downstream is an int, so the narrowing is guarded rather than
// assumed: an absurd threshold must be rejected, not wrapped into a small one.
func narrow(v uint64) (int, bool) {
	if v > math.MaxInt32 {
		return 0, false
	}
	return int(v), true
}

// GroupSpec and RoleSpec are the ORDERED builder shapes for the two payloads.
// Ordered because the encoding — and therefore the hash the store compares
// against — depends on group and role order, which a map cannot preserve.
//
// The encoders below exist for tooling and tests. The enclave never calls them:
// it hashes the payload it was handed, because these payloads are dynamic and
// re-encoding is not guaranteed to reproduce the input. See the package doc.
type GroupSpec struct {
	Group     string
	Threshold uint64
	Endpoints []Endpoint
}

type RoleSpec struct {
	Role      string
	Threshold uint64
	Admins    []common.Address
}

// tagBytes32 renders a tag name the way Solidity's bytes32("name") does.
func tagBytes32(name string) [32]byte {
	var tag [32]byte
	copy(tag[:], name)
	return tag
}

// EncodeEndpoints builds the message TeeOracleInstructionsSender.setEndpoints
// would dispatch: abi.encode(Endpoints{version, feedId, groups}).
func EncodeEndpoints(version uint64, feedID [21]byte, groups []GroupSpec) ([]byte, error) {
	w := wireEndpoints{
		Version: version,
		FeedId:  feedID,
		Groups:  make([]wireEndpointGroup, 0, len(groups)),
	}
	for _, g := range groups {
		endpoints := make([]wireEndpoint, 0, len(g.Endpoints))
		for _, ep := range g.Endpoints {
			endpoints = append(endpoints, wireEndpoint{
				Kind:      ep.Kind,
				Url:       ep.URL,
				UrlHash:   ep.URLHash,
				SecretRef: ep.SecretRef,
			})
		}
		w.Groups = append(w.Groups, wireEndpointGroup{
			Group:     tagBytes32(g.Group),
			Threshold: g.Threshold,
			Endpoints: endpoints,
		})
	}
	return endpointsArgs.Pack(w)
}

// EncodeAdmins builds the message TeeOracleInstructionsSender.setAdmins would
// dispatch: abi.encode(Admins{version, feedId, roles}).
func EncodeAdmins(version uint64, feedID [21]byte, roles []RoleSpec) ([]byte, error) {
	w := wireAdmins{
		Version: version,
		FeedId:  feedID,
		Roles:   make([]wireAdminRole, 0, len(roles)),
	}
	for _, r := range roles {
		w.Roles = append(w.Roles, wireAdminRole{
			Role:      tagBytes32(r.Role),
			Threshold: r.Threshold,
			Admins:    append([]common.Address(nil), r.Admins...),
		})
	}
	return adminsArgs.Pack(w)
}
