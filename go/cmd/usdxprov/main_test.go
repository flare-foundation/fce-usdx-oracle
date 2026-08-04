package main

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// The proxy's /info carries no teeId field; the machine identity must be
// derived from the attested publicKey. fetchKey serves that derivation, so a
// regression here silently binds envelopes to the wrong (or zero) machine.
func TestFetchKeyDerivesTeeID(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	want := crypto.PubkeyToAddress(key.PublicKey)

	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(key.PublicKey.X).Hex(),
		"y": common.BigToHash(key.PublicKey.Y).Hex(),
	})
	defer srv.Close()

	pub, teeID, err := fetchKey(srv.URL)
	if err != nil {
		t.Fatalf("fetchKey: %v", err)
	}
	if teeID != want {
		t.Fatalf("teeID = %s, want %s derived from the publicKey", teeID, want)
	}
	if pub.X.Cmp(key.PublicKey.X) != 0 || pub.Y.Cmp(key.PublicKey.Y) != 0 {
		t.Fatal("public key round-trip mismatch")
	}
}

func TestFetchKeyRejectsOffCurvePoint(t *testing.T) {
	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(big.NewInt(1)).Hex(),
		"y": common.BigToHash(big.NewInt(2)).Hex(),
	})
	defer srv.Close()

	if _, _, err := fetchKey(srv.URL); err == nil {
		t.Fatal("expected an error for an off-curve point, got none")
	}
}

// infoServer serves the real /info shape: teeInfo.publicKey as {x, y} hex
// coordinates and nothing identifying the machine by address.
func infoServer(t *testing.T, publicKey map[string]string) *httptest.Server {
	t.Helper()
	payload := map[string]any{
		"teeInfo": map[string]any{
			"challenge": (common.Hash{}).Hex(),
			"publicKey": publicKey,
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		if err := json.NewEncoder(w).Encode(payload); err != nil {
			t.Errorf("encoding payload: %v", err)
		}
	}))
}
