package main

import (
	"os"
	"path/filepath"
	"strconv"
	"strings"
	"testing"
)

func TestParseAdmins_RejectsUnknownFields(t *testing.T) {
	// An unrecognised field means the publisher and the running build disagree
	// about the schema; applying the subset we understand is worse than stopping.
	_, err := parseAdmins([]byte(`{"version":1,"backing":{"threshold":1,"admins":["` + a1 +
		`"]},"providers":{"threshold":2,"admins":["` + a1 + `","` + a2 + `"]},"quorum":3}`))
	if err == nil {
		t.Fatal("want an error for an unknown field")
	}
}

// The previous single-set schema must not decode into a half-populated payload
// that then installs one set and leaves the other empty.
func TestParseAdmins_RejectsTheFlatSchema(t *testing.T) {
	parsed, err := parseAdmins([]byte(`{"version":1,"threshold":2,"admins":["` + a1 + `"]}`))
	if err == nil {
		err = validateAdmins(parsed)
	}
	if err == nil {
		t.Fatal("the flat schema must not be accepted")
	}
}

const (
	a1 = "0x1111111111111111111111111111111111111111"
	a2 = "0x2222222222222222222222222222222222222222"
	a3 = "0x3333333333333333333333333333333333333333"
)

// payload builds a full two-set document, so each case varies exactly one thing.
func payload(version uint64, backing, providers string) string {
	return `{"version":` + strconv.FormatUint(version, 10) +
		`,"backing":` + backing + `,"providers":` + providers + `}`
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
			payload: payload(1, okBacking, okProviders),
		},
		{
			name:    "zero version is the never-applied sentinel",
			payload: payload(0, okBacking, okProviders),
			wantErr: "version must be non-zero",
		},
		{
			// The asymmetry that motivates the split: one key is enough for the
			// pinned backing endpoint, never for the provider credentials.
			name:    "providers below the floor",
			payload: payload(1, okBacking, `{"threshold":1,"admins":["`+a1+`","`+a2+`"]}`),
			wantErr: "providers threshold 1 is not between 2 and 2",
		},
		{
			name:    "backing accepts a single signer",
			payload: payload(1, `{"threshold":1,"admins":["`+a1+`"]}`, okProviders),
		},
		{
			name:    "backing threshold of zero",
			payload: payload(1, `{"threshold":0,"admins":["`+a1+`"]}`, okProviders),
			wantErr: "backing threshold 0 is not between 1 and 1",
		},
		{
			name:    "providers threshold above the set size",
			payload: payload(1, okBacking, `{"threshold":3,"admins":["`+a1+`","`+a2+`"]}`),
			wantErr: "providers threshold 3 is not between 2 and 2",
		},
		{
			name:    "empty backing set",
			payload: payload(1, `{"threshold":1,"admins":[]}`, okProviders),
			wantErr: "backing admin count 0",
		},
		{
			name:    "empty providers set",
			payload: payload(1, okBacking, `{"threshold":2,"admins":[]}`),
			wantErr: "providers admin count 0",
		},
		{
			// HexToAddress would silently yield the zero address here.
			name:    "not an address",
			payload: payload(1, okBacking, `{"threshold":2,"admins":["`+a1+`","not-an-address"]}`),
			wantErr: "is not a hex address",
		},
		{
			name:    "zero address",
			payload: payload(1, okBacking, `{"threshold":2,"admins":["`+a1+`","0x0000000000000000000000000000000000000000"]}`),
			wantErr: "admin address is zero",
		},
		{
			name:    "duplicate admin",
			payload: payload(1, okBacking, `{"threshold":2,"admins":["`+a1+`","`+a1+`","`+a2+`"]}`),
			wantErr: "appears more than once",
		},
		{
			// The same person may legitimately hold both roles.
			name:    "sets may overlap",
			payload: payload(1, `{"threshold":1,"admins":["`+a1+`"]}`, okProviders),
		},
		{
			// Checksum casing must not change the meaning of an address.
			name:    "lowercase addresses are accepted",
			payload: payload(1, `{"threshold":1,"admins":["`+strings.ToLower(a1)+`"]}`, okProviders),
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			parsed, err := parseAdmins([]byte(c.payload))
			if err != nil {
				t.Fatalf("parseAdmins: %v", err)
			}

			err = validateAdmins(parsed)
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
	path := filepath.Join("..", "..", "..", "testdata", "admins", "dev-wellknown-keys.json")
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
