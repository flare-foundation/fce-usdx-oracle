package fccutils

import (
	"encoding/hex"
	"os"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

func GetTeeProxyID(proxyURL string) (common.Address, common.Address, error) {
	teeInfo, err := TeeInfo(proxyURL)
	if err != nil {
		return common.Address{}, common.Address{}, err
	}

	teeID, proxyID, err := TeeProxyId(teeInfo)
	if err != nil {
		return common.Address{}, common.Address{}, err
	}

	return teeID, proxyID, nil
}

func GetCodeHashAndPlatform(teeInfo *types.SignedTeeInfoResponse) (common.Hash, common.Hash, error) {
	simulatedTee := os.Getenv("SIMULATED_TEE") == "true"
	codeHash := TeeCodeHash
	platform := TestPlatform
	var err error

	// A simulated machine's code hash is compiled into tee-node, and during a rolling
	// upgrade two builds are live at once — so SIMULATED_CODE_HASH names the one being
	// addressed. Simulated only: under real attestation the hash comes from the token.
	if simulatedTee {
		if override := os.Getenv("SIMULATED_CODE_HASH"); override != "" {
			if !isHash32(override) {
				return common.Hash{}, common.Hash{}, errors.Errorf(
					"SIMULATED_CODE_HASH must be 32 bytes of hex, got %q", override)
			}
			codeHash = common.HexToHash(override)
		}
	}
	if !simulatedTee {
		codeHash, platform, err = CodeHashAndPlatform(teeInfo.Attestation)
		if err != nil {
			return common.Hash{}, common.Hash{}, err
		}
	}

	if codeHash != teeInfo.MachineData.CodeHash {
		return common.Hash{}, common.Hash{}, errors.Errorf("code hashes do not match: %s, %s", codeHash, teeInfo.MachineData.CodeHash)
	}
	if platform != teeInfo.MachineData.Platform {
		return common.Hash{}, common.Hash{}, errors.Errorf("platforms do not matc: %s, %s", platform, teeInfo.MachineData.Platform)
	}

	return codeHash, platform, nil
}

// isHash32 reports whether s is exactly 32 bytes of hex, with or without 0x.
// common.HexToHash silently truncates and left-pads anything else, which would
// turn a typo into a valid-looking hash for a build nobody is running.
func isHash32(s string) bool {
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if len(s) != 64 {
		return false
	}
	_, err := hex.DecodeString(s)
	return err == nil
}
