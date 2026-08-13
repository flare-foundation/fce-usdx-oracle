package fccutils

import (
	"bytes"
	"context"
	"encoding/hex"
	"errors"
	"fmt"
	"math/big"
	"strings"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/ethclient"
)

// DecodeRevertReason extracts the revert reason from an eth_call or
// eth_estimateGas error, or "" when no revert data could be extracted.
func DecodeRevertReason(err error) string {
	if err == nil {
		return ""
	}

	// ErrorData() carries the ABI-encoded revert reason even when the message
	// is just "execution reverted".
	type dataError interface {
		ErrorData() interface{}
	}

	var de dataError
	if errors.As(err, &de) {
		if data := de.ErrorData(); data != nil {
			if hexStr, ok := data.(string); ok {
				return decodeRevertHex(hexStr)
			}
		}
	}

	return ""
}

// SimulateAndDecodeRevert replays a call via eth_call to decode the revert
// reason. A fallback for nodes that strip revert data from eth_estimateGas
// errors but include it in eth_call responses.
func SimulateAndDecodeRevert(
	client *ethclient.Client,
	from common.Address,
	to common.Address,
	value *big.Int,
	data []byte,
) string {
	toAddr := to
	msg := ethereum.CallMsg{
		From:  from,
		To:    &toAddr,
		Value: value,
		Data:  data,
	}

	result, err := client.CallContract(context.Background(), msg, nil)
	if err != nil {
		if reason := DecodeRevertReason(err); reason != "" {
			return reason
		}
		// Return the raw error message as a last resort
		return err.Error()
	}

	// Some nodes return the ABI-encoded revert data in the result
	// bytes instead of as an error
	if len(result) >= 4 {
		return decodeRevertHex(hex.EncodeToString(result))
	}

	return ""
}

// MatchCustomError maps a raw revert payload to the custom error in parsedABI
// whose 4-byte selector matches, appending arguments when present. Returns ""
// when nothing matches.
func MatchCustomError(parsedABI *abi.ABI, revertHex string) string {
	revertHex = strings.TrimPrefix(revertHex, "0x")
	data, err := hex.DecodeString(revertHex)
	if err != nil || len(data) < 4 {
		return ""
	}
	selector := data[:4]
	for name, e := range parsedABI.Errors {
		if !bytes.Equal(e.ID.Bytes()[:4], selector) {
			continue
		}
		if vals, uerr := e.Unpack(data); uerr == nil {
			if argStr := fmt.Sprintf("%v", vals); argStr != "[]" && argStr != "<nil>" {
				return name + argStr
			}
		}
		return name
	}
	return ""
}

func decodeRevertHex(hexStr string) string {
	hexStr = strings.TrimPrefix(hexStr, "0x")
	decoded, err := hex.DecodeString(hexStr)
	if err != nil || len(decoded) < 4 {
		return ""
	}

	// Try to decode as Error(string) — selector 0x08c379a2
	if reason, unpackErr := abi.UnpackRevert(decoded); unpackErr == nil {
		return reason
	}

	// Return raw hex for custom errors we can't decode
	return "0x" + hexStr
}
