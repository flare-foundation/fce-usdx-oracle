package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"fce-usdx-oracle/pkg/wire/configpayload"
)

// Every fixture the repo ships must survive the preflight. A failure means either a
// stale fixture or a preflight that has drifted from internal/provisioning/store.
func TestShippedFixturesValidate(t *testing.T) {
	matches, err := filepath.Glob("../../../deploy/config/endpoints/*.json")
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

// The four required groups, each as a valid fragment. The PRIVATE hex_cash
// fragment uses a fixed non-zero commitment: the publisher validates shape, not
// that the commitment matches a known URL.
const (
	hexCashHash = "0xabababababababababababababababababababababababababababababababab"

	flareValid    = `"flare":{"threshold":1,"endpoints":[{"url":"https://a/"}]}`
	ethereumValid = `"ethereum":{"threshold":1,"endpoints":[{"url":"https://b/"}]}`
	songbirdValid = `"songbird":{"threshold":1,"endpoints":[{"url":"https://c/"}]}`
	hexCashValid  = `"hex_cash":{"threshold":1,"endpoints":[{"urlHash":"` + hexCashHash + `","secret":"hex_cash_url"}]}`
)

// base assembles a valid payload, overriding the named group with a broken
// fragment (or dropping it when the fragment is empty).
func base(overrideGroup, fragment string) string {
	groups := map[string]string{
		"flare":    flareValid,
		"ethereum": ethereumValid,
		"songbird": songbirdValid,
		"hex_cash": hexCashValid,
	}
	if overrideGroup != "" {
		if fragment == "" {
			delete(groups, overrideGroup)
		} else {
			groups[overrideGroup] = fragment
		}
	}

	// Assemble in a stable order.
	order := []string{"flare", "ethereum", "songbird", "hex_cash"}
	var parts []string
	for _, g := range order {
		if frag, ok := groups[g]; ok {
			parts = append(parts, frag)
		}
	}
	return "{" + strings.Join(parts, ",") + "}"
}

func TestValidateRejects(t *testing.T) {
	cases := []struct {
		name    string
		payload string
		wants   string
	}{
		{
			name:    "unknown group",
			payload: `{"solana":{"threshold":1,"endpoints":[{"url":"https://a/"}]}}`,
			wants:   "unknown group",
		},
		{
			// Undercounts supply, which OVERSTATES the price.
			name:    "missing pinned chain",
			payload: base("songbird", ""),
			wants:   `group "songbird" is missing`,
		},
		{
			name:    "missing hex_cash",
			payload: base("hex_cash", ""),
			wants:   `group "hex_cash" is missing`,
		},
		{
			name:    "threshold above endpoint count",
			payload: base("flare", `"flare":{"threshold":3,"endpoints":[{"url":"https://a/"}]}`),
			wants:   "is not between 1 and 1",
		},
		{
			// Caught by configpayload.Threshold at parse time, before the
			// per-group range check in validateEndpoints.
			name:    "zero threshold",
			payload: base("flare", `"flare":{"threshold":0,"endpoints":[{"url":"https://a/"}]}`),
			wants:   "threshold 0 is out of range",
		},
		{
			name:    "no endpoints",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[]}`),
			wants:   "has no endpoints",
		},
		{
			name:    "plaintext http",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"url":"http://a/"}]}`),
			wants:   "must use https",
		},
		{
			name:    "secret ref without placeholder",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"url":"https://a/","secret":"k"}]}`),
			wants:   "must be used together",
		},
		{
			name:    "placeholder without secret ref",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"url":"https://a/{secret}"}]}`),
			wants:   "must be used together",
		},
		{
			// A credential must never be able to choose the host it is sent to.
			name:    "placeholder in the host",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"url":"https://{secret}.evil/","secret":"k"}]}`),
			wants:   "may not appear in the scheme or host",
		},
		{
			// The backing credential authenticates a pinned endpoint. Naming it as
			// a provider secret would deliver it to whatever host the URL names.
			name: "backing credential used as a provider secret",
			payload: base("flare",
				`"flare":{"threshold":1,"endpoints":[{"url":"https://a/{secret}","secret":"hex_api_key"}]}`),
			wants: "may not be used as a provider secret",
		},
		{
			name:    "endpoint with neither url nor urlHash",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"secret":"k"}]}`),
			wants:   "needs either url (PUBLIC) or urlHash (PRIVATE)",
		},
		{
			name: "endpoint with both url and urlHash",
			payload: base("flare",
				`"flare":{"threshold":1,"endpoints":[{"url":"https://a/","urlHash":"`+hexCashHash+`"}]}`),
			wants: "mutually exclusive",
		},
		{
			name:    "private endpoint without a secret ref",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"urlHash":"`+hexCashHash+`"}]}`),
			wants:   "must name the secret ref",
		},
		{
			name:    "zero urlHash",
			payload: base("flare", `"flare":{"threshold":1,"endpoints":[{"urlHash":"0x0","secret":"k"}]}`),
			wants:   "not a non-zero",
		},
		{
			// hex_cash is the one group the contract's shape rules do not pin — the
			// publisher does, because it carries the numerator of the price. No
			// secret ref here, so the endpoint parses as PUBLIC and checkHexCash
			// (not checkURL) is what rejects it.
			name: "hex_cash as PUBLIC",
			payload: base("hex_cash",
				`"hex_cash":{"threshold":1,"endpoints":[{"url":"https://hex/"}]}`),
			wants: "must be a PRIVATE endpoint",
		},
		{
			name: "hex_cash with the wrong secret ref",
			payload: base("hex_cash",
				`"hex_cash":{"threshold":1,"endpoints":[{"urlHash":"`+hexCashHash+`","secret":"something_else"}]}`),
			wants: `must name secretRef "hex_cash_url"`,
		},
		{
			name:    "hex_cash with two endpoints",
			payload: base("hex_cash", `"hex_cash":{"threshold":1,"endpoints":[{"urlHash":"`+hexCashHash+`","secret":"hex_cash_url"},{"urlHash":"`+hexCashHash+`","secret":"hex_cash_url"}]}`),
			wants:   "must have exactly one endpoint",
		},
		{
			name:    "empty object",
			payload: `{}`,
			wants:   "no groups configured",
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

// Duplicate groups are caught by validateEndpoints, not by the JSON parse:
// Go's json.Unmarshal silently deduplicates map keys (last one wins), so a
// hand-built object can never reach the check. This builds the typed list
// directly to exercise it.
func TestValidateRejectsDuplicateGroup(t *testing.T) {
	tag, err := configpayload.Tag("flare")
	if err != nil {
		t.Fatalf("Tag: %v", err)
	}

	// Two valid chains plus two flare entries.
	ethereum, _ := configpayload.Tag("ethereum")
	songbird, _ := configpayload.Tag("songbird")
	hexCash, _ := configpayload.Tag("hex_cash")

	ep := configpayload.Endpoint{Kind: configpayload.KindPublic, Url: "https://a/"}
	privEp := configpayload.Endpoint{Kind: configpayload.KindPrivate, SecretRef: "hex_cash_url"}
	privEp.UrlHash = [32]byte{0xab}

	groups := []configpayload.Group{
		{Group: tag, Threshold: 1, Endpoints: []configpayload.Endpoint{ep}},
		{Group: tag, Threshold: 1, Endpoints: []configpayload.Endpoint{ep}}, // duplicate
		{Group: ethereum, Threshold: 1, Endpoints: []configpayload.Endpoint{ep}},
		{Group: songbird, Threshold: 1, Endpoints: []configpayload.Endpoint{ep}},
		{Group: hexCash, Threshold: 1, Endpoints: []configpayload.Endpoint{privEp}},
	}

	err = validateEndpoints(groups)
	if err == nil {
		t.Fatal("expected a duplicate-group rejection")
	}
	if !strings.Contains(err.Error(), "appears more than once") {
		t.Errorf("error = %q, want it to contain %q", err, "appears more than once")
	}
}

// A well-formed payload for all four groups round-trips into the typed group
// list that EncodeSetEndpoints will pack.
func TestParseBuildsTypedGroups(t *testing.T) {
	parsed, err := parseEndpoints([]byte(base("", "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	if len(parsed) != 4 {
		t.Fatalf("got %d groups, want 4", len(parsed))
	}

	// Find hex_cash and confirm it came out PRIVATE.
	for _, g := range parsed {
		if strings.Contains(strings.ToLower(string(g.Group[:])), "hex") {
			if len(g.Endpoints) != 1 {
				t.Fatalf("hex_cash has %d endpoints, want 1", len(g.Endpoints))
			}
			if g.Endpoints[0].Kind != 1 { // KindPrivate
				t.Errorf("hex_cash endpoint kind = %d, want PRIVATE (1)", g.Endpoints[0].Kind)
			}
			if g.Endpoints[0].SecretRef != "hex_cash_url" {
				t.Errorf("hex_cash secretRef = %q, want hex_cash_url", g.Endpoints[0].SecretRef)
			}
		}
	}
}

// summarise reports what is about to be published without disclosing a URL:
// this reaches a terminal and logs.
func TestSummariseNamesRefsNeverURLs(t *testing.T) {
	parsed, err := parseEndpoints([]byte(base("", "")))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}

	out := summarise(parsed)

	if strings.Contains(out, "https://") {
		t.Errorf("summary leaked a URL:\n%s", out)
	}
	for _, want := range []string{"flare: 1-of-1", "hex_cash: 1-of-1", "hex_cash_url", "private"} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
}
