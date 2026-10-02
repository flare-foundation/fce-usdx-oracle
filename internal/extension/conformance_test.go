package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"math/big"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/observation"
	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/pkg/wire/feedupdate"

	"github.com/ethereum/go-ethereum/common"
)

// The conformance fixtures are the normative wire contract from
// docs/extension-contract.md, replayed in order against one extension. They
// assert the ENVELOPE — routing, status codes, the ActionResult field set, and
// the exact body of GET /state — never the price.
//
// They run in index.json's order against one extension, which keeps no state a
// request can change, so each fixture stands on its own.
const fixtureDir = "../../deploy/testdata/conformance"

// The observation every GET_FEED fixture is answered with. Fixed so the replay
// is deterministic.
const (
	conformanceValue      = int32(100_000_000)
	conformanceDecimals   = int8(8)
	conformanceObservedAt = uint64(1_700_000_000)
)

type fixture struct {
	Name    string `json:"name"`
	Request struct {
		Method  string          `json:"method"`
		Path    string          `json:"path"`
		Body    json.RawMessage `json:"body"`
		RawBody *string         `json:"raw_body"`
	} `json:"request"`
	Expect struct {
		Status       int             `json:"status"`
		JSON         json.RawMessage `json:"json"`
		JSONSubset   json.RawMessage `json:"json_subset"`
		TextContains string          `json:"text_contains"`
		LogPrefix    string          `json:"log_prefix"`
	} `json:"expect"`
}

// conformanceObserver answers with one fixed, already-signed observation. A
// real payload rather than a placeholder, so the fixtures pin the bytes
// production actually emits.
type conformanceObserver struct{ data []byte }

func (o *conformanceObserver) Report(context.Context, uint64) (observation.Observation, error) {
	return observation.Observation{
		Value:      conformanceValue,
		Decimals:   conformanceDecimals,
		ObservedAt: conformanceObservedAt,
		Binding: observation.Binding{
			ExtensionID:   config.ExtensionID(),
			FeedID:        protocol.FeedID,
			EndpointsHash: common.HexToHash("0x9f3c"),
			AdminsHash:    common.HexToHash("0x5e21"),
		},
		Data: o.data,
	}, nil
}

func (o *conformanceObserver) ReadinessReport(
	context.Context, common.Hash, types.Readiness,
) ([]byte, error) {
	return []byte{0x01}, nil
}

// conformancePayload is the ActionResult data a successful GET_FEED returns:
// abi.encode(FeedUpdate, Signature) over the fixed observation. The signature
// is a well-formed constant — the store verifies signatures, these fixtures
// verify the envelope carrying them.
func conformancePayload(t *testing.T) []byte {
	t.Helper()

	sig := make([]byte, feedupdate.SignatureLength)
	for i := range sig {
		sig[i] = byte(i + 1)
	}
	sig[64] = 27

	data, err := feedupdate.EncodeData(feedupdate.FeedUpdate{
		ExtensionID:   config.ExtensionID(),
		FeedID:        protocol.FeedID,
		Value:         conformanceValue,
		Decimals:      conformanceDecimals,
		ObservedAt:    conformanceObservedAt,
		EndpointsHash: common.HexToHash("0x9f3c"),
		AdminsHash:    common.HexToHash("0x5e21"),
	}, sig)
	if err != nil {
		t.Fatalf("building the conformance payload: %v", err)
	}
	return data
}

func TestConformance(t *testing.T) {
	var index struct {
		Fixtures []string `json:"fixtures"`
	}
	readJSON(t, filepath.Join(fixtureDir, "index.json"), &index)
	if len(index.Fixtures) == 0 {
		t.Fatal("index.json lists no fixtures")
	}

	e := NewExtension(0, 0, &conformanceObserver{data: conformancePayload(t)}, nil)

	for _, name := range index.Fixtures {
		var f fixture
		readJSON(t, filepath.Join(fixtureDir, name+".json"), &f)

		// Sequential, in index.json's order, against the one extension.
		t.Run(name, func(t *testing.T) { replay(t, e, f) })
	}
}

func replay(t *testing.T, e *Extension, f fixture) {
	t.Helper()

	var body []byte
	switch {
	case f.Request.RawBody != nil:
		body = []byte(*f.Request.RawBody)
	case len(f.Request.Body) > 0:
		body = f.Request.Body
	}

	req := httptest.NewRequest(f.Request.Method, f.Request.Path, bytes.NewReader(body))
	rec := httptest.NewRecorder()
	e.Server.Handler.ServeHTTP(rec, req)

	if rec.Code != f.Expect.Status {
		t.Fatalf("status %d, want %d (body %q)", rec.Code, f.Expect.Status, rec.Body.String())
	}

	got := rec.Body.Bytes()

	if f.Expect.TextContains != "" && !strings.Contains(string(got), f.Expect.TextContains) {
		t.Errorf("body %q does not contain %q", got, f.Expect.TextContains)
	}

	if len(f.Expect.JSON) > 0 {
		var want, have any
		mustUnmarshal(t, f.Expect.JSON, &want)
		mustUnmarshal(t, got, &have)
		if !reflect.DeepEqual(want, have) {
			t.Errorf("body does not match the fixture exactly\n got: %s\nwant: %s",
				compact(t, got), compact(t, f.Expect.JSON))
		}
	}

	if len(f.Expect.JSONSubset) > 0 {
		var want, have map[string]any
		mustUnmarshal(t, f.Expect.JSONSubset, &want)
		mustUnmarshal(t, got, &have)
		for k, v := range want {
			if !reflect.DeepEqual(have[k], v) {
				t.Errorf("field %q = %v, want %v", k, have[k], v)
			}
		}
	}

	if f.Expect.LogPrefix != "" {
		var res struct {
			Log string `json:"log"`
		}
		mustUnmarshal(t, got, &res)
		if !strings.HasPrefix(res.Log, f.Expect.LogPrefix) {
			t.Errorf("log %q does not start with %q", res.Log, f.Expect.LogPrefix)
		}
	}
}

func readJSON(t *testing.T, path string, dest any) {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}
	mustUnmarshal(t, raw, dest)
}

func mustUnmarshal(t *testing.T, raw []byte, dest any) {
	t.Helper()
	if err := json.Unmarshal(raw, dest); err != nil {
		t.Fatalf("decoding %s: %v", raw, err)
	}
}

func compact(t *testing.T, raw []byte) string {
	t.Helper()
	var buf bytes.Buffer
	if err := json.Compact(&buf, raw); err != nil {
		return string(raw)
	}
	return buf.String()
}

var _ = big.NewInt
var _ = http.MethodPost
