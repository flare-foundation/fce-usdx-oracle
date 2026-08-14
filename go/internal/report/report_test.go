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
	return PriceReport{
		Price:         big.NewInt(100_000_000), // $1.00 at 8 decimals
		Decimals:      8,
		ObservedAt:    1_700_000_000,
		InstructionID: common.HexToHash("0x" + "11"),
		EndpointsHash: common.HexToHash("0x" + "9f3c"),
		AdminsHash:    common.HexToHash("0x" + "5e21"),
	}
}

// The golden vector. Built word by word from the ABI spec rather than by
// calling the encoder, so this is an independent derivation of the layout the
// Solidity side must agree with — not a tautology asserting the encoder equals
// itself.
//
// Every field is a static type, so abi.encode is seven concatenated 32-byte
// words with no offsets and no length prefixes.
func TestEncodeBody_GoldenLayout(t *testing.T) {
	r := testReport(t)

	word := func(b []byte) []byte {
		if len(b) > 32 {
			t.Fatalf("word too long: %d", len(b))
		}
		w := make([]byte, 32)
		copy(w[32-len(b):], b) // left-pad: numbers
		return w
	}

	var want []byte
	want = append(want, Domain().Bytes()...)                        // bytes32 domain
	want = append(want, word(big.NewInt(100_000_000).Bytes())...)   // uint256 price
	want = append(want, word([]byte{8})...)                         // int8 decimals
	want = append(want, word(big.NewInt(1_700_000_000).Bytes())...) // uint64 observedAt
	want = append(want, r.InstructionID.Bytes()...)                 // bytes32 instructionId
	want = append(want, r.EndpointsHash.Bytes()...)                 // bytes32 endpointsHash
	want = append(want, r.AdminsHash.Bytes()...)                    // bytes32 adminsHash

	got, err := EncodeBody(r)
	if err != nil {
		t.Fatalf("EncodeBody: %v", err)
	}

	if len(got) != BodyLength {
		t.Fatalf("body length %d, want %d", len(got), BodyLength)
	}
	if !bytes.Equal(got, want) {
		for i := 0; i < 7; i++ {
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
	if got.EndpointsHash != r.EndpointsHash {
		t.Errorf("endpointsHash = %s, want %s", got.EndpointsHash.Hex(), r.EndpointsHash.Hex())
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
		{"nil price", func(r *PriceReport) { r.Price = nil }},
		{"negative price", func(r *PriceReport) { r.Price = big.NewInt(-1) }},
		{"negative decimals", func(r *PriceReport) { r.Decimals = -1 }},
		{"zero observedAt is the never-seen sentinel", func(r *PriceReport) { r.ObservedAt = 0 }},
		{"zero endpointsHash is the no-config sentinel", func(r *PriceReport) { r.EndpointsHash = common.Hash{} }},
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
