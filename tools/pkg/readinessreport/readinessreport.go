// Package readinessreport decodes the USDX oracle's signed readiness payload.
//
//	body    = abi.encode(bytes32 domain, bytes32 instructionId, uint64 timestamp,
//	                     bytes32 payloadHash)
//	data    = abi.encode(bytes body, bytes signature, bytes payload)
//	payload = JSON, the machine's account of itself
//
// The layout is declared here rather than imported from internal/wire/readiness
// so tools/ stays independent of any one implementation — this asserts on the
// wire contract and must run unchanged against every extension language, the
// same rule pricereport follows.
//
// # What a verified report does and does not prove
//
// Decode checks that the payload is the one the signature covers, and recovers
// who signed it. That makes it the enclave's own claim rather than whatever
// answered a port — which is the entire reason this exists instead of reading
// GET /state over the network.
//
// It remains a SELF-REPORT. A machine says what it believes; a feed update it
// lands says what the chain accepted, because the feed store verified the
// hashes before taking it. Use readiness to find problems early and to decide whether
// a cycle is worth paying for, never as the authority on whether a machine's
// prices will be accepted.
package readinessreport

import (
	"encoding/json"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// DomainName must match internal/wire/readiness. A mismatch here would accept a
// signature made for something else, so it is compared, never assumed.
const DomainName = "USDX_ORACLE_READINESS_V1"

// BodyLength is four static ABI words.
const BodyLength = 4 * 32

func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// Payload is the JSON the machine emits. Field names match
// pkg/types.ReadinessReport.
type Payload struct {
	Readiness struct {
		EndpointsSet  bool     `json:"endpointsSet"`
		EndpointsHash string   `json:"endpointsHash"`
		AdminsSet     bool     `json:"adminsSet"`
		AdminsHash    string   `json:"adminsHash"`
		Ready         bool     `json:"ready"`
		MissingRefs   []string `json:"missingSecretRefs"`
	} `json:"readiness"`

	LastObservedAt uint64 `json:"lastObservedAt"`
	ObservationAge uint64 `json:"observationAge"`
	WouldSign      bool   `json:"wouldSign"`
	Observations   int    `json:"observations"`
	Failures       int    `json:"failures"`
	LastErrorClass string `json:"lastErrorClass"`

	SourceHealth map[string]string `json:"sourceHealth"`
	ChainID      uint64            `json:"chainId"`
}

// Report is a decoded, signature-checked readiness answer.
type Report struct {
	Payload Payload

	// Signer is recovered from the signature. The machine is identified by this
	// and nothing else — there is no teeId field to disagree with it.
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

var bodyArgs = mustArgs("bytes32", "bytes32", "uint64", "bytes32")
var dataArgs = mustArgs("bytes", "bytes", "bytes")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			panic("readinessreport: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// Decode splits the ActionResult payload, verifies the payload against the hash
// the signature covers, and recovers the signer.
func Decode(data []byte) (Report, error) {
	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 3 {
		return Report{}, errors.New("not a readiness payload: outer decode failed")
	}

	body, ok := values[0].([]byte)
	if !ok || len(body) != BodyLength {
		return Report{}, errors.Errorf("body is not %d bytes", BodyLength)
	}
	signature, ok := values[1].([]byte)
	if !ok || len(signature) != 65 {
		return Report{}, errors.New("signature is not 65 bytes")
	}
	raw, ok := values[2].([]byte)
	if !ok || len(raw) == 0 {
		return Report{}, errors.New("empty payload")
	}

	inner, err := bodyArgs.Unpack(body)
	if err != nil || len(inner) != 4 {
		return Report{}, errors.New("body decode failed")
	}

	domain, _ := inner[0].([32]byte)
	if common.Hash(domain) != Domain() {
		// A price report would land here. Refusing on the domain is what stops
		// one signature being read as the other.
		return Report{}, errors.Errorf("wrong domain: not a %s payload", DomainName)
	}

	instructionID, _ := inner[1].([32]byte)
	timestamp, _ := inner[2].(uint64)
	payloadHash, _ := inner[3].([32]byte)

	// The signature covers the hash, so an unchecked payload would be unsigned
	// data smuggled in beside a valid signature.
	if crypto.Keccak256Hash(raw) != common.Hash(payloadHash) {
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
		InstructionID: common.Hash(instructionID),
		Timestamp:     timestamp,
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
