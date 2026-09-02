// Package backing reads USDX's reserve figures from Hex Trust's HTTP APIs.
//
// A Source is an interface so a change of reserve methodology is a new
// implementation and nothing downstream moves.
//
// Every value read here is potentially confidential to Hex, so errors are
// failure.Failure values from a closed vocabulary: no URL, no response body,
// and above all no reserve figure.
package backing

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"math/big"
	"net/http"
	"net/url"
	"strings"
	"time"

	"fce-usdx-oracle/internal/decimal"
	"fce-usdx-oracle/internal/failure"
)

// maxBodyBytes bounds an upstream that starts streaming; real payloads are ~150 bytes.
const maxBodyBytes = 64 << 10

// attempts is the total tries per fetch. Small on purpose: a source that needs
// three attempts is one to report as unhealthy rather than wait on.
const attempts = 2

type Reading struct {
	Amount *big.Rat

	// The source's own last_update: when Hex says the figure last CHANGED, not
	// when we read it and not a heartbeat. Hex republishes within ~10 minutes of
	// an on-chain event — a mint or a redemption — and not otherwise, so a quiet
	// day legitimately leaves this hours old with the figure still correct.
	// Recorded for operators; deliberately not a freshness bound, see Config.
	AsOf time.Time

	RetrievedAt time.Time
}

type Source interface {
	// Name must never be a URL: /state is public, endpoint paths are not.
	Name() string

	// Fetch returns the current reading. The error is always a failure.Failure.
	Fetch(ctx context.Context) (Reading, error)
}

type Config struct {
	Name string
	URL  string

	// Sent when both are non-empty.
	AuthHeader string
	AuthValue  string

	// MUST be false for the public endpoint, which omits currency entirely — a
	// shared struct asserting currency == "USD" would fail closed forever.
	RequireCurrency bool

	// There is deliberately no MaxAge. Both endpoints are event-driven, so age
	// carries no information about correctness — the rationale is in
	// config.BondsSource. A timestamp is still parsed and still rejected if it
	// is in the future, which is a broken upstream rather than an old figure.

	// Bounds a single attempt.
	Timeout time.Duration
}

type httpSource struct {
	cfg    Config
	client *http.Client
}

// New builds a Source from cfg. The error is an ordinary error, not a
// failure.Failure: a bad config is a startup fault that must stop the process.
func New(cfg Config) (Source, error) {
	if cfg.Name == "" {
		return nil, errors.New("backing: source name is required")
	}
	if strings.Contains(cfg.Name, "://") || strings.Contains(cfg.Name, ".") {
		// A URL-shaped name would disclose the endpoint on GET /state, and with
		// it any key embedded in a query string.
		return nil, errors.New("backing: source name must be an identifier, not a URL")
	}

	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.New("backing: source URL is not a valid URL")
	}
	if u.Scheme != "https" {
		return nil, errors.New("backing: source URL must use https")
	}
	if (cfg.AuthHeader == "") != (cfg.AuthValue == "") {
		return nil, errors.New("backing: auth header and value must be set together")
	}
	if cfg.Timeout <= 0 {
		return nil, errors.New("backing: timeout must be positive")
	}

	return &httpSource{
		cfg: cfg,
		client: &http.Client{
			Timeout: cfg.Timeout,
			// Never follow redirects: Go strips Authorization across hosts but
			// not custom headers, so a 302 could hand X-API-Key to any host.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *httpSource) Name() string { return s.cfg.Name }

// response is the union of both endpoints' shapes. Currency is a pointer so
// "absent" and "present but empty" stay distinguishable.
type response struct {
	MetricName string  `json:"metric_name"`
	Value      string  `json:"value"`
	Currency   *string `json:"currency"`
	LastUpdate string  `json:"last_update"`
}

func (s *httpSource) Fetch(ctx context.Context) (Reading, error) {
	var lastErr error
	for attempt := range attempts {
		if attempt > 0 {
			select {
			case <-ctx.Done():
				return Reading{}, failure.BackingTimeout
			case <-time.After(250 * time.Millisecond):
			}
		}

		reading, err := s.fetchOnce(ctx)
		if err == nil {
			return reading, nil
		}
		lastErr = err

		// A malformed body or a rejected credential will be malformed and
		// rejected again.
		if !isRetryable(err) {
			return Reading{}, err
		}
	}
	return Reading{}, lastErr
}

func isRetryable(err error) bool {
	return errors.Is(err, failure.BackingUnavailable) || errors.Is(err, failure.BackingTimeout)
}

func (s *httpSource) fetchOnce(ctx context.Context) (Reading, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, s.cfg.URL, nil)
	if err != nil {
		return Reading{}, failure.Internal
	}
	req.Header.Set("Accept", "application/json")
	if s.cfg.AuthHeader != "" {
		req.Header.Set(s.cfg.AuthHeader, s.cfg.AuthValue)
	}

	resp, err := s.client.Do(req)
	if err != nil {
		// The underlying *url.Error embeds the full request URL, credentials
		// included. Dropped rather than wrapped.
		if ctx.Err() != nil || isTimeout(err) {
			return Reading{}, failure.BackingTimeout
		}
		return Reading{}, failure.BackingUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		if resp.StatusCode >= 500 || resp.StatusCode == http.StatusTooManyRequests {
			return Reading{}, failure.BackingUnavailable
		}
		return Reading{}, failure.BackingMalformed
	}

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxBodyBytes))
	if err != nil {
		return Reading{}, failure.BackingUnavailable
	}

	return s.parse(body)
}

func isTimeout(err error) bool {
	var netErr interface{ Timeout() bool }
	return errors.As(err, &netErr) && netErr.Timeout()
}

// parse turns a response body into a Reading, rejecting an unrecognised shape,
// a non-USD figure, an unparseable amount or a timestamp from the future. An
// OLD timestamp is not a rejection reason — see Config.
func (s *httpSource) parse(body []byte) (Reading, error) {
	var r response
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		// Unknown fields are rejected rather than ignored: a shape change from
		// Hex is a signal to stop and look.
		return Reading{}, failure.BackingMalformed
	}

	if r.Value == "" {
		return Reading{}, failure.BackingMalformed
	}

	if s.cfg.RequireCurrency {
		if r.Currency == nil || *r.Currency != "USD" {
			return Reading{}, failure.BackingCurrency
		}
	} else if r.Currency != nil && *r.Currency != "USD" {
		// Present it must be USD; only absence is tolerated.
		return Reading{}, failure.BackingCurrency
	}

	amount, err := decimal.ParseAmount(r.Value)
	if err != nil {
		// err is dropped: the input is the reserve figure.
		return Reading{}, failure.BackingMalformed
	}

	asOf, err := time.Parse(time.RFC3339, r.LastUpdate)
	if err != nil {
		return Reading{}, failure.BackingMalformed
	}

	// The age of asOf says nothing on its own: it moves only when the figure changes, so a
	// week-old stamp on a week without a mint is the correct answer. A stamp in
	// the FUTURE is different — it means clock skew or a broken upstream, and
	// nothing else the response says can be trusted either.
	now := time.Now().UTC()
	if asOf.After(now.Add(time.Minute)) {
		return Reading{}, failure.BackingTimestamp
	}

	return Reading{Amount: amount, AsOf: asOf, RetrievedAt: now}, nil
}
