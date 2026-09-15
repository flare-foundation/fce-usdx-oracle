package extension

import (
	"encoding/json"
	"errors"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/pkg/types"

	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

// readinessAction builds a well-formed GET_READINESS over the direct channel,
// which is the only one it is valid on.
func readinessAction() teetypes.Action {
	return buildDirectAction(
		config.OPTypeTeeOracle,
		config.OPCommandGetReadiness,
		[]byte{config.ReadinessRequestV1},
	)
}

func readinessExtension(o *fakeObserver, p *fakeProvisioner) *Extension {
	e := newTestExtension()
	e.observer = o
	e.provisioner = p
	return e
}

func TestGetReadiness_Success(t *testing.T) {
	o := &fakeObserver{readiness: []byte("signed-readiness")}
	p := &fakeProvisioner{readiness: types.Readiness{
		EndpointsSet: true,
		Ready:        false,
		MissingRefs:  []string{"hex_api_key"},
	}}
	e := readinessExtension(o, p)

	status, body := e.processAction(readinessAction())
	result := mustResult(t, status, body)

	if result.Status != 1 {
		t.Fatalf("expected Status=1, got %d: %s", result.Status, result.Log)
	}
	if string(result.Data) != "signed-readiness" {
		t.Errorf("expected the observer's payload verbatim, got %q", result.Data)
	}
	if o.readinessCalls != 1 {
		t.Errorf("expected one ReadinessReport call, got %d", o.readinessCalls)
	}

	// The provisioning half is the extension's to supply: the observer does not
	// own it and must not be left to invent it.
	if !o.lastProvisioning.EndpointsSet || len(o.lastProvisioning.MissingRefs) != 1 {
		t.Errorf("provisioning readiness was not passed through: %+v", o.lastProvisioning)
	}

	// A status query is not a price. Counting it would make polling look like
	// production on GET /state.
	if e.updateCount != 0 {
		t.Errorf("readiness must not touch the price counters, updateCount=%d", e.updateCount)
	}
}

// The direct action carries the proxy's action id in place of an instruction
// id, and that is what the answer must be bound to — otherwise every readiness
// report for a machine is replayable as any other.
func TestGetReadiness_BindsToTheActionID(t *testing.T) {
	o := &fakeObserver{}
	e := readinessExtension(o, &fakeProvisioner{})

	if _, body := e.processAction(readinessAction()); body == nil {
		t.Fatal("no body")
	}
	if o.lastID != testDirectActionID {
		t.Errorf("expected binding to %s, got %s", testDirectActionID.Hex(), o.lastID.Hex())
	}
}

// Instruction-channel delivery must be refused. On-chain the payload would be
// permanent public calldata and every poll would cost a fee, which is why the
// command exists only on the direct channel.
func TestGetReadiness_RejectsInstructionChannel(t *testing.T) {
	o := &fakeObserver{}
	e := readinessExtension(o, &fakeProvisioner{})

	action := buildTestAction(
		config.OPTypeTeeOracle,
		config.OPCommandGetReadiness,
		[]byte{config.ReadinessRequestV1},
	)

	status, body := e.processAction(action)
	result := mustResult(t, status, body)

	if result.Status != 0 {
		t.Fatalf("expected Status=0 on the instruction channel, got %d", result.Status)
	}
	if o.readinessCalls != 0 {
		t.Error("the observer must not be reached for a wrongly-channelled request")
	}
}

func TestGetReadiness_RejectsUnexpectedPayload(t *testing.T) {
	for _, tc := range []struct {
		name    string
		payload []byte
	}{
		{"empty", nil},
		{"wrong version", []byte{0x02}},
		{"too long", []byte{config.ReadinessRequestV1, 0x00}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			o := &fakeObserver{}
			e := readinessExtension(o, &fakeProvisioner{})

			action := buildDirectAction(
				config.OPTypeTeeOracle,
				config.OPCommandGetReadiness,
				tc.payload,
			)

			status, body := e.processAction(action)
			result := mustResult(t, status, body)

			if result.Status != 0 {
				t.Fatalf("expected Status=0, got %d", result.Status)
			}
			if result.Log != "error: "+failure.UnexpectedPayload.Error() {
				t.Errorf("expected the vocabulary reason, got %q", result.Log)
			}
			if o.readinessCalls != 0 {
				t.Error("a malformed request must not reach the observer")
			}
		})
	}
}

// An extension without a provisioner has nothing to describe, and saying so is
// the honest answer. The important part is that the nil provisioner does not
// panic.
func TestGetReadiness_NoProvisionerReportsNotConfigured(t *testing.T) {
	e := newTestExtension()

	status, body := e.processAction(readinessAction())
	result := mustResult(t, status, body)

	if result.Status != 0 {
		t.Fatalf("expected Status=0 without a provisioner, got %d", result.Status)
	}
	if result.Log != "error: "+failure.NotConfigured.Error() {
		t.Errorf("expected not_configured, got %q", result.Log)
	}
}

// A signing or encoding fault is the one genuine error path. It must surface as
// vocabulary, never as the underlying error's own words: ActionResult.Log is
// public.
func TestGetReadiness_SigningFailureStaysVocabulary(t *testing.T) {
	const secret = "sk-live-should-never-appear"
	o := &fakeObserver{readinessErr: errors.New(secret)}
	e := readinessExtension(o, &fakeProvisioner{})

	status, body := e.processAction(readinessAction())
	result := mustResult(t, status, body)

	if result.Status != 0 {
		t.Fatalf("expected Status=0, got %d", result.Status)
	}
	if contains(string(body), secret) {
		t.Fatal("the raw error reached a public surface")
	}

	var cls struct{ Log string }
	_ = json.Unmarshal(body, &cls)
	if !contains(result.Log, string(failure.ClassInternal)) {
		t.Errorf("expected the internal class, got %q", result.Log)
	}
}
