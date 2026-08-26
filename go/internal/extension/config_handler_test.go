package extension

import (
	"context"
	"encoding/json"
	"net/http"
	"strings"
	"testing"

	"fce-usdx-oracle/internal/config"
	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
)

type fakeProvisioner struct {
	endpointsErr error
	adminsErr    error
	secretErr    error
	endpointCall int
	adminsCall   int
	secretCall   int
	readiness    types.Readiness

	// What the provisioner was actually handed. Recorded because a call count
	// alone cannot tell a delivered payload from an empty one, which is exactly
	// the failure the direct envelope produced.
	lastEndpoints []byte
	lastAdmins    []byte
	lastSecret    []byte
}

func (f *fakeProvisioner) ApplyEndpoints(payload []byte) error {
	f.endpointCall++
	f.lastEndpoints = payload
	return f.endpointsErr
}

func (f *fakeProvisioner) ApplyAdmins(payload []byte) error {
	f.adminsCall++
	f.lastAdmins = payload
	return f.adminsErr
}

func (f *fakeProvisioner) ApplySecret(ctx context.Context, message []byte) error {
	f.secretCall++
	f.lastSecret = message
	return f.secretErr
}

func (f *fakeProvisioner) Readiness() types.Readiness { return f.readiness }

// configAction builds a provisioning action in the envelope its channel really uses: a
// DataFixed for an instruction, a DirectInstruction for a direct action. Building both
// as DataFixed hid that the extension could not read a real direct payload.
func configAction(command common.Hash, actionType teetypes.ActionType, payload []byte) teetypes.Action {
	opType, opCommand := config.OPTypeTeeOracle, command

	if actionType == teetypes.Direct {
		return buildDirectAction(opType, opCommand, payload)
	}

	a := buildTestAction(opType, opCommand, payload)
	a.Data.Type = actionType
	return a
}

func newProvisionedExtension(p Provisioner) *Extension {
	e := newTestExtension()
	e.provisioner = p
	return e
}

// --- direct envelope ---

// The regression test for the envelope confusion: DirectInstruction calls the payload
// "message", DataFixed calls it "originalMessage", and reading one as the other does
// not error — so SET_SECRET reached ApplySecret with nil.
func TestSetSecret_DirectEnvelopeDeliversThePayload(t *testing.T) {
	p := &fakeProvisioner{}
	e := newProvisionedExtension(p)

	envelope := []byte("the-signed-envelope")
	status, body := e.processAction(
		configAction(config.OPCommandSetSecret, teetypes.Direct, envelope))
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 1 {
		t.Fatalf("expected success, got %d: %s", result.Status, result.Log)
	}
	if string(p.lastSecret) != string(envelope) {
		t.Errorf("provisioner received %q, want %q", p.lastSecret, envelope)
	}
}

// --- channel enforcement ---

// GET_FEED is raised through requestFeedUpdate(), which pays the registry fee
// and leaves the request on-chain. Serving it over /direct would hand the same
// signed report to anyone holding the proxy API key, for free and unrecorded.
func TestGetPrice_RejectedOverDirectChannel(t *testing.T) {
	e := newTestExtension()

	action := buildDirectAction(config.OPTypeTeeOracle, config.OPCommandGetFeed,
		feedRequest(t))
	status, body := e.processAction(action)
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected rejection, got status %d", result.Status)
	}
	if result.Log != "error: "+failure.WrongActionType.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if e.updateCount != 0 {
		t.Error("a refused request must not count as an update")
	}
}

// SET_ENDPOINTS is authorised by the owner-only check in the on-chain sender.
// Accepting it as a direct action would skip that check entirely.
func TestSetEndpoints_RejectedOverDirectChannel(t *testing.T) {
	p := &fakeProvisioner{}
	e := newProvisionedExtension(p)

	status, body := e.processAction(
		configAction(config.OPCommandSetEndpoints, teetypes.Direct, []byte(`{}`)))
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected rejection, got status %d", result.Status)
	}
	if result.Log != "error: "+failure.WrongActionType.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if p.endpointCall != 0 {
		t.Error("the provisioner must not be reached over the wrong channel")
	}
}

// SET_ADMINS is authorised by the same owner-only check, and decides who may
// then authorise SET_SECRET. A direct action would let anyone who can reach the
// proxy install their own admin set and provision the machine from there.
func TestSetAdmins_RejectedOverDirectChannel(t *testing.T) {
	p := &fakeProvisioner{}
	e := newProvisionedExtension(p)

	status, body := e.processAction(
		configAction(config.OPCommandSetAdmins, teetypes.Direct, []byte(`{}`)))
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected rejection, got status %d", result.Status)
	}
	if result.Log != "error: "+failure.WrongActionType.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if p.adminsCall != 0 {
		t.Error("the provisioner must not be reached over the wrong channel")
	}
}

// SET_SECRET is authorised by M-of-N admin signatures. On-chain it would put a
// credential ciphertext into permanent public storage for no benefit.
func TestSetSecret_RejectedOverInstructionChannel(t *testing.T) {
	p := &fakeProvisioner{}
	e := newProvisionedExtension(p)

	status, body := e.processAction(
		configAction(config.OPCommandSetSecret, teetypes.Instruction, []byte("envelope")))
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected rejection, got status %d", result.Status)
	}
	if result.Log != "error: "+failure.WrongActionType.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if p.secretCall != 0 {
		t.Error("the provisioner must not be reached over the wrong channel")
	}
}

func TestConfig_AcceptsOverCorrectChannel(t *testing.T) {
	cases := []struct {
		name       string
		command    common.Hash
		actionType teetypes.ActionType
		calls      func(*fakeProvisioner) int
	}{
		{"endpoints by instruction", config.OPCommandSetEndpoints, teetypes.Instruction,
			func(p *fakeProvisioner) int { return p.endpointCall }},
		{"admins by instruction", config.OPCommandSetAdmins, teetypes.Instruction,
			func(p *fakeProvisioner) int { return p.adminsCall }},
		{"secret by direct", config.OPCommandSetSecret, teetypes.Direct,
			func(p *fakeProvisioner) int { return p.secretCall }},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			p := &fakeProvisioner{}
			e := newProvisionedExtension(p)

			status, body := e.processAction(configAction(c.command, c.actionType, []byte(`{"a":1}`)))
			result := mustResult(t, status, body, http.StatusOK)

			if result.Status != 1 {
				t.Fatalf("expected success, got %d: %s", result.Status, result.Log)
			}
			if c.calls(p) != 1 {
				t.Errorf("provisioner call count = %d, want 1", c.calls(p))
			}
		})
	}
}

// --- output discipline ---

// Provisioning returns no data on any path. Anything returned would be a
// channel for leaking state back to a caller.
func TestConfig_ReturnsNoDataOnAnyPath(t *testing.T) {
	cases := []struct {
		name string
		p    *fakeProvisioner
		typ  teetypes.ActionType
	}{
		{"success", &fakeProvisioner{}, teetypes.Direct},
		{"rejected", &fakeProvisioner{secretErr: failure.ProvisionRejected}, teetypes.Direct},
		{"wrong channel", &fakeProvisioner{}, teetypes.Instruction},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			e := newProvisionedExtension(c.p)
			_, body := e.processAction(configAction(config.OPCommandSetSecret, c.typ, []byte("x")))

			var result teetypes.ActionResult
			if err := json.Unmarshal(body, &result); err != nil {
				t.Fatalf("unmarshal: %v", err)
			}
			if len(result.Data) != 0 {
				t.Errorf("provisioning returned data: %x", result.Data)
			}
		})
	}
}

// Every rejection reason must be identical, or the endpoint becomes an oracle
// for probing the admin set and the freshness window.
func TestSetSecret_AllRejectionsLookTheSame(t *testing.T) {
	var logs []string
	for range 3 {
		e := newProvisionedExtension(&fakeProvisioner{secretErr: failure.ProvisionRejected})
		_, body := e.processAction(configAction(config.OPCommandSetSecret, teetypes.Direct, []byte("x")))
		var result teetypes.ActionResult
		_ = json.Unmarshal(body, &result)
		logs = append(logs, result.Log)
	}
	for _, l := range logs {
		if l != logs[0] {
			t.Errorf("rejection logs differ: %q vs %q", l, logs[0])
		}
		if l != "error: "+failure.ProvisionRejected.Error() {
			t.Errorf("unexpected rejection log %q", l)
		}
	}
}

// A rejected envelope must not leak its contents back out.
func TestSetSecret_DoesNotEchoEnvelope(t *testing.T) {
	const sentinel = "Zq7CanaryEnvelopeXyz"
	e := newProvisionedExtension(&fakeProvisioner{secretErr: failure.ProvisionRejected})

	_, body := e.processAction(
		configAction(config.OPCommandSetSecret, teetypes.Direct, []byte(sentinel)))

	if strings.Contains(string(body), sentinel) {
		t.Errorf("the result echoes the submitted envelope: %s", body)
	}
}

// --- not provisioned ---

// Before provisioning, a price request must fail closed with the distinct
// not_configured class so monitoring can route it differently from a source
// outage.
func TestGetPrice_NotConfiguredBeforeProvisioning(t *testing.T) {
	e := newTestExtension()
	e.observer = nil
	e.provisioner = &fakeProvisioner{}

	// A provisioner without an observer is the real unprovisioned state.
	status, body := e.processAction(
		configAction(config.OPCommandSetSecret, teetypes.Instruction, []byte("x")))
	result := mustResult(t, status, body, http.StatusOK)
	if result.Status != 0 {
		t.Error("expected a rejection over the wrong channel")
	}
}

func TestConfig_WithoutProvisionerIsNotConfigured(t *testing.T) {
	e := newTestExtension() // provisioner nil

	status, body := e.processAction(
		configAction(config.OPCommandSetSecret, teetypes.Direct, []byte("x")))
	result := mustResult(t, status, body, http.StatusOK)

	if result.Status != 0 {
		t.Fatalf("expected rejection, got %d", result.Status)
	}
	if result.Log != "error: "+failure.NotConfigured.Error() {
		t.Errorf("log = %q", result.Log)
	}
	if e.lastErrorClass != failure.ClassNotConfigured {
		t.Errorf("class = %q, want %q", e.lastErrorClass, failure.ClassNotConfigured)
	}
}

func TestConfig_UnknownCommandIs501(t *testing.T) {
	e := newProvisionedExtension(&fakeProvisioner{})
	status, _ := e.processAction(configAction(toHash("NOT_A_COMMAND"), teetypes.Direct, []byte("x")))
	if status != http.StatusNotImplemented {
		t.Errorf("status = %d, want 501", status)
	}
}

// --- readiness in /state ---

func TestState_ReportsReadiness(t *testing.T) {
	e := newProvisionedExtension(&fakeProvisioner{
		readiness: types.Readiness{
			EndpointsSet: true, Ready: false,
			MissingRefs: []string{"hex_api_key"},
		},
	})

	rr := doStateRequest(t, e)
	var resp types.StateResponse
	if err := json.Unmarshal(rr, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}

	if !resp.State.Readiness.EndpointsSet {
		t.Error("endpointsSet should be reported")
	}
	if resp.State.Readiness.Ready {
		t.Error("ready should be false")
	}
	if len(resp.State.Readiness.MissingRefs) != 1 {
		t.Errorf("missing refs = %v", resp.State.Readiness.MissingRefs)
	}
}

// Readiness is published on a world-readable, attested surface. It may name
// which secrets are outstanding — that is the operational value — but never
// their values.
func TestState_ReadinessNamesRefsNotValues(t *testing.T) {
	const secret = "SUPERSECRETVALUE"

	e := newProvisionedExtension(&fakeProvisioner{
		readiness: types.Readiness{
			EndpointsSet: true,
			MissingRefs:  []string{"hex_api_key", "ankr_eth_key"},
		},
	})

	body := doStateRequest(t, e)
	if strings.Contains(string(body), secret) {
		t.Errorf("state leaks a secret value: %s", body)
	}

	var resp types.StateResponse
	if err := json.Unmarshal(body, &resp); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	// Refs must be identifiers, not URLs or values.
	for _, ref := range resp.State.Readiness.MissingRefs {
		if strings.Contains(ref, "://") || strings.Contains(ref, " ") {
			t.Errorf("ref %q does not look like an identifier", ref)
		}
	}
}
