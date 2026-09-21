package store

import "fce-usdx-oracle/pkg/wire/configpayload"

// The payload codecs are pkg/wire/configpayload's, shared with the publishing
// tools so one encoder produces the hash the sender records and one decoder
// reads what the machine is handed. What this package adds is the mapping of
// the codec's one error onto its own vocabulary.
//
// Unlike the feed update, these payloads are DYNAMIC: they carry strings and
// arrays, so their encoding is not canonical and must never be recomputed here.
// The hash the store compares against is keccak256 of the message exactly as
// dispatched; Store.ApplyEndpoints and Store.ApplyAdmins hash the bytes they
// were handed and nothing else.

func decodeEndpoints(payload []byte) (configpayload.Endpoints, error) {
	w, err := configpayload.DecodeEndpoints(payload)
	if err != nil {
		return configpayload.Endpoints{}, errBadPayload
	}
	return w, nil
}

func decodeAdmins(payload []byte) (configpayload.Admins, error) {
	w, err := configpayload.DecodeAdmins(payload)
	if err != nil {
		return configpayload.Admins{}, errBadPayload
	}
	return w, nil
}
