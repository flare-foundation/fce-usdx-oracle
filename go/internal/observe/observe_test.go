package observe

import (
	"context"
	"errors"
	"math/big"
	"sync"
	"testing"
	"time"

	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/oracle"

	"github.com/ethereum/go-ethereum/common"
)

// The poller is tested against a fake resolver: what is under test is the caching, the
// staleness bound and the fail-closed rules, none of which involve an upstream. The
// observation itself is covered in internal/oracle.

type fakeSigner struct {
	err    error
	signed [][]byte
	mu     sync.Mutex
}

func (s *fakeSigner) Sign(ctx context.Context, message []byte) ([]byte, error) {
	if s.err != nil {
		return nil, s.err
	}
	s.mu.Lock()
	s.signed = append(s.signed, message)
	s.mu.Unlock()
	sig := make([]byte, 65)
	sig[64] = 27
	return sig, nil
}

func (s *fakeSigner) count() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return len(s.signed)
}

// stubPoller builds a Poller with no resolver, so tests can seed the cache
// directly and exercise Latest/Report in isolation.
func stubPoller(t *testing.T, signer oracle.Signer) *Poller {
	t.Helper()
	return &Poller{
		signer:   signer,
		interval: time.Minute,
		maxAge:   10 * time.Minute,
		now:      func() time.Time { return time.Unix(1_800_000_000, 0).UTC() },
	}
}

func observationAt(unix uint64) oracle.Observation {
	return oracle.Observation{
		Price:         big.NewInt(100_000_000),
		Decimals:      8,
		ObservedAt:    unix,
		EndpointsHash: common.HexToHash("0x9f3c"),
		AdminsHash:    common.HexToHash("0x5e21"),
		Health:        map[string]string{"hex_bonds": "ok"},
	}
}

// Nothing may be served before the first observation completes, and the reason
// must say so rather than presenting as an absent price.
func TestLatest_BeforeFirstObservation(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})

	_, err := p.Latest()
	if !errors.Is(err, failure.NoObservation) {
		t.Fatalf("expected failure.NoObservation, got %v", err)
	}
}

// A failed attempt before any success reports why it failed. A missing
// credential must not present as a mysterious absence.
func TestLatest_ReportsWhyTheAttemptsFailed(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(oracle.Observation{}, failure.NotConfigured)

	_, err := p.Latest()
	if !errors.Is(err, failure.NotConfigured) {
		t.Fatalf("expected failure.NotConfigured, got %v", err)
	}
}

// The staleness bound. Past MaxObservationAge nothing may be signed: a stale
// price under a fresh signature is worse than no price, because the on-chain
// timestamp would say it is current.
func TestLatest_FailsClosedWhenStale(t *testing.T) {
	now := time.Unix(1_800_000_000, 0).UTC()

	cases := []struct {
		name string
		age  time.Duration
		ok   bool
	}{
		{"fresh", 0, true},
		{"one interval old", time.Minute, true},
		{"just inside the bound", 10*time.Minute - time.Second, true},
		{"exactly at the bound", 10 * time.Minute, true},
		{"one second past", 10*time.Minute + time.Second, false},
		{"an hour old", time.Hour, false},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := stubPoller(t, &fakeSigner{})
			p.record(observationAt(uint64(now.Add(-c.age).Unix())), nil)

			got, err := p.Latest()
			if c.ok {
				if err != nil {
					t.Fatalf("expected the observation to be served, got %v", err)
				}
				if got.Price == nil {
					t.Fatal("served an observation with no price")
				}
				return
			}
			if !errors.Is(err, failure.ObservationStale) {
				t.Fatalf("expected failure.ObservationStale, got %v", err)
			}
		})
	}
}

// A failed round must not discard a still-fresh observation. One RPC provider
// flapping is normal; stopping the feed for it would be an outage we caused.
func TestRecord_FailureKeepsTheLastGoodObservation(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	good := observationAt(1_800_000_000)
	p.record(good, nil)

	p.record(oracle.Observation{Health: map[string]string{"hex_bonds": "down"}}, failure.BackingUnavailable)

	got, err := p.Latest()
	if err != nil {
		t.Fatalf("a single failure must not withdraw a fresh observation: %v", err)
	}
	if got.Price.Cmp(good.Price) != 0 {
		t.Errorf("price = %s, want %s", got.Price, good.Price)
	}

	// Health, though, must reflect the failed attempt — it is what tells an
	// operator which source broke, and it is the only signal that anything is
	// wrong while the cached price is still being served.
	if got.Health["hex_bonds"] != "down" {
		t.Errorf("health = %v, want the failing attempt's health", got.Health)
	}

	if _, failures := p.Stats(); failures != 1 {
		t.Errorf("failures = %d, want 1", failures)
	}
}

// Report binds the cached observation to the requesting instruction. The
// signature must cover that id, or a report could be replayed against a
// different request.
func TestReport_SignsTheCachedObservationForThisInstruction(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(1_800_000_000), nil)

	id := common.HexToHash("0xdeadbeef")
	got, err := p.Report(context.Background(), id)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Data) == 0 {
		t.Fatal("Report returned no payload")
	}
	if signer.count() != 1 {
		t.Fatalf("expected exactly one signing call, got %d", signer.count())
	}

	// Two reports off one observation must differ only by instruction id, and
	// must NOT re-observe: the figures and the timestamp are the cached ones.
	second, err := p.Report(context.Background(), common.HexToHash("0xfeedface"))
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.ObservedAt != got.ObservedAt {
		t.Errorf("observedAt moved between reports (%d then %d); signing must not resample",
			got.ObservedAt, second.ObservedAt)
	}
	if string(second.Data) == string(got.Data) {
		t.Error("two instructions produced an identical payload; the id is not bound in")
	}
}

// A stale cache must never reach the signer at all.
func TestReport_DoesNotSignAStaleObservation(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(uint64(time.Unix(1_800_000_000, 0).Add(-time.Hour).Unix())), nil)

	if _, err := p.Report(context.Background(), common.HexToHash("0x1")); !errors.Is(err, failure.ObservationStale) {
		t.Fatalf("expected failure.ObservationStale, got %v", err)
	}
	if signer.count() != 0 {
		t.Errorf("a stale observation was sent to the signer %d times", signer.count())
	}
}

// A signing failure publishes nothing — the same fail-closed rule the
// observation path follows.
func TestReport_SigningFailurePublishesNothing(t *testing.T) {
	p := stubPoller(t, &fakeSigner{err: failure.SignUnavailable})
	p.record(observationAt(1_800_000_000), nil)

	got, err := p.Report(context.Background(), common.HexToHash("0x1"))
	if !errors.Is(err, failure.SignUnavailable) {
		t.Fatalf("expected failure.SignUnavailable, got %v", err)
	}
	if len(got.Data) != 0 {
		t.Error("a failed report must carry no payload")
	}
}

// --- the loop ---

type fakeResolver struct {
	mu    sync.Mutex
	cfg   oracle.Config
	err   error
	calls int
}

func (r *fakeResolver) Resolve() (oracle.Config, error) {
	r.mu.Lock()
	defer r.mu.Unlock()
	r.calls++
	return r.cfg, r.err
}

func (r *fakeResolver) count() int {
	r.mu.Lock()
	defer r.mu.Unlock()
	return r.calls
}

// Run observes immediately rather than waiting out the first interval: a
// restart must not leave the feed unanswerable for two minutes.
func TestRun_ObservesImmediately(t *testing.T) {
	r := &fakeResolver{err: failure.NotConfigured}
	p := New(r, &fakeSigner{})
	p.interval = time.Hour // so only the immediate observation can run

	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan struct{})
	go func() { p.Run(ctx); close(done) }()

	deadline := time.After(2 * time.Second)
	for r.count() == 0 {
		select {
		case <-deadline:
			t.Fatal("Run did not observe before its first tick")
		default:
			time.Sleep(5 * time.Millisecond)
		}
	}

	cancel()
	select {
	case <-done:
	case <-time.After(2 * time.Second):
		t.Fatal("Run did not return after its context was cancelled")
	}
}

// An unresolvable configuration is the normal state at boot and after every
// restart. The loop must keep running through it, not exit.
func TestRun_SurvivesAnUnresolvableConfig(t *testing.T) {
	r := &fakeResolver{err: failure.NotConfigured}
	p := New(r, &fakeSigner{})
	p.interval = 10 * time.Millisecond

	ctx, cancel := context.WithTimeout(context.Background(), 200*time.Millisecond)
	defer cancel()
	p.Run(ctx)

	if r.count() < 2 {
		t.Errorf("expected the loop to keep observing, got %d attempts", r.count())
	}
	if _, err := p.Latest(); !errors.Is(err, failure.NotConfigured) {
		t.Errorf("expected the resolver's reason to surface, got %v", err)
	}
}

// Whatever reaches Latest must be publishable: it lands in ActionResult.Log,
// which is world-readable.
func TestLatest_ErrorsAreVocabulary(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(oracle.Observation{}, errors.New("dial tcp 10.0.0.1:8545?apikey=SECRET: refused"))

	_, err := p.Latest()
	var f failure.Failure
	if !errors.As(err, &f) {
		t.Fatalf("error %v is not a failure.Failure and could leak an endpoint", err)
	}
	if !errors.Is(err, failure.Internal) {
		t.Errorf("expected a foreign error to become failure.Internal, got %v", err)
	}
}
