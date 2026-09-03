package envelope

import (
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// The admins sign a commitment to the ciphertext, not the ciphertext itself, so
// a payload delivered alongside a body it does not hash to must never decode:
// nothing downstream may be handed a payload the signatures did not cover.
func TestDecodeRejectsAPayloadThatDoesNotMatchTheSignedHash(t *testing.T) {
	e := Envelope{
		ChainID: big.NewInt(114), TeeID: common.Address{1}, Version: 1, IssuedAt: 1,
		Ref: "hex_api_key", Payload: []byte("what the admins committed to"),
	}
	body, err := EncodeBody(e)
	if err != nil {
		t.Fatal(err)
	}

	swapped, err := outerArgs.Pack(body, [][]byte{}, []byte("what was actually delivered"))
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decode(swapped); err == nil {
		t.Fatal("a payload that does not hash to the signed commitment decoded")
	}

	empty, err := outerArgs.Pack(body, [][]byte{}, []byte{})
	if err != nil {
		t.Fatal(err)
	}
	if _, _, err := decode(empty); err == nil {
		t.Fatal("an empty payload decoded")
	}

	genuine, err := outerArgs.Pack(body, [][]byte{}, e.Payload)
	if err != nil {
		t.Fatal(err)
	}
	got, _, err := decode(genuine)
	if err != nil {
		t.Fatalf("the genuine payload must decode: %v", err)
	}
	if string(got.Payload) != string(e.Payload) {
		t.Fatalf("decoded payload %q, want %q", got.Payload, e.Payload)
	}
}

// An envelope without a payload has nothing for the admins to commit to.
func TestEncodeBodyRequiresAPayload(t *testing.T) {
	if _, err := EncodeBody(Envelope{ChainID: big.NewInt(114), Ref: "hex_api_key"}); err == nil {
		t.Fatal("EncodeBody accepted an envelope with no payload")
	}
}
