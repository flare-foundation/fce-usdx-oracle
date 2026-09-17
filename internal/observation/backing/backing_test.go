package backing

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"fce-usdx-oracle/internal/failure"
)

// The two response shapes Hex actually returns, measured 31 Jul 2026. They are
// NOT the same shape, which is the point of RequireCurrency.
const (
	publicBody  = `{"last_update":"%s","metric_name":"current_reserves_amount","value":"25,452,544.52"}`
	privateBody = `{"metric_name":"current_cash_reserves_amount","value":"205304.47","currency":"USD","last_update":"%s"}`
)

// The metric each endpoint names in its own response.
const (
	publicMetric  = "current_reserves_amount"
	privateMetric = "current_cash_reserves_amount"
)

func nowStamp() string { return time.Now().UTC().Format(time.RFC3339) }

// newTestSource points a Source at a test server. It bypasses New()'s https
// requirement, which httptest cannot satisfy without a certificate; every other
// validation rule still applies.
func newTestSource(t *testing.T, cfg Config, handler http.HandlerFunc) Source {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)

	cfg.URL = srv.URL
	if cfg.Name == "" {
		cfg.Name = "hex_test"
	}
	if cfg.Timeout == 0 {
		cfg.Timeout = 2 * time.Second
	}
	if cfg.MetricName == "" {
		cfg.MetricName = publicMetric
	}

	// Mirror the client New() builds, so the tests exercise the production
	// timeout and redirect policy rather than httptest's defaults.
	client := srv.Client()
	client.Timeout = cfg.Timeout
	client.CheckRedirect = func(*http.Request, []*http.Request) error {
		return http.ErrUseLastResponse
	}
	return &hexSource{cfg: cfg, client: client}
}

func serve(body string) http.HandlerFunc {
	return func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = fmt.Fprint(w, body)
	}
}

func TestFetch_PublicEndpointShape(t *testing.T) {
	s := newTestSource(t, Config{RequireCurrency: false},
		serve(fmt.Sprintf(publicBody, nowStamp())))

	got, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "25452544.52"; got.Amount.FloatString(2) != want {
		t.Errorf("amount = %s, want %s", got.Amount.FloatString(2), want)
	}
}

func TestFetch_PrivateEndpointShape(t *testing.T) {
	s := newTestSource(t, Config{MetricName: privateMetric, RequireCurrency: true},
		serve(fmt.Sprintf(privateBody, nowStamp())))

	got, err := s.Fetch(context.Background())
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if want := "205304.47"; got.Amount.FloatString(2) != want {
		t.Errorf("amount = %s, want %s", got.Amount.FloatString(2), want)
	}
}

// The bug RequireCurrency exists to prevent: a single shared struct asserting
// currency == "USD" would reject the public endpoint on every single call,
// permanently, because it does not send the field at all.
func TestFetch_RequiringCurrencyWouldBreakThePublicEndpoint(t *testing.T) {
	body := fmt.Sprintf(publicBody, nowStamp())

	lenient := newTestSource(t, Config{RequireCurrency: false}, serve(body))
	if _, err := lenient.Fetch(context.Background()); err != nil {
		t.Fatalf("public endpoint must parse without a currency field: %v", err)
	}

	strict := newTestSource(t, Config{RequireCurrency: true}, serve(body))
	if _, err := strict.Fetch(context.Background()); !errors.Is(err, failure.BackingCurrency) {
		t.Fatalf("expected BackingCurrency, got %v", err)
	}
}

// Both value formats must work on both endpoints: WP1 leaves it open whether
// the formatting difference is deliberate, and a flip must not take the feed
// down.
func TestFetch_AcceptsBothValueFormatsOnEitherEndpoint(t *testing.T) {
	cases := []struct{ name, value, want string }{
		{"commas", "25,452,544.52", "25452544.52"},
		{"plain", "25452544.52", "25452544.52"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			body := fmt.Sprintf(
				`{"metric_name":"current_reserves_amount","value":%q,"last_update":%q}`, c.value, nowStamp())
			s := newTestSource(t, Config{}, serve(body))
			got, err := s.Fetch(context.Background())
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if got.Amount.FloatString(2) != c.want {
				t.Errorf("amount = %s, want %s", got.Amount.FloatString(2), c.want)
			}
		})
	}
}

func TestFetch_RejectsMalformed(t *testing.T) {
	cases := []struct {
		name string
		body string
		want failure.Failure
	}{
		{"not json", `not json at all`, failure.BackingMalformed},
		{"empty value", `{"metric_name":"current_reserves_amount","value":"","last_update":"2026-07-31T12:00:00Z"}`, failure.BackingMalformed},
		{"unparseable value", `{"metric_name":"current_reserves_amount","value":"abc","last_update":"2026-07-31T12:00:00Z"}`, failure.BackingMalformed},
		{"locale comma value", `{"metric_name":"current_reserves_amount","value":"1,50","last_update":"2026-07-31T12:00:00Z"}`, failure.BackingMalformed},
		{"negative value", `{"metric_name":"current_reserves_amount","value":"-1","last_update":"2026-07-31T12:00:00Z"}`, failure.BackingMalformed},
		{"bad timestamp", `{"metric_name":"current_reserves_amount","value":"1","last_update":"yesterday"}`, failure.BackingMalformed},
		{"unknown field", `{"metric_name":"current_reserves_amount","value":"1","last_update":"2026-07-31T12:00:00Z","surprise":1}`, failure.BackingMalformed},
		{"wrong currency", `{"metric_name":"current_reserves_amount","value":"1","currency":"EUR","last_update":"2026-07-31T12:00:00Z"}`, failure.BackingCurrency},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := newTestSource(t, Config{}, serve(c.body))
			_, err := s.Fetch(context.Background())
			if !errors.Is(err, c.want) {
				t.Errorf("expected %v, got %v", c.want, err)
			}
		})
	}
}

// Hex serves a family of metrics behind this one response shape, so a sibling —
// a ratio, a count, another currency's figure — parses cleanly and would be
// summed into backing as dollars. The response's own label is what separates them.
func TestFetch_RejectsADifferentMetric(t *testing.T) {
	body := fmt.Sprintf(
		`{"metric_name":"current_collateralisation_ratio","value":"1.13","last_update":%q}`,
		nowStamp())
	s := newTestSource(t, Config{}, serve(body))
	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingMetric) {
		t.Errorf("expected BackingMetric, got %v", err)
	}
}

// The expectation is per source, not a single shared constant: the cash endpoint's
// URL is provisioned rather than compiled in, so this is what keeps it from being
// repointed at another of Hex's metrics.
func TestFetch_MetricIsPinnedPerSource(t *testing.T) {
	s := newTestSource(t, Config{MetricName: privateMetric, RequireCurrency: true},
		serve(fmt.Sprintf(publicBody, nowStamp())))
	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingMetric) {
		t.Errorf("expected BackingMetric, got %v", err)
	}
}

// Decode reads one JSON value and stops. Without an explicit end-of-input check a
// body carrying a second value passes on the strength of the first.
func TestFetch_RejectsTrailingData(t *testing.T) {
	valid := fmt.Sprintf(publicBody, nowStamp())
	for _, trailer := range []string{`garbage`, `{"value":"0"}`, `null`, `[]`} {
		t.Run(trailer, func(t *testing.T) {
			s := newTestSource(t, Config{}, serve(valid+trailer))
			if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingMalformed) {
				t.Errorf("expected BackingMalformed, got %v", err)
			}
		})
	}
}

// Trailing WHITESPACE is not trailing data: a pretty-printed body must still parse.
func TestFetch_AcceptsTrailingWhitespace(t *testing.T) {
	body := fmt.Sprintf(publicBody, nowStamp()) + "\n\t \n"
	s := newTestSource(t, Config{}, serve(body))
	if _, err := s.Fetch(context.Background()); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}

// An unexpected currency must be rejected even when the field is optional.
// Tolerating absence is not the same as tolerating "EUR".
func TestFetch_PresentButWrongCurrencyRejectedEvenWhenOptional(t *testing.T) {
	body := fmt.Sprintf(
		`{"metric_name":"current_reserves_amount","value":"1","currency":"EUR","last_update":%q}`, nowStamp())
	s := newTestSource(t, Config{RequireCurrency: false}, serve(body))
	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingCurrency) {
		t.Errorf("expected BackingCurrency, got %v", err)
	}
}

// An old last_update is the NORMAL case, not a fault: Hex republishes within
// ~10 minutes of an on-chain event and not otherwise, so a week without a mint
// leaves a week-old stamp on a perfectly correct figure. A freshness bound here
// rejected good data at a rate set by USDX activity — hex_cash was `down` for
// most of every hour under a 30-minute bound. This is the guard against
// reintroducing one.
func TestFetch_OldTimestampAccepted(t *testing.T) {
	for _, age := range []time.Duration{2 * time.Hour, 7 * time.Hour, 30 * 24 * time.Hour} {
		t.Run(age.String(), func(t *testing.T) {
			old := time.Now().UTC().Add(-age).Format(time.RFC3339)
			s := newTestSource(t, Config{}, serve(fmt.Sprintf(publicBody, old)))

			r, err := s.Fetch(context.Background())
			if err != nil {
				t.Fatalf("a %s-old figure must be accepted, got %v", age, err)
			}
			if r.Amount == nil || r.Amount.Sign() == 0 {
				t.Error("accepted reading carries no amount")
			}
			if got := r.AsOf.UTC().Format(time.RFC3339); got != old {
				t.Errorf("AsOf = %s, want the source's own stamp %s", got, old)
			}
		})
	}
}

// A future timestamp is a different fault from an old one: it means clock skew
// or a broken upstream, so nothing else in the response can be trusted either.
func TestFetch_FutureTimestampRejected(t *testing.T) {
	future := time.Now().UTC().Add(time.Hour).Format(time.RFC3339)
	s := newTestSource(t, Config{}, serve(fmt.Sprintf(publicBody, future)))

	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingTimestamp) {
		t.Errorf("expected BackingTimestamp, got %v", err)
	}
}

func TestFetch_HTTPErrorsClassified(t *testing.T) {
	cases := []struct {
		code int
		want failure.Failure
	}{
		{http.StatusInternalServerError, failure.BackingUnavailable},
		{http.StatusBadGateway, failure.BackingUnavailable},
		{http.StatusTooManyRequests, failure.BackingUnavailable},
		{http.StatusNotFound, failure.BackingMalformed},
		{http.StatusUnauthorized, failure.BackingMalformed},
	}
	for _, c := range cases {
		t.Run(http.StatusText(c.code), func(t *testing.T) {
			s := newTestSource(t, Config{}, func(w http.ResponseWriter, r *http.Request) {
				w.WriteHeader(c.code)
			})
			if _, err := s.Fetch(context.Background()); !errors.Is(err, c.want) {
				t.Errorf("status %d: expected %v, got %v", c.code, c.want, err)
			}
		})
	}
}

func TestFetch_SendsAuthHeader(t *testing.T) {
	var got string
	s := newTestSource(t,
		Config{AuthHeader: "X-API-Key", AuthValue: "secret-value"},
		func(w http.ResponseWriter, r *http.Request) {
			got = r.Header.Get("X-API-Key")
			_, _ = fmt.Fprintf(w, publicBody, nowStamp())
		})

	if _, err := s.Fetch(context.Background()); err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if got != "secret-value" {
		t.Errorf("auth header = %q, want %q", got, "secret-value")
	}
}

// Go strips Authorization across a redirect but NOT custom headers, so an
// upstream that redirects could otherwise be handed the API key. Redirects are
// disabled outright.
func TestFetch_DoesNotFollowRedirects(t *testing.T) {
	var leakedTo string
	sink := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		leakedTo = r.Header.Get("X-API-Key")
		_, _ = fmt.Fprintf(w, publicBody, nowStamp())
	}))
	defer sink.Close()

	src := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		http.Redirect(w, r, sink.URL, http.StatusFound)
	}))
	defer src.Close()

	s, err := New(Config{
		Name: "hex_test", URL: strings.Replace(src.URL, "http://", "https://", 1),
		MetricName: publicMetric,
		AuthHeader: "X-API-Key", AuthValue: "secret-value", Timeout: time.Second,
	})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	// Swap in the plain-HTTP client so the redirect is actually exercised; the
	// CheckRedirect policy under test is set by New().
	hs, ok := s.(*hexSource)
	if !ok {
		t.Fatalf("New returned %T, want *hexSource", s)
	}
	hs.cfg.URL = src.URL
	hs.client = &http.Client{Timeout: time.Second, CheckRedirect: hs.client.CheckRedirect}

	_, _ = hs.Fetch(context.Background())

	if leakedTo != "" {
		t.Errorf("API key leaked to redirect target: %q", leakedTo)
	}
}

func TestFetch_TimeoutClassified(t *testing.T) {
	s := newTestSource(t, Config{Timeout: 50 * time.Millisecond},
		func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(500 * time.Millisecond)
		})

	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingTimeout) {
		t.Errorf("expected BackingTimeout, got %v", err)
	}
}

func TestFetch_RespectsContextCancellation(t *testing.T) {
	s := newTestSource(t, Config{Timeout: 5 * time.Second},
		func(w http.ResponseWriter, r *http.Request) {
			time.Sleep(2 * time.Second)
		})

	ctx, cancel := context.WithTimeout(context.Background(), 80*time.Millisecond)
	defer cancel()

	start := time.Now()
	if _, err := s.Fetch(ctx); err == nil {
		t.Fatal("expected an error")
	}
	if elapsed := time.Since(start); elapsed > 2*time.Second {
		t.Errorf("fetch outlived its context by %v", elapsed)
	}
}

// A body that never ends must not be read without bound.
func TestFetch_BodyIsSizeCapped(t *testing.T) {
	s := newTestSource(t, Config{Timeout: 2 * time.Second},
		func(w http.ResponseWriter, r *http.Request) {
			w.Header().Set("Content-Type", "application/json")
			chunk := strings.Repeat("a", 4096)
			for range 100 {
				_, _ = fmt.Fprint(w, chunk)
			}
		})

	// It must fail as malformed rather than hang or exhaust memory.
	if _, err := s.Fetch(context.Background()); !errors.Is(err, failure.BackingMalformed) {
		t.Errorf("expected BackingMalformed, got %v", err)
	}
}

// Every error leaving this package must be publishable.
func TestFetch_AllErrorsArePublishable(t *testing.T) {
	bodies := []string{`not json`, `{"value":""}`, `{"metric_name":"current_reserves_amount","value":"x","last_update":"y"}`}
	for _, b := range bodies {
		s := newTestSource(t, Config{}, serve(b))
		_, err := s.Fetch(context.Background())
		if err == nil {
			continue
		}
		var f failure.Failure
		if !errors.As(err, &f) {
			t.Errorf("error %v is not a failure.Failure and could leak a reserve figure", err)
		}
	}
}

// --- config validation ---

func TestNew_RejectsUnsafeConfig(t *testing.T) {
	base := Config{
		Name: "hex_bonds", URL: "https://example.com/m",
		MetricName: publicMetric, Timeout: time.Second,
	}

	cases := []struct {
		name   string
		mutate func(*Config)
	}{
		{"plaintext http", func(c *Config) { c.URL = "http://example.com/m" }},
		{"url as name", func(c *Config) { c.Name = "https://api.example.com/m" }},
		{"hostname as name", func(c *Config) { c.Name = "api.example.com" }},
		{"empty name", func(c *Config) { c.Name = "" }},
		{"auth value without header", func(c *Config) { c.AuthValue = "k" }},
		{"auth header without value", func(c *Config) { c.AuthHeader = "X-API-Key" }},
		{"zero timeout", func(c *Config) { c.Timeout = 0 }},
		{"empty metric name", func(c *Config) { c.MetricName = "" }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			cfg := base
			c.mutate(&cfg)
			if _, err := New(cfg); err == nil {
				t.Error("expected a config error")
			}
		})
	}
}

func TestNew_AcceptsValidConfig(t *testing.T) {
	if _, err := New(Config{
		Name: "hex_bonds", URL: "https://api.example.com/metrics",
		MetricName: publicMetric, Timeout: time.Second,
	}); err != nil {
		t.Errorf("unexpected error: %v", err)
	}
}
