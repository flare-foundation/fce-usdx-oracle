package main

import (
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fce-usdx-oracle/internal/config"

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

// Pins finding 25. verify-tee and this command make separate requests, so an endpoint
// can serve an honest key to the check and a substituted one to the encryption. -tee
// is an ASSERTION, never an override: overriding would encrypt to whoever answered.
func TestBuildRefusesAKeyThatIsNotTheAttestedMachine(t *testing.T) {
	substituted, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	// The proxy serves the substituted key; -tee names what the attestation
	// actually derived, so the two disagree.
	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(substituted.PublicKey.X).Hex(),
		"y": common.BigToHash(substituted.PublicKey.Y).Hex(),
	})
	defer srv.Close()

	attested, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}

	out := filepath.Join(t.TempDir(), "envelope.json")
	t.Setenv("USDXPROV_VALUE", "a-real-credential")

	err = build([]string{
		"-url", srv.URL, "-chain-id", "114", "-ref", config.HexAPIKeyRef,
		"-tee", crypto.PubkeyToAddress(attested.PublicKey).Hex(), "-out", out,
	})
	if err == nil {
		t.Fatal("build accepted a key deriving to a machine other than the attested one")
	}

	// The refusal has to happen BEFORE encryption. An envelope on disk would
	// mean the ciphertext exists, and it is readable by the holder of the
	// substituted key.
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("an envelope was written despite the key mismatch")
	}
}

// The other half: a -tee that agrees with the served key is the normal path and
// must not be broken by the check above.
func TestBuildAcceptsTheAttestedMachine(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	teeID := crypto.PubkeyToAddress(key.PublicKey)

	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(key.PublicKey.X).Hex(),
		"y": common.BigToHash(key.PublicKey.Y).Hex(),
	})
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "envelope.json")
	t.Setenv("USDXPROV_VALUE", "a-real-credential")

	err = build([]string{
		"-url", srv.URL, "-chain-id", "114", "-ref", config.HexAPIKeyRef,
		"-tee", teeID.Hex(), "-out", out,
	})
	if err != nil {
		t.Fatalf("build refused the attested machine: %v", err)
	}

	var f envelopeFile
	if err := readJSON(out, &f); err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	if common.HexToAddress(f.TeeID) != teeID {
		t.Errorf("envelope teeId = %s, want %s", f.TeeID, teeID.Hex())
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
