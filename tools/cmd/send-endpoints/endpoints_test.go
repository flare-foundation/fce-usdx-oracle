package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// Every fixture the repo ships must survive the preflight. If one does not, it
// is either stale or the preflight has drifted from internal/runtimecfg — and
// both are exactly the failure this check exists to catch, so the suite should
// say so rather than let someone discover it by spending gas.
func TestShippedFixturesValidate(t *testing.T) {
	matches, err := filepath.Glob("../../../testdata/endpoints/*.json")
	if err != nil {
		t.Fatalf("globbing fixtures: %v", err)
	}
	if len(matches) == 0 {
		t.Fatal("no endpoint fixtures found — the path is wrong or they have moved")
	}

	for _, path := range matches {
		t.Run(filepath.Base(path), func(t *testing.T) {
			raw, err := os.ReadFile(path)
			if err != nil {
				t.Fatalf("reading: %v", err)
			}

			parsed, err := parseEndpoints(raw)
			if err != nil {
				t.Fatalf("parse: %v", err)
			}
			if err := validateEndpoints(parsed); err != nil {
				t.Fatalf("validate: %v", err)
			}
		})
	}
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wants   string
	}{
		{
			// The original footgun: a payload from the pre-pinning schema. It is
			// valid JSON, so json.Valid passed it straight onto the chain.
			name: "old schema with unknown fields",
			payload: `{"flare":{"token":"0x1","quorum":2,"finalityMargin":3,
			           "providers":[{"url":"https://a/"}]}}`,
			wants: "not a known configuration",
		},
		{
			name:    "unknown chain",
			payload: `{"solana":{"threshold":1,"providers":[{"url":"https://a/"}]}}`,
			wants:   "unknown chain",
		},
		{
			// Undercounts supply, which OVERSTATES the price.
			name: "missing pinned chain",
			payload: `{"flare":{"threshold":1,"providers":[{"url":"https://a/"}]},
			           "ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}}`,
			wants: `chain "songbird" is missing`,
		},
		{
			name: "threshold above provider count",
			payload: allChains(`"flare":{"threshold":3,"providers":[{"url":"https://a/"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "is not between 1 and 1",
		},
		{
			name: "zero threshold",
			payload: allChains(`"flare":{"threshold":0,"providers":[{"url":"https://a/"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "is not between 1 and 1",
		},
		{
			name: "no providers",
			payload: allChains(`"flare":{"threshold":1,"providers":[]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "has no providers",
		},
		{
			name: "plaintext http",
			payload: allChains(`"flare":{"threshold":1,"providers":[{"url":"http://a/"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "must use https",
		},
		{
			name: "secret ref without placeholder",
			payload: allChains(`"flare":{"threshold":1,"providers":[{"url":"https://a/","secret":"k"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "must be used together",
		},
		{
			name: "placeholder without secret ref",
			payload: allChains(`"flare":{"threshold":1,"providers":[{"url":"https://a/{secret}"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "must be used together",
		},
		{
			// A credential must never be able to choose the host it is sent to.
			name: "placeholder in the host",
			payload: allChains(`"flare":{"threshold":1,"providers":[{"url":"https://{secret}.evil/","secret":"k"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "may not appear in the scheme or host",
		},
		{
			// The backing credential authenticates a pinned endpoint. Naming it as
			// a provider secret would deliver it to whatever host the URL names.
			name: "backing credential used as a provider secret",
			payload: allChains(
				`"flare":{"threshold":1,"providers":[{"url":"https://a/{secret}","secret":"hex_api_key"}]}`,
				`"ethereum":{"threshold":1,"providers":[{"url":"https://b/"}]}`,
				`"songbird":{"threshold":1,"providers":[{"url":"https://c/"}]}`),
			wants: "may not be used as a provider secret",
		},
		{
			name:    "empty object",
			payload: `{}`,
			wants:   "no chains configured",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := parseEndpoints([]byte(c.payload))
			if err == nil {
				err = validateEndpoints(parsed)
			}
			if err == nil {
				t.Fatalf("expected rejection containing %q, got none", c.wants)
			}
			if !strings.Contains(err.Error(), c.wants) {
				t.Errorf("error = %q, want it to contain %q", err, c.wants)
			}
		})
	}
}

// summarise reports what is about to be published without disclosing a URL:
// this reaches a terminal and logs.
func TestSummariseNamesRefsNeverURLs(t *testing.T) {
	raw, err := os.ReadFile("../../../testdata/endpoints/coston2.json")
	if err != nil {
		t.Skipf("fixture unavailable: %v", err)
	}
	parsed, err := parseEndpoints(raw)
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	out := summarise(parsed)

	if strings.Contains(out, "https://") {
		t.Errorf("summary leaked a URL:\n%s", out)
	}
	for _, want := range []string{"flare: 2-of-3", "ethereum: 2-of-2", "songbird: 1-of-1", "ankr_flare_key"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
}

// allChains assembles a payload from three per-chain fragments.
func allChains(fragments ...string) string {
	return "{" + strings.Join(fragments, ",") + "}"
}
