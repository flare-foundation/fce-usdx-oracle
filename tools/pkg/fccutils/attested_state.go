package fccutils

import "github.com/pkg/errors"

// ErrNoAttestedState reports that GET /info carried an empty extension state, so what
// the machine runs cannot be read. NOT a differing hash — that is a rejected
// instruction, this is an unanswered question. Waiting will not help; report at INFO.
var ErrNoAttestedState = errors.New(
	"the proxy published no attested extension state (this tee-node version does not fold " +
		"GET /state into /info), so what the machine is running cannot be confirmed this way")
