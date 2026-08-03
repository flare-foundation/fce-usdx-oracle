package runtimecfg

import (
	"context"
	"encoding/json"
	"errors"
	"io"

	"fce-usdx-oracle/internal/failure"
	"fce-usdx-oracle/internal/provision"
	"fce-usdx-oracle/pkg/types"

	"github.com/ethereum/go-ethereum/common"
)

// Node is the subset of tee-node this package needs.
type Node interface {
	Decrypt(ctx context.Context, ciphertext []byte) ([]byte, error)

	// TeeID is this machine's own address, which envelopes are bound to.
	TeeID(ctx context.Context) (common.Address, error)
}

// Provisioner applies verified configuration to a Store.
//
// It is the seam between "is this message authorised?" (package provision) and
// "what does it configure?" (this package). Keeping them apart means the
// signature rules can be audited without reading endpoint-assembly code, and
// the reverse.
type Provisioner struct {
	store    *Store
	verifier *provision.Verifier
	node     Node
}

// NewProvisioner wires the pieces together.
func NewProvisioner(store *Store, verifier *provision.Verifier, node Node) *Provisioner {
	return &Provisioner{store: store, verifier: verifier, node: node}
}

// ApplyEndpoints installs an endpoint configuration.
//
// No envelope: authorisation for endpoints is the owner-only check in the
// on-chain instruction sender, and the extension has already established that
// this arrived over the instruction channel. What is checked here is content —
// above all that every host is in the baked allowlist.
func (p *Provisioner) ApplyEndpoints(payload []byte) error {
	if len(payload) == 0 {
		return errors.New("runtimecfg: empty endpoint payload")
	}

	var e Endpoints
	dec := json.NewDecoder(newLimitedReader(payload))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		// An unrecognised field means the on-chain publisher and this build
		// disagree about the schema. Stopping is safer than applying the subset
		// we happen to understand.
		return errors.New("runtimecfg: endpoint payload is not a known configuration")
	}

	return p.store.SetEndpoints(e)
}

// ApplySecret verifies a signed envelope and installs the credential.
//
// The envelope's payload is a ciphertext encrypted to this TEE's public key, so
// the plaintext exists only inside the enclave and only in memory.
func (p *Provisioner) ApplySecret(ctx context.Context, message []byte) error {
	// Resolve this machine's identity lazily: tee-node may not be reachable when
	// the extension starts, and provisioning is the first moment the answer is
	// actually needed.
	if err := p.ensureTeeID(ctx); err != nil {
		return failure.ProvisionRejected
	}

	env, err := p.verifier.Verify(message)
	if err != nil {
		return err
	}

	plaintext, err := p.node.Decrypt(ctx, env.Payload)
	if err != nil {
		// Collapsed to the single provisioning reason: whether decryption failed
		// is itself a signal about which TEE holds which key.
		return failure.ProvisionRejected
	}

	// The plaintext is a credential. It is never logged, never wrapped into an
	// error, and never returned.
	if err := p.store.SetSecret(env.Ref, string(plaintext)); err != nil {
		return failure.ProvisionRejected
	}
	return nil
}

// ensureTeeID binds the verifier to this machine on first use.
func (p *Provisioner) ensureTeeID(ctx context.Context) error {
	if p.verifier.HasTeeID() {
		return nil
	}
	teeID, err := p.node.TeeID(ctx)
	if err != nil {
		return err
	}
	p.verifier.SetTeeID(teeID)
	return nil
}

// Readiness reports outstanding provisioning in the shape GET /state publishes.
func (p *Provisioner) Readiness() types.Readiness {
	r := p.store.Readiness()
	return types.Readiness{
		EndpointsSet: r.EndpointsSet,
		SecretsSet:   r.SecretsSet,
		Ready:        r.Ready,
		MissingRefs:  r.MissingRefs,
	}
}

// maxPayloadBytes caps an endpoint configuration. The real one is a few
// kilobytes; this bounds a hostile publisher.
const maxPayloadBytes = 256 << 10

type limitedReader struct {
	data []byte
	pos  int
}

func newLimitedReader(b []byte) *limitedReader {
	if len(b) > maxPayloadBytes {
		b = b[:maxPayloadBytes]
	}
	return &limitedReader{data: b}
}

func (r *limitedReader) Read(p []byte) (int, error) {
	if r.pos >= len(r.data) {
		return 0, io.EOF
	}
	n := copy(p, r.data[r.pos:])
	r.pos += n
	return n, nil
}
