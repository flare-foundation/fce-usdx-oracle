// Package eip191 recovers the signer of a personal-sign signature over a
// message body, the scheme the provisioning envelope and the readiness report
// are signed under so that a hardware wallet can produce either:
//
//	digest = keccak256("\x19Ethereum Signed Message:\n32" ‖ keccak256(body))
package eip191

import (
	"math/big"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// SignatureLength is r ‖ s ‖ v.
const SignatureLength = 65

// Recover returns the address that signed body, or false for anything that is
// not a signature over it. v is accepted as 27/28, the standard produced by
// personal-sign tooling and hardware wallets, and as the raw recovery id 0/1
// some signers emit. The values are checked with go-ethereum's own rule,
// homestead set, so the high-s form is rejected and one signature means one
// thing; the hand-rolled low-s tests this replaces admitted r or s of zero and
// either at or above the curve order.
func Recover(body, sig []byte) (common.Address, bool) {
	if len(sig) != SignatureLength {
		return common.Address{}, false
	}

	normalised := make([]byte, SignatureLength)
	copy(normalised, sig)
	switch normalised[64] {
	case 27, 28:
		normalised[64] -= 27
	case 0, 1:
	default:
		return common.Address{}, false
	}

	r := new(big.Int).SetBytes(normalised[:32])
	s := new(big.Int).SetBytes(normalised[32:64])
	if !crypto.ValidateSignatureValues(normalised[64], r, s, true) {
		return common.Address{}, false
	}

	pub, err := crypto.SigToPub(accounts.TextHash(crypto.Keccak256(body)), normalised)
	if err != nil || pub == nil {
		return common.Address{}, false
	}
	return crypto.PubkeyToAddress(*pub), true
}
