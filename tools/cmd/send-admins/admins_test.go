package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestParseAdmins_RejectsUnknownFields(t *testing.T) {
	// An unrecognised field means the publisher and the running build disagree
	// about the schema; applying the subset we understand is worse than stopping.
	_, err := parseAdmins([]byte(`{"version":1,"threshold":2,"admins":["0x1111111111111111111111111111111111111111","0x2222222222222222222222222222222222222222"],"quorum":3}`))
	if err == nil {
		t.Fatal("want an error for an unknown field")
	}
}

func TestValidateAdmins(t *testing.T) {
	const a1 = "0x1111111111111111111111111111111111111111"
	const a2 = "0x2222222222222222222222222222222222222222"
	const a3 = "0x3333333333333333333333333333333333333333"

	cases := []struct {
		name    string
		payload string
		wantErr string
	}{
		{
			name:    "valid 2-of-3",
			payload: `{"version":1,"threshold":2,"admins":["` + a1 + `","` + a2 + `","` + a3 + `"]}`,
		},
		{
			name:    "zero version is the never-applied sentinel",
			payload: `{"version":0,"threshold":2,"admins":["` + a1 + `","` + a2 + `"]}`,
			wantErr: "version must be non-zero",
		},
		{
			// One key would be the whole root of trust.
			name:    "threshold below the floor",
			payload: `{"version":1,"threshold":1,"admins":["` + a1 + `","` + a2 + `"]}`,
			wantErr: "threshold 1 is not between 2 and 2",
		},
		{
			name:    "threshold above the set size",
			payload: `{"version":1,"threshold":3,"admins":["` + a1 + `","` + a2 + `"]}`,
			wantErr: "threshold 3 is not between 2 and 2",
		},
		{
			name:    "empty set",
			payload: `{"version":1,"threshold":2,"admins":[]}`,
			wantErr: "admin count 0",
		},
		{
			// HexToAddress would silently yield the zero address here.
			name:    "not an address",
			payload: `{"version":1,"threshold":2,"admins":["` + a1 + `","not-an-address"]}`,
			wantErr: "is not a hex address",
		},
		{
			name:    "zero address",
			payload: `{"version":1,"threshold":2,"admins":["` + a1 + `","0x0000000000000000000000000000000000000000"]}`,
			wantErr: "admin address is zero",
		},
		{
			// 2-of-3 naming the same key twice is really 2-of-2 with one key
			// unusable, because the distinct-signer check counts it once.
			name:    "duplicate admin",
			payload: `{"version":1,"threshold":2,"admins":["` + a1 + `","` + a1 + `","` + a2 + `"]}`,
			wantErr: "appears more than once",
		},
		{
			// Checksum casing must not change the meaning of an address.
			name:    "lowercase addresses are accepted",
			payload: `{"version":1,"threshold":2,"admins":["` + strings.ToLower(a1) + `","` + a2 + `"]}`,
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
