// Package readinessreport verifies the signed readiness payload a machine
// returns for GET_READINESS and recovers the machine that signed it.
//
// The layout is the extension's, imported from pkg/wire/readiness; what
// this package adds is the verification the enclave never performs on its own
// output: that the payload is the one the signature covers, and who signed it.
// That makes the report the enclave's own claim rather than whatever answered a
// port, which is the entire reason it is signed.
//
// It remains a SELF-REPORT. A machine says what it believes; a feed update it
// lands says what the chain accepted, because the feed store verified the
// hashes before taking it. Use readiness to find problems early and to decide
// whether a cycle is worth paying for, never as the authority on whether a
// machine's prices will be accepted.
package readinessreport

import (
	"encoding/json"

	"fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/pkg/wire/readiness"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// Payload is the JSON the machine emits, the extension's own shape.
type Payload = types.ReadinessReport

// Report is a decoded, signature-checked readiness answer.
type Report struct {
	Payload Payload

	// Signer is recovered from the signature. The machine is identified by this
	// and nothing else: there is no teeId field to disagree with it.
	Signer common.Address

	// InstructionID and Timestamp are the replay bindings: an answer captured
	// for one request cannot be presented as the answer to another, or as a
	// current one later.
	InstructionID common.Hash
	Timestamp     uint64

	// Raw is the payload exactly as signed, for a caller that wants to re-verify
	// or store it.
	Raw []byte
}

// Decode splits the ActionResult payload, checks the body's domain, verifies the
// payload against the hash the signature covers, and recovers the signer.
func Decode(data []byte) (Report, error) {
	body, signature, raw, err := readiness.DecodeData(data)
	if err != nil {
		return Report{}, errors.New("not a readiness payload")
	}
	if len(signature) != 65 {
		return Report{}, errors.New("signature is not 65 bytes")
	}

	// A price report would fail here: the wire package refuses a body whose
	// domain is not the readiness one.
	header, err := readiness.DecodeBody(body)
	if err != nil {
		return Report{}, errors.Errorf("body is not a %s header", readiness.DomainName)
	}

	// The signature covers the hash, so an unchecked payload would be unsigned
	// data smuggled in beside a valid signature.
	if crypto.Keccak256Hash(raw) != header.PayloadHash {
		return Report{}, errors.New("the payload does not match the hash the signature covers")
	}

	signer, err := recoverSigner(body, signature)
	if err != nil {
		return Report{}, err
	}

	var p Payload
	if err := json.Unmarshal(raw, &p); err != nil {
		return Report{}, errors.Errorf("payload is not readiness JSON: %s", err)
	}

	return Report{
		Payload:       p,
		Signer:        signer,
		InstructionID: header.InstructionID,
		Timestamp:     header.Timestamp,
		Raw:           raw,
	}, nil
}

// recoverSigner mirrors the store's recovery: EIP-191 over keccak(body), with v
// normalised. tee-node returns 27/28 and go-ethereum wants 0/1.
func recoverSigner(body, signature []byte) (common.Address, error) {
	sig := make([]byte, 65)
	copy(sig, signature)
	switch sig[64] {
	case 27, 28:
		sig[64] -= 27
	case 0, 1:
	default:
		return common.Address{}, errors.Errorf("bad recovery id %d", sig[64])
	}

	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), sig)
	if err != nil || pub == nil {
		return common.Address{}, errors.New("signature does not recover")
	}
	return crypto.PubkeyToAddress(*pub), nil
}
