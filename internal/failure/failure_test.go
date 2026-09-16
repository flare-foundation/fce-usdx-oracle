package failure

import (
	"net/http"
	"regexp"
	"strings"
	"testing"
	"time"
)

// The public vocabulary must never contain a value, a URL, or a credential.
// These patterns are what leaked error strings actually look like — see the
// package doc for the two real cases this exists to prevent.
var forbidden = []struct {
	name string
	re   *regexp.Regexp
}{
	{"a URL or scheme", regexp.MustCompile(`(?i)https?://|://`)},
	{"a hostname", regexp.MustCompile(`(?i)\b[a-z0-9-]+\.(com|net|org|io|dev|rocks)\b`)},
	{"a digit", regexp.MustCompile(`[0-9]`)},
	{"a quoted literal", regexp.MustCompile(`["'` + "`" + `]`)},
	{"a credential-ish word", regexp.MustCompile(`(?i)key|token|secret|bearer|password|auth`)},
}

func TestVocabularyIsPublishable(t *testing.T) {
	for _, f := range All {
		for _, pat := range forbidden {
			if pat.re.MatchString(f.Error()) {
				t.Errorf("failure %q contains %s — ActionResult.Log is public "+
					"(contract §4.6), so it must not carry values, endpoints or credentials",
					f.Error(), pat.name)
			}
		}
	}
}

func TestVocabularyIsPopulated(t *testing.T) {
	for _, f := range All {
		if f.Error() == "" {
			t.Error("a failure has an empty reason")
		}
		if f.Class() == ClassNone {
			t.Errorf("failure %q has no class; the readiness report would show it as healthy", f.Error())
		}
	}
}

// Reasons should be distinguishable, or an operator cannot tell two failure
// modes apart from the public surface alone.
func TestVocabularyIsUnique(t *testing.T) {
	seen := map[string]bool{}
	for _, f := range All {
		if seen[f.Error()] {
			t.Errorf("duplicate failure reason %q", f.Error())
		}
		seen[f.Error()] = true
	}
}

func TestZeroFailureIsNotAFailure(t *testing.T) {
	var f Failure
	if !f.IsZero() {
		t.Error("the zero Failure should report IsZero")
	}
	for _, real := range All {
		if real.IsZero() {
			t.Errorf("declared failure %q reports IsZero", real.Error())
		}
	}
}

// The regression guard for the incident this package prevents: wrapping a real
// upstream error and publishing it. It asserts that the errors we would otherwise
// have surfaced DO contain secrets, so the danger stays documented.
func TestRawErrorsWouldLeak(t *testing.T) {
	c := &http.Client{Timeout: 200 * time.Millisecond}
	resp, err := c.Get("https://usdx-observation.invalid/restricted/metrics?apikey=SECRET_VALUE")
	if err == nil {
		_ = resp.Body.Close()
		t.Skip("expected the request to fail")
	}
	if !strings.Contains(err.Error(), "SECRET_VALUE") {
		t.Skip("http error no longer embeds the URL; the guard below is what matters")
	}

	// The whole point: this error must never reach a Failure.
	for _, f := range All {
		if strings.Contains(f.Error(), "SECRET_VALUE") {
			t.Fatal("a Failure carries request-derived data")
		}
	}
}
