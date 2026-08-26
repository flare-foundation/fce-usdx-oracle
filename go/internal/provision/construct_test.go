// This file is an EXTERNAL test package: internal/provision declares its own
// recover(digest, sig), which shadows the builtin, so a panic cannot be
// asserted from inside the package.
package provision_test

import (
	"math/big"
	"testing"

	"fce-usdx-oracle/internal/provision"

	"github.com/ethereum/go-ethereum/common"
)

// TestNewVerifierRejectsZeroTeeID pins the construction invariant: the machine
// binding is the only check tying a credential to one enclave, and a zero teeID
// would match an envelope whose binding was never filled in.
func TestNewVerifierRejectsZeroTeeID(t *testing.T) {
	defer func() {
		if recover() == nil {
			t.Fatal("NewVerifier accepted a zero teeID")
		}
	}()
	provision.NewVerifier(big.NewInt(114), common.Address{}, []string{"hex_api_key"})
}
