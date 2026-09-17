package fccutils

import (
	"encoding/json"
	"io"
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

// nodeTimeout bounds one request to a tee-node or its proxy. The URL is an
// operator argument and may name a host that accepts the connection and never
// answers, which would otherwise hold these commands forever.
const nodeTimeout = 10 * time.Second

// maxBody caps a node response before it is decoded; real payloads are a few
// kilobytes.
const maxBody = 1 << 20

var nodeClient = &http.Client{Timeout: nodeTimeout}

func TeeInfo(nodeURL string) (*types.SignedTeeInfoResponse, error) {
	result, err := nodeClient.Get(nodeURL + "/info")
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}
	defer func() { _ = result.Body.Close() }()

	var teeInfo types.SignedTeeInfoResponse
	err = json.NewDecoder(io.LimitReader(result.Body, maxBody)).Decode(&teeInfo)
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
		result, err = nodeClient.Get(nodeURL + "/action/result/" + actionID.Hex())
		if err == nil && result.StatusCode == http.StatusOK {
			break
		}
		if result != nil {
			// Every non-terminal answer holds a connection until its body is
			// closed; the retry loop would otherwise leak one per attempt.
			_ = result.Body.Close()
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
	defer func() { _ = result.Body.Close() }()

	var response types.ActionResponse
	err = json.NewDecoder(io.LimitReader(result.Body, maxBody)).Decode(&response)
	if err != nil {
		return nil, errors.Errorf("%s", err)
	}

	return &response, nil
}
