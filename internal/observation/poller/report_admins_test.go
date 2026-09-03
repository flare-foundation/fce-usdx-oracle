package poller

import (
	"context"
	"errors"
	"testing"

	"fce-usdx-oracle/internal/failure"

	"github.com/ethereum/go-ethereum/common"
)

// A machine observes with endpoints alone, but the update it would sign commits
// to an admin set. Without one the store rejects the update, so Report refuses
// by name — before the signer is called, and with the source health intact so
// an operator can see the sources themselves are fine.
func TestReport_RefusesToSignWithoutAnAdminSet(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	obs := observationAt(1_800_000_000)
	obs.Binding.AdminsHash = common.Hash{}
	p.record(obs, nil)

	got, err := p.Report(context.Background(), testRequestedAt)
	if !errors.Is(err, failure.NotConfigured) {
		t.Fatalf("Report without admins = %v, want NotConfigured", err)
	}
	if signer.count() != 0 {
		t.Errorf("the signer was called %d times for an update that names no admin set", signer.count())
	}
	if len(got.Data) != 0 {
		t.Error("a refused report must carry no payload")
	}
	if len(got.Health) != len(obs.Health) {
		t.Error("source health must still travel with the refusal")
	}
}
