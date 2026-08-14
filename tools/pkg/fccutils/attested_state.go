package fccutils

import "github.com/pkg/errors"

// ErrNoAttestedState reports that the proxy's GET /info carried an empty
// extension state, so what the machine is running cannot be read at all.
//
// This is NOT the same as the machine reporting a hash that differs from the
// one just published, and collapsing the two is actively misleading: a
// difference means the machine rejected or never received the instruction,
// while an empty state means the question went unanswered. Callers that treat
// the second as the first tell an operator their config failed when it applied
// cleanly.
//
// The state reaches /info because tee-node polls the extension's GET /state and
// folds the body into the attested payload — which is what makes it worth
// reading rather than querying the extension directly: inside /info it is
// covered by dataSignature and the attestation, so it is the enclave's signed
// claim about itself rather than whatever happens to answer a port. Versions
// that do not implement that poll publish nothing here, and no amount of
// waiting will change that, so callers should stop rather than spend their
// budget.
//
// Report it at INFO, not WARN: on a deployment whose tee-node cannot publish
// state it is true on every single run, and a warning that always fires is one
// operators learn to skip — which costs more than it saves the day a real
// mismatch appears.
var ErrNoAttestedState = errors.New(
	"the proxy published no attested extension state (this tee-node version does not fold " +
		"GET /state into /info), so what the machine is running cannot be confirmed this way")
