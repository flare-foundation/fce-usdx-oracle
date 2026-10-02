package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"

	"fce-usdx-oracle/pkg/protocol"
)

const (
	a1 = "0x1111111111111111111111111111111111111111"
	a2 = "0x2222222222222222222222222222222222222222"
	a3 = "0x3333333333333333333333333333333333333333"
)

// An unknown top-level key decodes as a role the extension does not pin. With
// the role-tagged schema, an unrecognised key is no longer a "field" — it is a
// role — so the unknown-role check is what must catch it. The structurally-valid
// unknown role parses; validateAdmins is what rejects it.
func TestValidateAdmins_RejectsUnknownRole(t *testing.T) {
	parsed, err := parseAdmins([]byte(`{"backing":{"threshold":1,"admins":["` + a1 +
		`"]},"providers":{"threshold":2,"admins":["` + a1 + `","` + a2 +
		`"]},"auditors":{"threshold":1,"admins":["` + a1 + `"]}}`))
	if err != nil {
		t.Fatalf("parseAdmins: %v", err)
	}
	if err := validateAdmins(parsed); err == nil {
		t.Fatal("want an error for an unknown role")
	} else if !strings.Contains(err.Error(), "unknown role") {
		t.Errorf("error = %q, want it to contain %q", err, "unknown role")
	}
}

// The previous single-set schema must not decode into a half-populated payload
// that then installs one set and leaves the other empty. A top-level "threshold"
// is a number where a role object is expected, so it cannot parse.
func TestParseAdmins_RejectsTheFlatSchema(t *testing.T) {
	parsed, err := parseAdmins([]byte(`{"threshold":2,"admins":["` + a1 + `"]}`))
	if err == nil {
		err = validateAdmins(parsed)
	}
	if err == nil {
		t.Fatal("the flat schema must not be accepted")
	}
}

// A missing role is rejected: the machine needs BOTH signer sets to provision, so
// publishing one and leaving the other absent strands it.
func TestValidateAdmins_RequiresBothRoles(t *testing.T) {
	parsed, err := parseAdmins([]byte(`{"backing":{"threshold":1,"admins":["` + a1 + `"]}}`))
	if err != nil {
		t.Fatalf("parseAdmins: %v", err)
	}
	if err := validateAdmins(parsed); err == nil {
		t.Fatal("want an error when providers is missing")
	} else if !strings.Contains(err.Error(), `role "providers" is missing`) {
		t.Errorf("error = %q, want it to name the missing role", err)
	}
}

// adminSetJSON renders a set of n distinct non-zero addresses at the given
// threshold, for the bounds the three literal addresses cannot reach. No keys
// exist behind them; these payloads never get signed.
func adminSetJSON(threshold, n int) string {
	admins := make([]string, n)
	for i := range admins {
		admins[i] = fmt.Sprintf("%q", fmt.Sprintf("0x%040x", i+1))
	}
	return `{"threshold":` + strconv.Itoa(threshold) +
		`,"admins":[` + strings.Join(admins, ",") + `]}`
}

// payload builds a full two-role document, so each case varies exactly one thing.
func payload(backing, providers string) string {
	return `{"backing":` + backing + `,"providers":` + providers + `}`
}

func TestValidateAdmins(t *testing.T) {
	const okBacking = `{"threshold":1,"admins":["` + a1 + `"]}`
	const okProviders = `{"threshold":2,"admins":["` + a1 + `","` + a2 + `","` + a3 + `"]}`

	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name:    "valid 1-of-1 backing and 2-of-3 providers",
			payload: payload(okBacking, okProviders),
		},
		{
			// The asymmetry that motivates the split: one key is enough for the
			// pinned backing endpoint, never for the provider credentials.
			name:    "providers below the floor",
			payload: payload(okBacking, `{"threshold":1,"admins":["`+a1+`","`+a2+`"]}`),
			wantErr: "threshold 1 is not between 2 and 2",
		},
		{
			name:    "backing accepts a single signer",
			payload: payload(`{"threshold":1,"admins":["`+a1+`"]}`, okProviders),
		},
		{
			// Caught by configpayload.Threshold at parse time, before the per-role
			// range check in validateAdmins.
			name:    "backing threshold of zero",
			payload: payload(`{"threshold":0,"admins":["`+a1+`"]}`, okProviders),
			wantErr: "threshold 0 is out of range",
		},
		{
			name:    "providers threshold above the set size",
			payload: payload(okBacking, `{"threshold":3,"admins":["`+a1+`","`+a2+`"]}`),
			wantErr: "threshold 3 is not between 2 and 2",
		},
		{
			// The set is big enough, but the extension drops an envelope
			// carrying more than maxThreshold signatures before it counts
			// signers, so this rotation would land and then strand the machine.
			name:    "providers threshold above the signature cap",
			payload: payload(okBacking, adminSetJSON(maxThreshold+1, maxThreshold+4)),
			wantErr: "threshold 17 is not between 2 and 16",
		},
		{
			// The cap itself is satisfiable, and only the threshold is bounded:
			// a large set with a small threshold is ordinary M-of-N.
			name:    "providers threshold exactly at the signature cap",
			payload: payload(okBacking, adminSetJSON(maxThreshold, maxAdmins)),
		},
		{
			name:    "small threshold over a full-size set",
			payload: payload(okBacking, adminSetJSON(2, maxAdmins)),
		},
		{
			name:    "empty backing set",
			payload: payload(`{"threshold":1,"admins":[]}`, okProviders),
			wantErr: "admin count 0",
		},
		{
			name:    "empty providers set",
			payload: payload(okBacking, `{"threshold":2,"admins":[]}`),
			wantErr: "admin count 0",
		},
		{
			// HexToAddress would silently yield the zero address here. Surfaces at
			// parse time, not validate time.
			name:    "not an address",
			payload: payload(okBacking, `{"threshold":2,"admins":["`+a1+`","not-an-address"]}`),
			wantErr: "is not a hex address",
		},
		{
			name:    "zero address",
			payload: payload(okBacking, `{"threshold":2,"admins":["`+a1+`","0x0000000000000000000000000000000000000000"]}`),
			wantErr: "admin address is zero",
		},
		{
			name:    "duplicate admin",
			payload: payload(okBacking, `{"threshold":2,"admins":["`+a1+`","`+a1+`","`+a2+`"]}`),
			wantErr: "appears more than once",
		},
		{
			// The same person may legitimately hold both roles.
			name:    "sets may overlap",
			payload: payload(`{"threshold":1,"admins":["`+a1+`"]}`, okProviders),
		},
		{
			// Checksum casing must not change the meaning of an address.
			name:    "lowercase addresses are accepted",
			payload: payload(`{"threshold":1,"admins":["`+strings.ToLower(a1)+`"]}`, okProviders),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			// Address problems surface at parse time, shape problems at validate
			// time; fold both into one error to assert against.
			parsed, perr := parseAdmins([]byte(c.payload))
			err := perr
			if err == nil {
				err = validateAdmins(parsed)
			}

			if c.wantErr == "" {
				if err != nil {
					t.Fatalf("unexpected error: %v", err)
				}
				return
			}
			if err == nil {
				t.Fatalf("want an error containing %q", c.wantErr)
			}
			if !strings.Contains(err.Error(), c.wantErr) {
				t.Errorf("error = %q, want it to contain %q", err, c.wantErr)
			}
		})
	}
}

// The checked-in fixture must pass the same preflight a published payload does.
// A stale fixture is one -config argument away from a rotation that half-lands.
func TestFixtureIsValid(t *testing.T) {
	path := filepath.Join("..", "..", "..", "deploy", "config", "admins", "dev-wellknown-keys.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("reading %s: %v", path, err)
	}

	parsed, err := parseAdmins(raw)
	if err != nil {
		t.Fatalf("parseAdmins: %v", err)
	}
	if err := validateAdmins(parsed); err != nil {
		t.Fatalf("validateAdmins: %v", err)
	}
}

// The summary is what an operator checks before signing a rotation, so the
// credentials it says each set governs must be the ones the build pins: the
// backing set governs both Hex credentials, and naming only one would hide that
// the cash URL rides on the weaker set.
func TestSummariseNamesEveryBackingRef(t *testing.T) {
	parsed, err := parseAdmins([]byte(payload(
		`{"threshold":1,"admins":["`+a1+`"]}`,
		`{"threshold":2,"admins":["`+a2+`","`+a3+`"]}`)))
	if err != nil {
		t.Fatalf("parseAdmins: %v", err)
	}
	out := summarise(parsed)
	for _, want := range []string{
		"backing: 1-of-1 — governs hex_api_key and hex_cash_url\n",
		"providers: 2-of-2 — governs every other credential\n",
	} {
		if !strings.Contains(out, want) {
			t.Errorf("summary is missing %q:\n%s", want, out)
		}
	}
	for _, ref := range protocol.BackingRefs() {
		if !strings.Contains(out, ref) {
			t.Errorf("summary does not name backing ref %s:\n%s", ref, out)
		}
	}
}
