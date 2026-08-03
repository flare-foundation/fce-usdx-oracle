// Package report encodes the signed price payload.
//
// # What is signed, and why each field is in it
//
//	struct PriceReport {
//	    bytes32 domain;         // keccak256("USDX_ORACLE_PRICE_V1")
//	    uint256 chainId;
//	    address store;
//	    bytes21 feedId;
//	    uint256 price;
//	    int8    decimals;
//	    uint64  observedAt;
//	    bytes32 instructionId;
//	}
//
//	body = abi.encode(report)
//	data = abi.encode(body, signature)
//
// FCC is explicitly "fire and forget": no ordering guarantee, no delivery
// guarantee, no replay protection beyond what a payload arranges for itself. So
// every binding here is load-bearing.
//
//   - **domain** separates this signature from any other thing the TEE identity
//     key ever signs. Without it, a signature produced for some future USDX
//     instruction could be replayed as a price.
//   - **chainId** blocks Coston2 → mainnet replay. Without it a price observed
//     on testnet is a valid mainnet price.
//   - **store** blocks replay into a different deployment of the same contract
//     on the same chain — including one an attacker deployed.
//   - **feedId** blocks reuse against a different feed served by the same store.
//   - **observedAt** is the store's monotonicity key, so it MUST be inside the
//     signed body. If it were outside, a relayer could rewrite it and either
//     replay an old price as fresh or push a real price out of order.
//   - **instructionId** ties the report to the instruction that requested it.
//
// # Carry the body, never re-encode it
//
// data carries `body` as raw bytes rather than the struct's fields. The store
// verifies the signature over exactly the bytes it received, then decodes them.
// Re-encoding on the way in would make verification depend on the encoder
// agreeing byte-for-byte with the TEE's, and any future divergence — a field
// reordering, a padding difference — would present as an invalid signature
// rather than as the encoding bug it is.
//
// # Price only
//
// Supply and backing components are excluded: they may be commercially
// sensitive to Hex (spec §4.1), and this payload is public and permanent. The
// consequence, recorded in the spec, is that consumers cannot recompute
// min(1, B/S) from on-chain data.
package report

import (
	"math/big"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// DomainName is the domain separator's preimage. Versioned: a change to the
// payload layout must change this string, so that old signatures cannot be
// reinterpreted under the new schema.
const DomainName = "USDX_ORACLE_PRICE_V1"

// FeedIDLength is the byte length of an FTSO feed id: 1 category byte plus 20
// bytes of ASCII name, right-padded with zeros.
const FeedIDLength = 21

// BodyLength is the encoded body's length: eight static ABI words.
const BodyLength = 8 * 32

// FeedID is an FTSO feed identifier.
type FeedID [FeedIDLength]byte

// Domain returns keccak256(DomainName).
func Domain() common.Hash { return crypto.Keccak256Hash([]byte(DomainName)) }

// PriceReport is the signed observation.
type PriceReport struct {
	ChainID       *big.Int
	Store         common.Address
	FeedID        FeedID
	Price         *big.Int
	Decimals      int8
	ObservedAt    uint64
	InstructionID common.Hash
}

// bodyArgs is the ABI layout of the struct's fields, in declaration order.
//
// Every field is a static type, so the encoding is eight 32-byte words with no
// offsets — which is what makes the golden-vector test in this package able to
// assert the layout word by word.
var bodyArgs = mustArgs(
	"bytes32", // domain
	"uint256", // chainId
	"address", // store
	"bytes21", // feedId
	"uint256", // price
	"int8",    // decimals
	"uint64",  // observedAt
	"bytes32", // instructionId
)

// dataArgs is the outer envelope: (bytes body, bytes signature).
var dataArgs = mustArgs("bytes", "bytes")

func mustArgs(types ...string) abi.Arguments {
	args := make(abi.Arguments, len(types))
	for i, t := range types {
		ty, err := abi.NewType(t, "", nil)
		if err != nil {
			// Only reachable via a typo in the literals above, which the
			// package's tests would catch immediately.
			panic("report: invalid ABI type " + t + ": " + err.Error())
		}
		args[i] = abi.Argument{Type: ty}
	}
	return args
}

// EncodeBody returns abi.encode(report) — the exact bytes to be signed.
func EncodeBody(r PriceReport) ([]byte, error) {
	if r.ChainID == nil || r.Price == nil {
		return nil, failure.Internal
	}
	if r.ChainID.Sign() <= 0 || r.Price.Sign() < 0 {
		return nil, failure.Internal
	}
	if r.Store == (common.Address{}) {
		// A zero store would produce a report bound to nothing, verifiable
		// against any deployment.
		return nil, failure.Internal
	}
	if r.Decimals < 0 {
		return nil, failure.Internal
	}
	if r.ObservedAt == 0 {
		// Zero is the store's "never seen" sentinel; a report carrying it could
		// never satisfy strict monotonicity.
		return nil, failure.Internal
	}

	body, err := bodyArgs.Pack(
		Domain(),
		r.ChainID,
		r.Store,
		r.FeedID,
		r.Price,
		r.Decimals,
		r.ObservedAt,
		r.InstructionID,
	)
	if err != nil {
		return nil, failure.Internal
	}
	return body, nil
}

// EncodeData wraps a signed body into the ActionResult payload:
// abi.encode(body, signature).
func EncodeData(body, signature []byte) ([]byte, error) {
	if len(body) != BodyLength || len(signature) == 0 {
		return nil, failure.Internal
	}
	data, err := dataArgs.Pack(body, signature)
	if err != nil {
		return nil, failure.Internal
	}
	return data, nil
}

// DecodeData splits an ActionResult payload back into body and signature.
// Used by tests and tooling; the store does the equivalent on-chain.
func DecodeData(data []byte) (body, signature []byte, err error) {
	values, err := dataArgs.Unpack(data)
	if err != nil || len(values) != 2 {
		return nil, nil, failure.Internal
	}
	body, ok := values[0].([]byte)
	if !ok {
		return nil, nil, failure.Internal
	}
	signature, ok = values[1].([]byte)
	if !ok {
		return nil, nil, failure.Internal
	}
	return body, signature, nil
}

// DecodeBody reverses EncodeBody. It verifies the domain separator, so a
// payload signed for a different schema is rejected here rather than silently
// misread.
func DecodeBody(body []byte) (PriceReport, error) {
	values, err := bodyArgs.Unpack(body)
	if err != nil || len(values) != 8 {
		return PriceReport{}, failure.Internal
	}

	domain, ok := values[0].([32]byte)
	if !ok || common.Hash(domain) != Domain() {
		return PriceReport{}, failure.Internal
	}

	chainID, _ := values[1].(*big.Int)
	store, _ := values[2].(common.Address)
	feedID, _ := values[3].([FeedIDLength]byte)
	priceV, _ := values[4].(*big.Int)
	decimals, _ := values[5].(int8)
	observedAt, _ := values[6].(uint64)
	instructionID, _ := values[7].([32]byte)

	if chainID == nil || priceV == nil {
		return PriceReport{}, failure.Internal
	}

	return PriceReport{
		ChainID:       chainID,
		Store:         store,
		FeedID:        feedID,
		Price:         priceV,
		Decimals:      decimals,
		ObservedAt:    observedAt,
		InstructionID: common.Hash(instructionID),
	}, nil
}

// ParseFeedID builds a feed id from a category byte and an ASCII name.
//
// Verified against flare-smart-contracts-v2 FtsoFeedIdConverter.getFeedId: the
// id is bytes21 = 1 category byte ‖ 20 bytes of name, right-padded with zeros.
// FtsoV2._isCustomFeedId requires 32 <= category < 64 for a custom feed.
func ParseFeedID(category byte, name string) (FeedID, error) {
	var id FeedID
	if category < 32 || category >= 64 {
		// Outside this range FtsoV2.addCustomFeeds reverts with
		// "invalid feed category".
		return id, failure.Internal
	}
	if len(name) == 0 || len(name) > FeedIDLength-1 {
		return id, failure.Internal
	}
	for i := 0; i < len(name); i++ {
		// Non-ASCII would encode to multiple bytes and silently truncate.
		if name[i] < 0x20 || name[i] > 0x7E {
			return id, failure.Internal
		}
	}
	id[0] = category
	copy(id[1:], name)
	return id, nil
}
