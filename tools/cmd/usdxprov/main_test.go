package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/crypto/ecies"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
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
	if pub.X.Cmp(key.X) != 0 || pub.Y.Cmp(key.Y) != 0 {
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
		"-url", srv.URL, "-chain-id", "114", "-ref", protocol.HexAPIKeyRef,
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
		"-url", srv.URL, "-chain-id", "114", "-ref", protocol.HexAPIKeyRef,
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

const (
	testCashURL  = "https://api.example/restricted/cash"
	testCashSalt = "0x1111111111111111111111111111111111111111111111111111111111111111"
)

// The cash URL is provisioned as abi.encode(salt, url), never as a string, so
// build derives it from HEX_CASH_URL and HEX_CASH_SALT and must refuse a value
// given for it: a URL pasted into USDXPROV_VALUE would otherwise be encrypted as
// a string the enclave then rejects against its commitment.
func TestBuildRefusesAValueForTheCashURL(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(key.PublicKey.X).Hex(),
		"y": common.BigToHash(key.PublicKey.Y).Hex(),
	})
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "envelope.json")
	t.Setenv("USDXPROV_VALUE", testCashURL)
	t.Setenv("HEX_CASH_URL", testCashURL)
	t.Setenv("HEX_CASH_SALT", testCashSalt)

	err = build([]string{
		"-url", srv.URL, "-chain-id", "114", "-ref", protocol.HexCashURLRef, "-out", out,
	})
	if err == nil {
		t.Fatal("build accepted a string value for the cash URL")
	}
	if _, statErr := os.Stat(out); !os.IsNotExist(statErr) {
		t.Error("an envelope was written despite the refusal")
	}
}

// What build encrypts for hex_cash_url must be byte-identical to the preimage
// the store recomputes the commitment from, and that commitment must be the one
// commit prints for the endpoints configuration; otherwise the machine refuses
// the URL it is handed.
func TestBuildEncryptsTheCashURLPreimage(t *testing.T) {
	key, err := crypto.GenerateKey()
	if err != nil {
		t.Fatal(err)
	}
	srv := infoServer(t, map[string]string{
		"x": common.BigToHash(key.PublicKey.X).Hex(),
		"y": common.BigToHash(key.PublicKey.Y).Hex(),
	})
	defer srv.Close()

	out := filepath.Join(t.TempDir(), "envelope.json")
	t.Setenv("USDXPROV_VALUE", "")
	t.Setenv("HEX_CASH_URL", testCashURL)
	t.Setenv("HEX_CASH_SALT", testCashSalt)

	err = build([]string{
		"-url", srv.URL, "-chain-id", "114", "-ref", protocol.HexCashURLRef,
		"-tee", crypto.PubkeyToAddress(key.PublicKey).Hex(), "-out", out,
	})
	if err != nil {
		t.Fatalf("build refused the cash URL: %v", err)
	}

	var f envelopeFile
	if err := readJSON(out, &f); err != nil {
		t.Fatalf("reading the envelope: %v", err)
	}
	ciphertext, err := hex.DecodeString(strings.TrimPrefix(f.Ciphertext, "0x"))
	if err != nil {
		t.Fatalf("ciphertext is not hex: %v", err)
	}
	plaintext, err := ecies.ImportECDSA(key).Decrypt(ciphertext, nil, nil)
	if err != nil {
		t.Fatalf("the envelope does not decrypt with the served key: %v", err)
	}

	u := configpayload.PrivateURL{Salt: common.HexToHash(testCashSalt), Url: testCashURL}
	want, err := configpayload.EncodePrivateURL(u)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(plaintext, want) {
		t.Fatalf("provisioned blob differs from the store's preimage:\n got %x\nwant %x", plaintext, want)
	}
	commitment, err := configpayload.CommitPrivateURL(u)
	if err != nil {
		t.Fatal(err)
	}
	if commitment != crypto.Keccak256Hash(plaintext) {
		t.Fatalf("commitment %s is not the hash of the provisioned blob", commitment.Hex())
	}
}

// privateURL is the one check the URL and salt pass before either is committed
// on-chain or encrypted, so its refusals are pinned.
func TestPrivateURLValidation(t *testing.T) {
	cases := []struct {
		name, url, salt string
		ok              bool
	}{
		{"accepted", testCashURL, testCashSalt, true},
		{"accepted without 0x", testCashURL, strings.TrimPrefix(testCashSalt, "0x"), true},
		{"missing url", "", testCashSalt, false},
		{"missing salt", testCashURL, "", false},
		{"plain http", "http://api.example/cash", testCashSalt, false},
		{"short salt", testCashURL, "0xabcd", false},
		{"zero salt", testCashURL, "0x" + strings.Repeat("00", 32), false},
		{"salt is not hex", testCashURL, "0x" + strings.Repeat("zz", 32), false},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u, err := privateURL(tc.url, tc.salt)
			if tc.ok != (err == nil) {
				t.Fatalf("ok=%v, err=%v", tc.ok, err)
			}
			if tc.ok && (u.Url != tc.url || u.Salt != common.HexToHash(testCashSalt)) {
				t.Fatalf("parsed %+v", u)
			}
		})
	}
}

// resultServer files a result for id after `after` polls; before that it 404s,
// as the proxy does until tee-node has answered.
func resultServer(t *testing.T, id common.Hash, after int, result teetypes.ActionResult) (*httptest.Server, *int) {
	t.Helper()
	polls := 0
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/action/result/"+id.Hex() || r.URL.Query().Get("submissionTag") != string(teetypes.Submit) {
			t.Errorf("unexpected request %s", r.URL)
		}
		polls++
		if polls < after {
			w.WriteHeader(http.StatusNotFound)
			return
		}
		encodeJSON(t, w, teetypes.ActionResponse{Result: result})
	}))
	return srv, &polls
}

// submit's wait is the machine's own verdict, not the proxy's acceptance: a 404
// until tee-node has answered, then status 1 applied or 0 rejected with the
// extension's reason. Returning early on the 404 is exactly the race that made
// the upload script report a credential missing that landed a moment later.
func TestAwaitResultReportsTheMachinesVerdict(t *testing.T) {
	resultPollInterval = 2 * time.Millisecond
	t.Cleanup(func() { resultPollInterval = time.Second })
	id := common.HexToHash("0x11")

	applied, polls := resultServer(t, id, 3, teetypes.ActionResult{ID: id, Status: 1})
	defer applied.Close()
	if err := awaitResult(applied.URL, id, time.Second); err != nil {
		t.Fatalf("an applied result was reported as: %v", err)
	}
	if *polls < 3 {
		t.Fatalf("returned after %d polls, before the result was filed", *polls)
	}

	rejected, _ := resultServer(t, id, 1, teetypes.ActionResult{ID: id, Status: 0, Log: "provisioning rejected"})
	defer rejected.Close()
	err := awaitResult(rejected.URL, id, time.Second)
	if err == nil || !strings.Contains(err.Error(), "provisioning rejected") {
		t.Fatalf("a rejection must surface the machine's reason, got: %v", err)
	}

	never, _ := resultServer(t, id, 1<<30, teetypes.ActionResult{})
	defer never.Close()
	if err := awaitResult(never.URL, id, 20*time.Millisecond); err == nil {
		t.Fatal("a result that never arrives must be reported, not treated as applied")
	}
}

// encodeJSON writes v as a test server's response. The fixtures are the
// package's own types, so a failure is a broken test rather than a case.
func encodeJSON(t *testing.T, w http.ResponseWriter, v any) {
	t.Helper()
	if err := json.NewEncoder(w).Encode(v); err != nil {
		t.Errorf("encoding the response: %v", err)
	}
}
