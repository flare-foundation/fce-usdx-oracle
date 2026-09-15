package fccutils

import (
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
)

// infoServer serves the proxy's /info with the given extension state fields, as
// tee-node fills them: state as bytes, stateVersion naming their schema or the
// all-ones sentinel when the extension could not be read.
func infoServer(t *testing.T, state []byte, version common.Hash) *httptest.Server {
	t.Helper()
	payload := map[string]any{
		"teeInfo": map[string]any{
			"state": map[string]any{
				"state":        hexutil.Bytes(state),
				"stateVersion": version,
			},
		},
	}
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/info" {
			t.Errorf("unexpected path %s", r.URL.Path)
		}
		encodeJSON(t, w, payload)
	}))
}

// Every tool reads the extension's state through this one function, so the
// three things /info can say — here it is, this tee-node does not carry it,
// tee-node could not read it — must come back as three different answers.
func TestReadExtensionState(t *testing.T) {
	stateJSON := []byte(`{"readiness":{"ready":true},"sourceHealth":{"hex_cash":"ok"}}`)
	version := common.HexToHash("0x302e312e30")

	carried := infoServer(t, stateJSON, version)
	defer carried.Close()
	raw, err := ReadExtensionState(carried.URL)
	if err != nil {
		t.Fatalf("a carried state was refused: %v", err)
	}
	var got struct {
		Readiness struct {
			Ready bool `json:"ready"`
		} `json:"readiness"`
		SourceHealth map[string]string `json:"sourceHealth"`
	}
	if err := json.Unmarshal(raw, &got); err != nil || !got.Readiness.Ready || got.SourceHealth["hex_cash"] != "ok" {
		t.Fatalf("state came back as %s (%v)", raw, err)
	}

	empty := infoServer(t, nil, common.Hash{})
	defer empty.Close()
	if _, err := ReadExtensionState(empty.URL); !errors.Is(err, ErrNoAttestedState) {
		t.Fatalf("an empty field must read as ErrNoAttestedState, got %v", err)
	}

	unavailable := infoServer(t, nil, common.MaxHash)
	defer unavailable.Close()
	if _, err := ReadExtensionState(unavailable.URL); !errors.Is(err, ErrStateUnavailable) {
		t.Fatalf("the all-ones version must read as ErrStateUnavailable, got %v", err)
	}

	garbage := infoServer(t, []byte("not json"), version)
	defer garbage.Close()
	if _, err := ReadExtensionState(garbage.URL); err == nil {
		t.Fatal("bytes that are not JSON must be refused")
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
