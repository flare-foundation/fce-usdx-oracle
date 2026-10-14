package keeper

import (
	"context"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
	"io"
	"net/http"
	"time"
	"unicode/utf8"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
)

// HexBondsURL is the public Hex endpoint the extension reads its bonds figure
// from: internal/config.HexBondsURL in the extension module, compiled into the
// attested image. Repeated here because the tooling imports no internal
// package; the two must name the same URL for the bonds log to record what the
// machines read.
const HexBondsURL = "https://api.htdigitalassets.com/alm-stablecoin-db/metrics/current_reserves_amount"

// bondsFetchTimeout bounds one fetch. The cycle waits for it, so it is kept
// short; a slow endpoint is recorded as an error rather than delaying the
// request the machines are waiting on.
const bondsFetchTimeout = 15 * time.Second

// maxBondsBody bounds what one record keeps of a response. The endpoint answers
// with about a hundred bytes, so anything near this size is not the metric, and
// the log must not grow with it.
const maxBondsBody = 64 << 10

// BondsRecord is one fetch of the public bonds endpoint, as the keeper saw it.
// The machines read the same endpoint from inside the enclave and keep nothing
// of what they read, and Hex serves only the current figure, so these records
// are the only history of it.
type BondsRecord struct {
	KeeperID string `json:"keeperId"`
	URL      string `json:"url"`

	// RequestedAt is the keeper's clock when the request was sent.
	RequestedAt time.Time `json:"requestedAt"`
	DurationMS  int64     `json:"durationMs"`

	// Status is the HTTP status code; zero when no response arrived.
	Status int `json:"status,omitempty"`

	// Response is the body when it parses as JSON, for reading with jq. It is
	// not byte-exact: the encoder compacts it.
	Response json.RawMessage `json:"response,omitempty"`

	// Raw is the body exactly as received, as a JSON string, which round-trips
	// any valid UTF-8 byte for byte. A body that is not valid UTF-8 would be
	// altered by that encoding, so it goes to RawBase64 instead. RawSHA256 is
	// the digest of the bytes either field reproduces, so a reader can confirm
	// the record holds what the endpoint sent.
	Raw       string `json:"raw,omitempty"`
	RawBase64 string `json:"rawBase64,omitempty"`
	RawSHA256 string `json:"rawSha256,omitempty"`
	Truncated bool   `json:"truncated,omitempty"`

	// Error is why no complete response was recorded.
	Error string `json:"error,omitempty"`
}

// BondsLog fetches the public bonds endpoint once per cycle and appends what
// came back to a JSON-lines file. It is evidence, not an input: nothing the
// cycle decides depends on it, and a failed fetch is recorded, never raised.
type BondsLog struct {
	url      string
	keeperID string
	client   *http.Client
	enc      *json.Encoder
}

// NewBondsLog writes one JSON object per line to w. HTML escaping is off, so
// Raw reads as the endpoint sent it when the file is opened directly.
func NewBondsLog(w io.Writer, url, keeperID string) *BondsLog {
	enc := json.NewEncoder(w)
	enc.SetEscapeHTML(false)
	return &BondsLog{
		url:      url,
		keeperID: keeperID,
		client:   &http.Client{Timeout: bondsFetchTimeout},
		enc:      enc,
	}
}

// Record fetches the endpoint once and appends the outcome. Safe on a nil
// receiver, which is how a keeper without -bondsLog runs.
func (b *BondsLog) Record(ctx context.Context) {
	if b == nil || b.enc == nil {
		return
	}
	if err := b.enc.Encode(b.fetch(ctx)); err != nil {
		logger.Warnf("writing the bonds record: %s", err)
	}
}

func (b *BondsLog) fetch(ctx context.Context) BondsRecord {
	ctx, cancel := context.WithTimeout(ctx, bondsFetchTimeout)
	defer cancel()

	rec := BondsRecord{KeeperID: b.keeperID, URL: b.url, RequestedAt: time.Now().UTC()}
	finish := func(err error) BondsRecord {
		rec.DurationMS = time.Since(rec.RequestedAt).Milliseconds()
		if err != nil {
			rec.Error = err.Error()
		}
		return rec
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, b.url, nil)
	if err != nil {
		return finish(err)
	}
	resp, err := b.client.Do(req)
	if err != nil {
		return finish(err)
	}
	defer func() { _ = resp.Body.Close() }()
	rec.Status = resp.StatusCode

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBondsBody+1))
	if len(body) > maxBondsBody {
		body, rec.Truncated = body[:maxBondsBody], true
	}
	if len(body) > 0 {
		sum := sha256.Sum256(body)
		rec.RawSHA256 = hex.EncodeToString(sum[:])
		if utf8.Valid(body) {
			rec.Raw = string(body)
		} else {
			rec.RawBase64 = base64.StdEncoding.EncodeToString(body)
		}
		if !rec.Truncated && json.Valid(body) {
			rec.Response = json.RawMessage(body)
		}
	}
	return finish(err)
}
