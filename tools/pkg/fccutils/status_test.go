package fccutils

import "testing"

// The labels are upstream's IMachineManager.TeeStatus, in enum order. An
// operator reads them off fleet-status and looks them up in the contracts, so
// a label upstream does not use (REGISTERED, DECOMMISSIONED) sends them to a
// state that does not exist.
func TestTeeStatusNameFollowsUpstream(t *testing.T) {
	want := []string{"NONE", "INITIALIZED", "PRODUCTION", "SUSPENDED", "PAUSED", "BANNED"}
	for status, name := range want {
		if got := TeeStatusName(uint8(status)); got != name {
			t.Errorf("TeeStatusName(%d) = %q, want %q", status, got, name)
		}
	}
	if got := TeeStatusName(6); got != "UNKNOWN(6)" {
		t.Errorf("TeeStatusName(6) = %q, want UNKNOWN(6)", got)
	}
}
