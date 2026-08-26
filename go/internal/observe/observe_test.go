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

// testRequestedAt is the requesting block's timestamp for a well-timed request:
// the same instant stubPoller's clock is frozen at, which is what an honest
// machine answering promptly would see.
const testRequestedAt uint64 = 1_800_000_000

func observationAt(unix uint64) oracle.Observation {
	return oracle.Observation{
		Value:         100_000_000,
		Decimals:      8,
		ExtensionID:   big.NewInt(66281),
		FeedID:        [21]byte{0x20, 'U', 'S', 'D', 'X'},
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
				if got.Value == 0 {
					t.Fatal("served an observation with no value")
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
	if got.Value != good.Value {
		t.Errorf("value = %d, want %d", got.Value, good.Value)
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

// Report signs the cached observation. It is no longer bound to the requesting
// instruction: the feed store dropped the instructionId field, so two reports
// off one observation are byte-identical by design. What stops a captured
// update being replayed is the store's strictly increasing observedAt plus the
// extension and feed ids inside the signed payload.
func TestReport_SignsTheCachedObservation(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(1_800_000_000), nil)

	got, err := p.Report(context.Background(), testRequestedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if len(got.Data) == 0 {
		t.Fatal("Report returned no payload")
	}
	if signer.count() != 1 {
		t.Fatalf("expected exactly one signing call, got %d", signer.count())
	}

	// A second report must NOT re-observe: the figures and the timestamp are the
	// cached ones, so the payload is identical. Signing that resampled would put
	// a fresh timestamp on stale inputs.
	second, err := p.Report(context.Background(), testRequestedAt)
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if second.ObservedAt != got.ObservedAt {
		t.Errorf("observedAt moved between reports (%d then %d); signing must not resample",
			got.ObservedAt, second.ObservedAt)
	}
	if string(second.Data) != string(got.Data) {
		t.Error("two reports off one observation differ; nothing request-specific may enter the payload")
	}
}

// A stale cache must never reach the signer at all.
func TestReport_DoesNotSignAStaleObservation(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(uint64(time.Unix(1_800_000_000, 0).Add(-time.Hour).Unix())), nil)

	if _, err := p.Report(context.Background(), testRequestedAt); !errors.Is(err, failure.ObservationStale) {
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

	got, err := p.Report(context.Background(), testRequestedAt)
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

// --- the requesting block as reference clock ---

// The local staleness bound is measured against the same clock that stamped the
// observation, so a machine running slow passes it no matter how far off it is.
// The requesting block's timestamp is cosigned, so this one it cannot pass.
func TestReport_RefusesWhenTheClockIsBehindChain(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)

	// Freshly observed by the local clock — Latest is satisfied and would sign.
	p.record(observationAt(testRequestedAt), nil)

	// Chain says the request came a second past maxAge after that observation,
	// which on an honest clock is impossible: the request block is in the past.
	late := testRequestedAt + uint64((10 * time.Minute).Seconds()) + 1

	got, err := p.Report(context.Background(), late)
	if !errors.Is(err, failure.ClockBehindChain) {
		t.Fatalf("expected failure.ClockBehindChain, got %v", err)
	}
	if signer.count() != 0 {
		t.Errorf("a clock-skewed observation reached the signer %d times", signer.count())
	}
	if len(got.Data) != 0 {
		t.Error("a refused report must carry no payload")
	}
	// A clock fault says nothing about the upstreams, and an operator reading the
	// refusal needs to see they are fine.
	if got.Health["hex_bonds"] != "ok" {
		t.Errorf("health did not survive the refusal: %v", got.Health)
	}
}

// The bound is exactly maxAge, inclusive. Pinned because an off-by-one here
// refuses a request that the store would have accepted.
func TestReport_ClockBoundIsInclusive(t *testing.T) {
	maxAge := 10 * time.Minute

	atLimit := checkClock(testRequestedAt, testRequestedAt+uint64(maxAge.Seconds()), maxAge)
	if atLimit != nil {
		t.Errorf("an observation exactly maxAge before the request was refused: %v", atLimit)
	}

	past := checkClock(testRequestedAt, testRequestedAt+uint64(maxAge.Seconds())+1, maxAge)
	if !errors.Is(past, failure.ClockBehindChain) {
		t.Errorf("one second past maxAge: expected ClockBehindChain, got %v", past)
	}
}

// The load-bearing asymmetry. Delivery is fire-and-forget, so an instruction may
// sit in a queue while the poller keeps observing; the observation is then NEWER
// than the block that asked for it, by however long the relay took. That is
// ordinary and must sign. Bounding this direction too would have turned every
// slow relay into a refused request.
func TestReport_LateDeliveryStillSigns(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(testRequestedAt), nil)

	// Requested an hour ago, delivered only now.
	stale := testRequestedAt - uint64(time.Hour.Seconds())

	if _, err := p.Report(context.Background(), stale); err != nil {
		t.Fatalf("a late-delivered instruction was refused: %v", err)
	}
	if signer.count() != 1 {
		t.Errorf("expected one signing call, got %d", signer.count())
	}
}

// A zero timestamp would disable the bound silently, so it is refused rather
// than treated as "no opinion". GET_FEED is instruction-only, so a request that
// carries no block timestamp is malformed, not merely unusual.
func TestReport_RefusesAnInstructionWithNoTimestamp(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)
	p.record(observationAt(testRequestedAt), nil)

	if _, err := p.Report(context.Background(), 0); !errors.Is(err, failure.MissingRequestTime) {
		t.Fatalf("expected failure.MissingRequestTime, got %v", err)
	}
	if signer.count() != 0 {
		t.Errorf("a request with no timestamp reached the signer %d times", signer.count())
	}
}

// The local bound still runs first: when the upstreams have stopped, the answer
// must be ObservationStale, not a clock fault. The two are routed differently —
// one is an upstream outage, the other is this machine's host.
func TestReport_LocalStalenessIsReportedAsItself(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(observationAt(testRequestedAt-uint64(time.Hour.Seconds())), nil)

	if _, err := p.Report(context.Background(), testRequestedAt); !errors.Is(err, failure.ObservationStale) {
		t.Fatalf("expected failure.ObservationStale, got %v", err)
	}
}
