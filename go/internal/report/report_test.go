package report

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

func testReport(t *testing.T) PriceReport {
	t.Helper()
	feedID, err := ParseFeedID(0x20, "USDX/USD")
	if err != nil {
		t.Fatalf("ParseFeedID: %v", err)
	}
	return PriceReport{
		ChainID:       big.NewInt(114), // Coston2
		Store:         common.HexToAddress("0x00000000000000000000000000000000000000AA"),
		FeedID:        feedID,
		Price:         big.NewInt(100_000_000), // $1.00 at 8 decimals
		Decimals:      8,
		ObservedAt:    1_700_000_000,
		InstructionID: common.HexToHash("0x" + "11"),
	}
}

// The golden vector. Built word by word from the ABI spec rather than by
// calling the encoder, so this is an independent derivation of the layout the
// Solidity side must agree with — not a tautology asserting the encoder equals
// itself.
//
// Every field is a static type, so abi.encode is eight concatenated 32-byte
// words with no offsets and no length prefixes.
func TestEncodeBody_GoldenLayout(t *testing.T) {
	r := testReport(t)

	word := func(b []byte) []byte {
		if len(b) > 32 {
			t.Fatalf("word too long: %d", len(b))
		}
		w := make([]byte, 32)
		copy(w[32-len(b):], b) // left-pad: numbers and addresses
		return w
	}
	rightPadded := func(b []byte) []byte {
		w := make([]byte, 32)
		copy(w, b) // right-pad: fixed-size bytesN
		return w
	}

	var want []byte
	want = append(want, Domain().Bytes()...)                        // bytes32 domain
	want = append(want, word(big.NewInt(114).Bytes())...)           // uint256 chainId
	want = append(want, word(r.Store.Bytes())...)                   // address store
	want = append(want, rightPadded(r.FeedID[:])...)                // bytes21 feedId
	want = append(want, word(big.NewInt(100_000_000).Bytes())...)   // uint256 price
	want = append(want, word([]byte{8})...)                         // int8 decimals
	want = append(want, word(big.NewInt(1_700_000_000).Bytes())...) // uint64 observedAt
	want = append(want, r.InstructionID.Bytes()...)                 // bytes32 instructionId

	got, err := EncodeBody(r)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}

	if len(got) != BodyLength {
		t.Fatalf("body length %d, want %d", len(got), BodyLength)
	}
	if !bytes.Equal(got, want) {
		for i := 0; i < 8; i++ {
			g := got[i*32 : (i+1)*32]
			w := want[i*32 : (i+1)*32]
			if !bytes.Equal(g, w) {
				t.Errorf("word %d:\n got  %s\n want %s", i, hex.EncodeToString(g), hex.EncodeToString(w))
			}
		}
		t.Fatal("encoded body does not match the hand-derived layout")
	}

	// Emit the vector so the Solidity test can pin the same bytes.
	t.Logf("golden body: 0x%s", hex.EncodeToString(got))
}

// bytes21 is right-padded, unlike every numeric field. Getting this backwards
// produces a plausible-looking id that matches no registered feed.
func TestEncodeBody_FeedIDIsRightPadded(t *testing.T) {
	body, err := EncodeBody(testReport(t))
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}

	feedWord := body[3*32 : 4*32]
	if feedWord[0] != 0x20 {
		t.Errorf("feed id category byte should lead the word, got 0x%02x", feedWord[0])
	}
	if !bytes.Equal(feedWord[1:9], []byte("USDX/USD")) {
		t.Errorf("feed id name not at the front of the word: %s", hex.EncodeToString(feedWord))
	}
	for _, b := range feedWord[21:] {
		if b != 0 {
			t.Errorf("bytes 21..32 of a bytes21 word must be zero: %s", hex.EncodeToString(feedWord))
			break
		}
	}
}

func TestDomainSeparator(t *testing.T) {
	want := crypto.Keccak256Hash([]byte("USDX_ORACLE_PRICE_V1"))
	if Domain() != want {
		t.Errorf("Domain() = %s, want %s", Domain().Hex(), want.Hex())
	}
}

func TestRoundTrip(t *testing.T) {
	r := testReport(t)

	body, err := EncodeBody(r)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	got, err := DecodeBody(body)
	if err != nil {
		t.Fatalf("DecodeBody: %v", err)
	}

	if got.ChainID.Cmp(r.ChainID) != 0 {
		t.Errorf("chainId = %s, want %s", got.ChainID, r.ChainID)
	}
	if got.Store != r.Store {
		t.Errorf("store = %s, want %s", got.Store, r.Store)
	}
	if got.FeedID != r.FeedID {
		t.Errorf("feedId = %x, want %x", got.FeedID, r.FeedID)
	}
	if got.Price.Cmp(r.Price) != 0 {
		t.Errorf("price = %s, want %s", got.Price, r.Price)
	}
	if got.Decimals != r.Decimals {
		t.Errorf("decimals = %d, want %d", got.Decimals, r.Decimals)
	}
	if got.ObservedAt != r.ObservedAt {
		t.Errorf("observedAt = %d, want %d", got.ObservedAt, r.ObservedAt)
	}
	if got.InstructionID != r.InstructionID {
		t.Errorf("instructionId = %s, want %s", got.InstructionID.Hex(), r.InstructionID.Hex())
	}
}

// A body signed under a different schema version must not decode as this one.
func TestDecodeBody_RejectsWrongDomain(t *testing.T) {
	body, err := EncodeBody(testReport(t))
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}

	tampered := make([]byte, len(body))
	copy(tampered, body)
	tampered[0] ^= 0xFF // corrupt the domain separator

	if _, err := DecodeBody(tampered); err == nil {
		t.Error("expected a body with a foreign domain separator to be rejected")
	}
}

func TestEncodeBody_RejectsUnsafeReports(t *testing.T) {
	cases := []struct {
		name   string
		mutate func(*PriceReport)
	}{
		{"nil chain id", func(r *PriceReport) { r.ChainID = nil }},
		{"zero chain id", func(r *PriceReport) { r.ChainID = big.NewInt(0) }},
		{"nil price", func(r *PriceReport) { r.Price = nil }},
		{"negative price", func(r *PriceReport) { r.Price = big.NewInt(-1) }},
		{"zero store binds to nothing", func(r *PriceReport) { r.Store = common.Address{} }},
		{"negative decimals", func(r *PriceReport) { r.Decimals = -1 }},
		{"zero observedAt is the never-seen sentinel", func(r *PriceReport) { r.ObservedAt = 0 }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			r := testReport(t)
			c.mutate(&r)
			if _, err := EncodeBody(r); err == nil {
				t.Error("expected an error")
			}
		})
	}
}

// --- envelope ---

func TestDataRoundTrip(t *testing.T) {
	body, err := EncodeBody(testReport(t))
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	sig := bytes.Repeat([]byte{0xAB}, 65)

	data, err := EncodeData(body, sig)
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}

	gotBody, gotSig, err := DecodeData(data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if !bytes.Equal(gotBody, body) {
		t.Error("body did not survive the envelope round trip")
	}
	if !bytes.Equal(gotSig, sig) {
		t.Error("signature did not survive the envelope round trip")
	}
}

// The body must survive as raw bytes. The store verifies the signature over
// exactly what it received, so a re-encode anywhere in the path would turn a
// future encoding divergence into an "invalid signature" rather than a visible
// encoding bug.
func TestDataCarriesBodyVerbatim(t *testing.T) {
	body, err := EncodeBody(testReport(t))
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}
	data, err := EncodeData(body, bytes.Repeat([]byte{0x01}, 65))
	if err != nil {
		t.Fatalf("EncodeData: %v", err)
	}
	if !bytes.Contains(data, body) {
		t.Error("the encoded body does not appear verbatim inside the envelope")
	}
}

func TestEncodeData_Rejects(t *testing.T) {
	body, _ := EncodeBody(testReport(t))
	if _, err := EncodeData(body, nil); err == nil {
		t.Error("expected an error for an empty signature")
	}
	if _, err := EncodeData([]byte{1, 2, 3}, bytes.Repeat([]byte{1}, 65)); err == nil {
		t.Error("expected an error for a wrong-length body")
	}
}

// --- feed id ---

func TestParseFeedID(t *testing.T) {
	id, err := ParseFeedID(0x20, "USDX/USD")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	// Verified against FtsoFeedIdConverter.getFeedId: category byte then ASCII,
	// right-padded with zeros to 21 bytes.
	want := "20" + hex.EncodeToString([]byte("USDX/USD")) + "000000000000000000000000"
	if got := hex.EncodeToString(id[:]); got != want {
		t.Errorf("feed id = %s, want %s", got, want)
	}
}

// FtsoV2._isCustomFeedId requires 32 <= category < 64; addCustomFeeds reverts
// with "invalid feed category" outside it.
func TestParseFeedID_CategoryRange(t *testing.T) {
	for _, c := range []byte{0, 1, 31, 64, 65, 255} {
		if _, err := ParseFeedID(c, "USDX/USD"); err == nil {
			t.Errorf("category %d is outside the custom range and must be rejected", c)
		}
	}
	for _, c := range []byte{32, 33, 62, 63} {
		if _, err := ParseFeedID(c, "USDX/USD"); err != nil {
			t.Errorf("category %d is a valid custom feed category: %v", c, err)
		}
	}
}

func TestParseFeedID_NameConstraints(t *testing.T) {
	if _, err := ParseFeedID(0x20, ""); err == nil {
		t.Error("expected an error for an empty name")
	}
	if _, err := ParseFeedID(0x20, "123456789012345678901"); err == nil {
		t.Error("expected an error for a name over 20 bytes")
	}
	// Exactly 20 is the limit and must be accepted.
	if _, err := ParseFeedID(0x20, "12345678901234567890"); err != nil {
		t.Errorf("a 20-byte name must be accepted: %v", err)
	}
	// Non-ASCII would encode to multiple bytes and silently truncate the name.
	if _, err := ParseFeedID(0x20, "USDX/€UR"); err == nil {
		t.Error("expected an error for a non-ASCII name")
	}
}
