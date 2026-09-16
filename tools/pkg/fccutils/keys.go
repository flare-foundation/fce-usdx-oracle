package fccutils

import (
	"crypto/ecdsa"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// OverrideKey is the private key named by an environment variable, or nil when
// the variable is unset or empty. It exists because the deployment's roles do
// not have to be one key: the extension owner publishes the contracts and the
// allowed machine owners, the machine owner registers the machines, and the
// sender's governance publishes configurations. Each tool takes the deployment
// key by default and the role's own key through its variable —
// EXTENSION_OWNER_KEY and MACHINE_OWNER_KEY — so a deployment that separates
// them needs no flags.
//
// The name appears in the error, because a key that does not parse is a typo in
// a file the operator cannot read back.
func OverrideKey(name string) (*ecdsa.PrivateKey, error) {
	raw := strings.TrimSpace(os.Getenv(name))
	if raw == "" {
		return nil, nil
	}
	raw = strings.TrimPrefix(strings.TrimPrefix(raw, "0x"), "0X")
	key, err := crypto.HexToECDSA(raw)
	if err != nil {
		return nil, errors.Errorf("%s: %s", name, err)
	}
	return key, nil
}
