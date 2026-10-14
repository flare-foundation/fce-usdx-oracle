package keeper

import (
	"bytes"
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

func recordOnce(t *testing.T, url string) (BondsRecord, string) {
	t.Helper()
	var out bytes.Buffer
	NewBondsLog(&out, url, "keeper-a").Record(context.Background())

	line := out.String()
	if strings.Count(line, "\n") != 1 || !strings.HasSuffix(line, "\n") {
		t.Fatalf("want exactly one JSON line, got %q", line)
	}
	var rec BondsRecord
	if err := json.Unmarshal([]byte(line), &rec); err != nil {
		t.Fatalf("the record is not JSON: %v", err)
	}
	return rec, line
}

func sha256Hex(b []byte) string {
	sum := sha256.Sum256(b)
	return hex.EncodeToString(sum[:])
}

// The answer is kept byte for byte, so the log shows exactly what the machines
// would have parsed — including a figure the parser would accept but that is
// wrong — with no question of whether recording it changed it.
func TestBondsLogKeepsTheAnswerByteForByte(t *testing.T) {
	const body = "{\n  \"last_update\": \"2026-10-12T14:31:59Z\",\n  \"metric_name\": \"current_reserves_amount\",\n  \"value\": \"6,242,018.38\", \"note\": \"<b>a & b</b>\"\n}\n"
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(body))
	}))
	defer srv.Close()

	rec, line := recordOnce(t, srv.URL)
	if rec.Status != http.StatusOK || rec.Error != "" {
		t.Errorf("status = %d, error = %q; want a clean 200", rec.Status, rec.Error)
	}
	if rec.KeeperID != "keeper-a" || rec.URL != srv.URL || rec.RequestedAt.IsZero() {
		t.Errorf("record = %+v, want the keeper, the url and the request time", rec)
	}
	if rec.Raw != body {
		t.Errorf("raw = %q, want the body byte for byte: %q", rec.Raw, body)
	}
	if rec.RawSHA256 != sha256Hex([]byte(body)) {
		t.Errorf("rawSha256 = %s, want the digest of the served bytes", rec.RawSHA256)
	}
	var got map[string]string
	if err := json.Unmarshal(rec.Response, &got); err != nil {
		t.Fatalf("response is not the JSON that was served: %v", err)
	}
	if got["value"] != "6,242,018.38" {
		t.Errorf("response value = %q, want the served figure", got["value"])
	}
	// The served body spans lines; the record must still be one line, and read
	// as sent when the file is opened directly.
	if strings.Count(line, "\n") != 1 {
		t.Error("a multi-line answer split the record across lines")
	}
	if !strings.Contains(line, "<b>a & b</b>") {
		t.Error("HTML characters were escaped in the file")
	}
}

// A body that is not valid UTF-8 would be altered by a JSON string, so it is
// kept as base64 instead, still byte for byte.
func TestBondsLogKeepsInvalidUTF8AsBase64(t *testing.T) {
	body := []byte{'{', '"', 'v', '"', ':', '"', 0xff, 0xfe, '"', '}'}
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(body)
	}))
	defer srv.Close()

	rec, _ := recordOnce(t, srv.URL)
	decoded, err := base64.StdEncoding.DecodeString(rec.RawBase64)
	if err != nil || !bytes.Equal(decoded, body) || rec.Raw != "" {
		t.Errorf("rawBase64 = %q (raw %q), want the exact bytes and no raw string", rec.RawBase64, rec.Raw)
	}
	if rec.RawSHA256 != sha256Hex(body) {
		t.Errorf("rawSha256 = %s, want the digest of the served bytes", rec.RawSHA256)
	}
}

// An error page is not JSON; it is kept as received with its status, since an
// outage is part of the history too.
func TestBondsLogKeepsANonJSONAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusBadGateway)
		_, _ = w.Write([]byte("<html>502 Bad Gateway</html>"))
	}))
	defer srv.Close()

	rec, _ := recordOnce(t, srv.URL)
	if rec.Status != http.StatusBadGateway || rec.Raw != "<html>502 Bad Gateway</html>" || rec.Response != nil {
		t.Errorf("record = %+v, want the 502 and its page as received", rec)
	}
}

// A fetch that gets no answer at all is still one line, with the reason.
func TestBondsLogRecordsAFailedFetch(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) {}))
	url := srv.URL
	srv.Close()

	rec, _ := recordOnce(t, url)
	if rec.Error == "" || rec.Status != 0 {
		t.Errorf("record = %+v, want an error and no status", rec)
	}
}

// The endpoint answers with about a hundred bytes; an answer far larger is cut
// at the bound and marked, so the log cannot grow with it.
func TestBondsLogTruncatesAnOversizedAnswer(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write(bytes.Repeat([]byte("x"), maxBondsBody+100))
	}))
	defer srv.Close()

	rec, _ := recordOnce(t, srv.URL)
	if !rec.Truncated || len(rec.Raw) != maxBondsBody || rec.Response != nil {
		t.Errorf("truncated = %t, kept %d bytes; want the first %d bytes, marked", rec.Truncated, len(rec.Raw), maxBondsBody)
	}
}

// A keeper started without -bondsLog has no log, and a cycle must not trip on it.
func TestBondsLogNilRecordsNothing(t *testing.T) {
	var b *BondsLog
	b.Record(context.Background())
}
