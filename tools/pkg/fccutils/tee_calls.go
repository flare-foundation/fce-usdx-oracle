package fccutils

import (
	"encoding/json"
	"net/http"
	"time"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	csigning "github.com/flare-foundation/go-flare-common/pkg/signing"

	"github.com/ethereum/go-ethereum/accounts"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/tee/attestation/googlecloud"
	"github.com/flare-foundation/tee-node/pkg/attestation"
	"github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

const repeats = 15

func TeeInfo(nodeURL string) (*types.SignedTeeInfoResponse, error) {
	result, err := http.Get(nodeURL + "/info")
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}
	defer result.Body.Close()

	var teeInfo types.SignedTeeInfoResponse
	err = json.NewDecoder(result.Body).Decode(&teeInfo)
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}

	return &teeInfo, nil
}

func CodeHashAndPlatform(attestationString string) (common.Hash, common.Hash, error) {
	claims := attestation.NeededClaims{}
	_, _, err := googlecloud.ParsePKITokenUnverifiedClaims(attestationString, &claims)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("%s", err)
	}

	codeHash, err := claims.CodeHash()
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("%s", err)
	}
	platform, err := claims.Platform()
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("%s", err)
	}

	return codeHash, platform, nil
}

func TeeProxyId(teeInfo *types.SignedTeeInfoResponse) (common.Address, common.Address, error) {
	pubKey, err := types.ParsePubKey(teeInfo.TeeInfo.PublicKey)
	if err != nil {
		return common.Address{}, common.Address{}, errors.Errorf("%s", err)
	}

	teeID := crypto.PubkeyToAddress(*pubKey)

	hash, err := teeInfo.TeeInfo.Hash()
	if err != nil {
		return common.Address{}, common.Address{}, errors.Errorf("%s", err)
	}
	// The proxy signs over Payload{ProxyTeeInfo, chainID, infoHash}. Recover
	// against the SAME preimage, or proxyId comes out garbage and the on-chain
	// availability check is rejected with "proxy signer does not match".
	infoSignHash, err := csigning.NewPayload(csigning.ProxyTeeInfo, teeInfo.TeeInfo.ChainID, common.BytesToHash(hash)).Hash()
	if err != nil {
		return common.Address{}, common.Address{}, errors.Errorf("%s", err)
	}
	proxyPubKey, err := crypto.SigToPub(accounts.TextHash(infoSignHash[:]), teeInfo.ProxySignature)
	if err != nil {
		return common.Address{}, common.Address{}, errors.Errorf("%s", err)
	}
	proxyID := crypto.PubkeyToAddress(*proxyPubKey)

	return teeID, proxyID, nil
}

func ActionResult(nodeURL string, actionID common.Hash) (*types.ActionResponse, error) {
	var result *http.Response
	var err error
	for range repeats {
		result, err = http.Get(nodeURL + "/action/result/" + actionID.Hex())
		if err == nil && result.StatusCode == http.StatusOK {
			break
		}
		time.Sleep(2 * time.Second)
	}
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}
	if result.StatusCode != http.StatusOK {
		logger.Warnf("action result status not ok: got: %d for %s, %s", result.StatusCode, actionID.Hex(), nodeURL)
		return nil, errors.Errorf("action result status not ok, got: %d", result.StatusCode)
	}
	defer result.Body.Close()

	var response types.ActionResponse
	err = json.NewDecoder(result.Body).Decode(&response)
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}

	return &response, nil
}
