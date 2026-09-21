package store

import (
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
)

// The tests build payloads by name, the way an operator's configuration reads,
// and the enclave only ever decodes; these builders convert the named shape into
// the wire's and hand it to the shared codec.
//
// GroupSpec and RoleSpec are ORDERED because the encoding, and therefore the
// hash the store compares against, depends on group and role order.

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

// tagBytes32 renders a tag name the way Solidity's bytes32("name") does, with
// no length check: a test may want an over-long name to see it refused.
func tagBytes32(name string) [32]byte {
	var tag [32]byte
	copy(tag[:], name)
	return tag
}

func EncodeEndpoints(version uint64, feedID [21]byte, groups []GroupSpec) ([]byte, error) {
	w := make([]configpayload.Group, 0, len(groups))
	for _, g := range groups {
		endpoints := make([]configpayload.Endpoint, 0, len(g.Endpoints))
		for _, ep := range g.Endpoints {
			endpoints = append(endpoints, configpayload.Endpoint{
				Kind: ep.Kind, Url: ep.URL, UrlHash: ep.URLHash, SecretRef: ep.SecretRef,
			})
		}
		w = append(w, configpayload.Group{Group: tagBytes32(g.Group), Threshold: g.Threshold, Endpoints: endpoints})
	}
	return configpayload.EncodeEndpoints(version, feedID, w)
}

func EncodeAdmins(version uint64, feedID [21]byte, roles []RoleSpec) ([]byte, error) {
	w := make([]configpayload.Role, 0, len(roles))
	for _, r := range roles {
		w = append(w, configpayload.Role{
			Role: tagBytes32(r.Role), Threshold: r.Threshold, Admins: append([]common.Address(nil), r.Admins...),
		})
	}
	return configpayload.EncodeAdmins(version, feedID, w)
}

// EncodePrivateURL packs the commitment preimage WITHOUT the codec's salt check,
// so a test can hand the enclave the unsalted blob a hostile sender might and
// see it refused there. The layout is the codec's: abi.encode(salt, url).
func EncodePrivateURL(u PrivateURL) ([]byte, error) {
	salt, err := abi.NewType("bytes32", "", nil)
	if err != nil {
		return nil, err
	}
	url, err := abi.NewType("string", "", nil)
	if err != nil {
		return nil, err
	}
	return abi.Arguments{{Type: salt}, {Type: url}}.Pack(u.Salt, u.Url)
}
