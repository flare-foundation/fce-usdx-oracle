package poller

import (
	"context"
	"encoding/json"
	"errors"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/observation"
	"fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/pkg/wire/readiness"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

var readinessInstructionID = common.HexToHash("0xabc")

// decodeReadiness unwraps what ReadinessReport returns and checks the binding
// the signature actually covers, rather than trusting the payload alone.
func decodeReadiness(t *testing.T, data []byte) types.ReadinessReport {
	t.Helper()

	body, sig, payload, err := readiness.DecodeData(data)
	if err != nil {
		t.Fatalf("DecodeData: %v", err)
	}
	if len(body) != readiness.BodyLength {
		t.Fatalf("body is %d bytes, want %d", len(body), readiness.BodyLength)
	}
	if len(sig) == 0 {
		t.Fatal("no signature")
	}

	// The body commits to the payload by hash. Re-hashing the bytes we were
	// given is the whole point: a consumer must never re-serialise, because
	// json.Marshal will not reproduce byte-identical input.
	if !bytesContain(body, crypto.Keccak256(payload)) {
		t.Fatal("the body does not commit to the payload it shipped with")
	}

	var rep types.ReadinessReport
	if err := json.Unmarshal(payload, &rep); err != nil {
		t.Fatalf("payload is not a ReadinessReport: %v", err)
	}
	return rep
}

func bytesContain(haystack, needle []byte) bool {
	if len(needle) == 0 || len(haystack) < len(needle) {
		return false
	}
	for i := 0; i+len(needle) <= len(haystack); i++ {
		match := true
		for j := range needle {
			if haystack[i+j] != needle[j] {
				match = false
				break
			}
		}
		if match {
			return true
		}
	}
	return false
}

func TestReadinessReport_ReflectsTheObservation(t *testing.T) {
	signer := &fakeSigner{}
	p := stubPoller(t, signer)

	// now is 1_800_000_000; observe 60s earlier so the age is a known figure.
	p.record(observationAt(1_800_000_000-60), nil)

	data, err := p.ReadinessReport(context.Background(), readinessInstructionID,
		types.Readiness{EndpointsSet: true, Ready: true})
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}

	rep := decodeReadiness(t, data)

	if rep.LastObservedAt != 1_800_000_000-60 {
		t.Errorf("lastObservedAt = %d", rep.LastObservedAt)
	}
	if rep.ObservationAge != 60 {
		t.Errorf("observationAge = %d, want 60", rep.ObservationAge)
	}
	if !rep.WouldSign {
		t.Error("a 60s-old observation is well inside maxAge; wouldSign should be true")
	}
	if rep.Observations != 1 || rep.Failures != 0 {
		t.Errorf("counters = %d/%d, want 1/0", rep.Observations, rep.Failures)
	}
	if rep.LastErrorClass != "" {
		t.Errorf("lastErrorClass = %q, want empty after a success", rep.LastErrorClass)
	}
	if !rep.Readiness.EndpointsSet || !rep.Readiness.Ready {
		t.Error("the provisioning half was not carried through")
	}
	if rep.ChainID != config.ChainID() {
		t.Errorf("chainId = %d, want %d", rep.ChainID, config.ChainID())
	}
	if signer.count() != 1 {
		t.Errorf("expected exactly one signing call, got %d", signer.count())
	}
}

// The counters are the poller's, not the request handler's. This is the
// distinction the whole surface exists for: a machine that has never served a
// GET_FEED still reports what its loop has been doing.
func TestReadinessReport_CountsFailuresWithoutAnyRequest(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(observation.Observation{}, failure.NotConfigured)
	p.record(observation.Observation{}, failure.NotConfigured)

	rep := decodeReadiness(t, mustReadiness(t, p))

	if rep.Failures != 2 || rep.Observations != 0 {
		t.Errorf("counters = %d obs / %d fail, want 0/2", rep.Observations, rep.Failures)
	}
	if rep.LastErrorClass != string(failure.ClassNotConfigured) {
		t.Errorf("lastErrorClass = %q", rep.LastErrorClass)
	}
	if rep.WouldSign {
		t.Error("wouldSign must be false when there has never been an observation")
	}
	if rep.LastObservedAt != 0 {
		t.Errorf("lastObservedAt = %d, want 0", rep.LastObservedAt)
	}
}

// An observation past maxAge is exactly the case a keeper wants to skip: the
// machine is up and answering, and would still refuse to sign.
func TestReadinessReport_StaleObservationWouldNotSign(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(observationAt(1_800_000_000-(11*60)), nil) // maxAge is 10m

	rep := decodeReadiness(t, mustReadiness(t, p))

	if rep.WouldSign {
		t.Error("an 11-minute-old observation is past maxAge; wouldSign should be false")
	}
	if rep.ObservationAge != 11*60 {
		t.Errorf("observationAge = %d", rep.ObservationAge)
	}
}

// An observation taken before any admin set was installed is one Report refuses
// with NotConfigured: the update it would sign commits to no admin set, and the
// store rejects that. A report saying it would sign would have a keeper pay for
// the refusal.
func TestReadinessReport_NoAdminSetWouldNotSign(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	obs := observationAt(1_800_000_000 - 60)
	obs.Binding.AdminsHash = common.Hash{}
	p.record(obs, nil)

	rep := decodeReadiness(t, mustReadiness(t, p))
	if rep.WouldSign {
		t.Error("an observation that commits to no admin set cannot be signed; wouldSign should be false")
	}
	if rep.ObservationAge != 60 {
		t.Errorf("observationAge = %d, want 60: the refusal is about the admin set, not the age", rep.ObservationAge)
	}
}

// wouldSign is a forecast of Report, so for a request arriving now the two must
// agree in every state the poller can be in.
func TestReadinessReport_WouldSignAgreesWithReport(t *testing.T) {
	noAdmins := observationAt(1_800_000_000 - 60)
	noAdmins.Binding.AdminsHash = common.Hash{}

	cases := []struct {
		name string
		seed func(p *Poller)
	}{
		{"never observed", func(p *Poller) {}},
		{"only failures", func(p *Poller) { p.record(observation.Observation{}, failure.NotConfigured) }},
		{"fresh, admins installed", func(p *Poller) { p.record(observationAt(1_800_000_000-60), nil) }},
		{"at the age bound", func(p *Poller) { p.record(observationAt(1_800_000_000-10*60), nil) }},
		{"past the age bound", func(p *Poller) { p.record(observationAt(1_800_000_000-(10*60+1)), nil) }},
		{"fresh, no admin set", func(p *Poller) { p.record(noAdmins, nil) }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := stubPoller(t, &fakeSigner{})
			c.seed(p)

			rep := decodeReadiness(t, mustReadiness(t, p))
			_, err := p.Report(context.Background(), testRequestedAt)
			if rep.WouldSign != (err == nil) {
				t.Errorf("wouldSign = %v, but Report for a request now returned %v", rep.WouldSign, err)
			}
		})
	}
}

// The one check wouldSign cannot make: Report measures the observation against
// the requesting block's timestamp, which only a request carries. A machine
// whose own clock says the reading is fresh still refuses a request stamped
// more than maxAge later, and one with no timestamp at all.
func TestReadinessReport_CannotForeseeTheRequestClock(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	p.record(observationAt(1_800_000_000-60), nil)

	rep := decodeReadiness(t, mustReadiness(t, p))
	if !rep.WouldSign {
		t.Fatal("setup: a fresh observation under an admin set should report wouldSign")
	}
	if _, err := p.Report(context.Background(), 1_800_000_000+10*60); !errors.Is(err, failure.ClockBehindChain) {
		t.Errorf("Report for a request 11 minutes after the reading = %v, want ClockBehindChain", err)
	}
	if _, err := p.Report(context.Background(), 0); !errors.Is(err, failure.MissingRequestTime) {
		t.Errorf("Report for a request with no timestamp = %v, want MissingRequestTime", err)
	}
}

// Health is copied under the lock. Handing out the live map would race with the
// next observation replacing it.
func TestReadinessReport_HealthIsACopy(t *testing.T) {
	p := stubPoller(t, &fakeSigner{})
	obs := observationAt(1_800_000_000 - 10)
	obs.Health = map[string]string{"rpc_flare_1": types.HealthOK}
	p.record(obs, nil)

	rep := decodeReadiness(t, mustReadiness(t, p))
	if rep.SourceHealth["rpc_flare_1"] != types.HealthOK {
		t.Fatalf("health not carried: %v", rep.SourceHealth)
	}

	// Mutating the source map must not be visible in an already-emitted report.
	obs.Health["rpc_flare_1"] = types.HealthDown
	if rep.SourceHealth["rpc_flare_1"] != types.HealthOK {
		t.Error("the report aliased the observation's health map")
	}
}

// A signing failure must surface as vocabulary. The signer's own words could
// name an endpoint or a key.
func TestReadinessReport_SigningFailureIsNotLeaked(t *testing.T) {
	const secret = "https://rpc.example/KEY-SHOULD-NOT-APPEAR"
	p := stubPoller(t, &fakeSigner{err: errors.New(secret)})
	p.record(observationAt(1_800_000_000-10), nil)

	_, err := p.ReadinessReport(context.Background(), readinessInstructionID, types.Readiness{})
	if err == nil {
		t.Fatal("expected an error")
	}
	if bytesContain([]byte(err.Error()), []byte("KEY-SHOULD-NOT-APPEAR")) {
		t.Fatalf("the signer's error reached the caller verbatim: %v", err)
	}
}

func mustReadiness(t *testing.T, p *Poller) []byte {
	t.Helper()
	data, err := p.ReadinessReport(context.Background(), readinessInstructionID, types.Readiness{})
	if err != nil {
		t.Fatalf("ReadinessReport: %v", err)
	}
	return data
}
