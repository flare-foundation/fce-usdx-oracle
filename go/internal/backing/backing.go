// Package backing reads USDX's reserve figures from Hex Trust's HTTP APIs.
//
// # Why this is an interface
//
// Spec §3.0 records an unresolved blocker: the public endpoint's
// current_reserves_amount has been observed to equal total on-chain supply to
// the cent, and to track it through a 1,000,000 USDX burn. If that is because
// the figure is derived from chain data rather than measured from assets, then
// backing/supply is 1.000000 by construction and the feed can never report the
// depeg it exists to detect.
//
// If Hex's answer forces a different data source — mark-to-market bond
// valuations, an independent custody attestation — the fix is a new Source
// implementation. Everything downstream (supply reads, the price formula,
// signing, the payload, the contracts) is unaffected. That containment is the
// reason this package exists as an interface rather than as calls inlined into
// the observation path.
//
// # Disclosure
//
// Every value read here is potentially confidential to Hex (spec §4.1), and
// none of it may reach ActionResult.Log, GET /state, or the on-chain payload.
// Errors returned from this package are therefore failure.Failure values from a
// closed vocabulary: no URL, no response body, and above all no reserve figure.
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

// maxBodyBytes caps how much of a response is read. The real payloads are ~150
// bytes; this bounds an upstream that starts streaming.
const maxBodyBytes = 64 << 10

// attempts is the total number of tries per fetch, including the first.
// Deliberately small: the observation has a global deadline, and a source that
// needs three attempts is a source to report as unhealthy rather than to wait
// on.
const attempts = 2

// Reading is one observed backing figure.
type Reading struct {
	// Amount is the exact value reported by the source.
	Amount *big.Rat

	// AsOf is the source's own last_update: when Hex says the figure was
	// computed, not when we read it. The gap between this and the supply reads
	// is the TOCTOU exposure in spec §6.1.
	AsOf time.Time

	// RetrievedAt is when the response was received.
	RetrievedAt time.Time
}

// Source is one backing figure provider.
type Source interface {
	// Name is a stable identifier used for GET /state health reporting. It must
	// never be a URL: /state is public, and endpoint paths and credentials are
	// not (spec §4.1).
	Name() string

	// Fetch returns the current reading. The error is always a
	// failure.Failure, safe to surface publicly.
	Fetch(ctx context.Context) (Reading, error)
}

// Config describes one HTTP backing endpoint.
type Config struct {
	// Name is the health-reporting identifier, e.g. "hex_bonds".
	Name string

	// URL is the endpoint. Must be https.
	URL string

	// AuthHeader and AuthValue are sent when both are non-empty. The private
	// endpoint's scheme is unconfirmed (spec WP1 OPEN: it returns 404 rather
	// than 401 without credentials, so bearer vs X-API-Key cannot be
	// distinguished until a key is issued).
	AuthHeader string
	AuthValue  string

	// RequireCurrency asserts the response's currency field equals "USD".
	//
	// This MUST be false for the public endpoint. Measured 31 Jul 2026, the two
	// endpoints return different shapes: the public one omits currency
	// entirely. A single shared struct that asserts currency == "USD" would
	// fail closed forever against it — the bug this flag exists to prevent.
	RequireCurrency bool

	// MaxAge rejects a reading whose last_update is older than this. Zero
	// disables the check. The public endpoint refreshes on a ~10 minute tick,
	// so anything below that is noise.
	MaxAge time.Duration

	// Timeout bounds a single attempt.
	Timeout time.Duration
}

// httpSource is the HTTP implementation of Source.
type httpSource struct {
	cfg    Config
	client *http.Client
}

// New builds a Source from cfg.
//
// The returned error is an ordinary error, not a failure.Failure: a bad config
// is a startup fault that must stop the process, not a runtime condition to be
// reported through the price feed.
func New(cfg Config) (Source, error) {
	if cfg.Name == "" {
		return nil, errors.New("backing: source name is required")
	}
	if strings.Contains(cfg.Name, "://") || strings.Contains(cfg.Name, ".") {
		// A name reaches GET /state, which is public. A URL-shaped name would
		// disclose the endpoint, and with it any key embedded in a query string.
		return nil, errors.New("backing: source name must be an identifier, not a URL")
	}

	u, err := url.Parse(cfg.URL)
	if err != nil {
		return nil, errors.New("backing: source URL is not a valid URL")
	}
	if u.Scheme != "https" {
		// Plaintext would expose the API key on the wire and let anyone on path
		// dictate the reserve figure, and therefore the published price.
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
			// Never follow redirects.
			//
			// Go strips Authorization on a cross-host redirect, but it does NOT
			// strip custom headers — and the private endpoint may well use
			// X-API-Key (WP1 OPEN). A compromised or misconfigured upstream
			// could then 302 to a host of its choosing and be handed the key.
			// An API client has no legitimate need to follow redirects, so the
			// safe behaviour is also the simple one.
			CheckRedirect: func(*http.Request, []*http.Request) error {
				return http.ErrUseLastResponse
			},
		},
	}, nil
}

func (s *httpSource) Name() string { return s.cfg.Name }

// response is the union of both endpoints' shapes.
//
// Currency is a pointer so "absent" and "present but empty" stay
// distinguishable — the public endpoint omits it, and RequireCurrency decides
// whether that is acceptable.
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
			// Give a transient fault a moment, but never outlast the caller's
			// deadline: the observation window is what bounds TOCTOU exposure.
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

		// Only transient faults are worth a second attempt. A malformed body or
		// a rejected credential will be malformed and rejected again.
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
		// included. It is deliberately dropped rather than wrapped.
		if ctx.Err() != nil || isTimeout(err) {
			return Reading{}, failure.BackingTimeout
		}
		return Reading{}, failure.BackingUnavailable
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		// 5xx and 429 may pass; 4xx will not.
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

// parse validates and converts a response body. Split out from the transport so
// every validation rule is testable without a network.
func (s *httpSource) parse(body []byte) (Reading, error) {
	var r response
	dec := json.NewDecoder(strings.NewReader(string(body)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&r); err != nil {
		// Unknown fields are rejected rather than ignored: a shape change from
		// Hex is a signal to stop and look, not to keep publishing prices from
		// a response we no longer fully understand.
		return Reading{}, failure.BackingMalformed
	}

	if r.Value == "" {
		return Reading{}, failure.BackingMalformed
	}

	// Per-endpoint currency handling. See Config.RequireCurrency.
	if s.cfg.RequireCurrency {
		if r.Currency == nil || *r.Currency != "USD" {
			return Reading{}, failure.BackingCurrency
		}
	} else if r.Currency != nil && *r.Currency != "USD" {
		// If it is present it must still be USD; only absence is tolerated.
		return Reading{}, failure.BackingCurrency
	}

	// Both formats are accepted on both endpoints regardless of which one is
	// documented to produce which: WP1 leaves it open whether the difference is
	// deliberate, and a format flip must not take the feed down.
	amount, err := decimal.ParseAmount(r.Value)
	if err != nil {
		// err quotes nothing, but is dropped anyway — the input is the reserve
		// figure.
		return Reading{}, failure.BackingMalformed
	}

	asOf, err := time.Parse(time.RFC3339, r.LastUpdate)
	if err != nil {
		return Reading{}, failure.BackingMalformed
	}

	now := time.Now().UTC()
	if s.cfg.MaxAge > 0 && now.Sub(asOf) > s.cfg.MaxAge {
		return Reading{}, failure.BackingStale
	}
	// A figure stamped in the future means clock skew or a broken upstream.
	// Either way the freshness check is meaningless, so refuse it.
	if asOf.After(now.Add(time.Minute)) {
		return Reading{}, failure.BackingStale
	}

	return Reading{Amount: amount, AsOf: asOf, RetrievedAt: now}, nil
}
