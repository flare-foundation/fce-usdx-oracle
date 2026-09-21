package store

import (
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/common"
)

// The payload codecs live in pkg/wire/configpayload, shared with the
// publishing tools so one encoder produces the hash the sender records and one
// decoder reads what the machine is handed. This file names what this package
// uses of them and maps the codec's one error onto this package's vocabulary.
//
// Unlike the feed update, these payloads are DYNAMIC: they carry strings and
// arrays, so their encoding is not canonical and must never be recomputed here.
// The hash the store compares against is keccak256 of the message exactly as
// dispatched; Store.ApplyEndpoints and Store.ApplyAdmins hash the bytes they
// were handed and nothing else.

// EndpointKind mirrors the on-chain enum.
const (
	KindPublic  = configpayload.KindPublic
	KindPrivate = configpayload.KindPrivate
)

type (
	wireEndpoints = configpayload.Endpoints
	wireAdminRole = configpayload.Role
	wireAdmins    = configpayload.Admins

	// PrivateURL is a PRIVATE endpoint's commitment preimage, provisioned as a
	// SET_SECRET payload and checked here against the published urlHash.
	PrivateURL = configpayload.PrivateURL
)

func decodeEndpoints(payload []byte) (wireEndpoints, error) {
	w, err := configpayload.DecodeEndpoints(payload)
	if err != nil {
		return wireEndpoints{}, errBadPayload
	}
	return w, nil
}

func decodeAdmins(payload []byte) (wireAdmins, error) {
	w, err := configpayload.DecodeAdmins(payload)
	if err != nil {
		return wireAdmins{}, errBadPayload
	}
	return w, nil
}

func decodePrivateURL(preimage []byte) (PrivateURL, bool) {
	return configpayload.DecodePrivateURL(preimage)
}

// tagString renders a bytes32 tag as the ASCII name it is meant to be; an
// unusable tag is reported by the caller as unknown rather than echoed.
func tagString(tag [32]byte) (string, bool) { return configpayload.TagString(tag) }

// narrow converts a wire threshold to the int every consumer here takes.
func narrow(v uint64) (int, bool) { return configpayload.Narrow(v) }

// CommitPrivateURL is the commitment a PRIVATE endpoint's provisioned URL must
// reproduce, recomputed here from the blob the enclave was handed.
func CommitPrivateURL(u PrivateURL) (common.Hash, error) { return configpayload.CommitPrivateURL(u) }
