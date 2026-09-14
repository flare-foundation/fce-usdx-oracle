package fccutils

import (
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/pkg/errors"
)

// ErrNoAttestedState reports that GET /info carried no extension state at all,
// so what the machine runs cannot be read this way. NOT a differing hash — that
// is a rejected instruction, this is an unanswered question. Waiting will not
// help: a tee-node that folds the state in does so on every info request, so an
// empty field means this tee-node does not.
var ErrNoAttestedState = errors.New(
	"the proxy published no extension state (this tee-node does not fold GET /state " +
		"into /info), so what the machine is running cannot be confirmed this way")

// ErrStateUnavailable reports that tee-node tried to read its extension's
// GET /state and could not: the state version is tee-node's all-ones sentinel.
// Unlike ErrNoAttestedState this clears on its own once the extension answers.
var ErrStateUnavailable = errors.New(
	"tee-node could not read its extension's state (the extension was down or " +
		"answered badly), so what the machine is running cannot be confirmed right now")

// stateUnavailableVersion is tee-node's types.StateUnavailableVersion, declared
// here so the tools build against the pinned tee-node as well as a newer one.
var stateUnavailableVersion = common.MaxHash

// ReadExtensionState returns the extension's state as JSON, read out of the
// proxy's GET /info. tee-node fetches the extension's GET /state on every info
// request and carries the state bytes, attested, in teeInfo.state.state; the
// bytes are the JSON encoding of the extension's State. This is the one way the
// tools read the state: the extension's own port is loopback-only inside the
// machine.
func ReadExtensionState(proxyURL string) (json.RawMessage, error) {
	client := &http.Client{Timeout: 10 * time.Second}
	resp, err := client.Get(strings.TrimRight(proxyURL, "/") + "/info")
	if err != nil {
		return nil, errors.Errorf("querying the proxy: %s", err)
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return nil, errors.Errorf("proxy returned %d", resp.StatusCode)
	}

	var info struct {
		TeeInfo struct {
			State struct {
				State        hexutil.Bytes `json:"state"`
				StateVersion common.Hash   `json:"stateVersion"`
			} `json:"state"`
		} `json:"teeInfo"`
	}
	if err := json.NewDecoder(io.LimitReader(resp.Body, 1<<20)).Decode(&info); err != nil {
		return nil, errors.Errorf("decoding /info: %s", err)
	}
	if info.TeeInfo.State.StateVersion == stateUnavailableVersion {
		return nil, ErrStateUnavailable
	}
	if len(info.TeeInfo.State.State) == 0 {
		return nil, ErrNoAttestedState
	}
	if !json.Valid(info.TeeInfo.State.State) {
		return nil, errors.New("the extension state in /info is not JSON")
	}
	return json.RawMessage(info.TeeInfo.State.State), nil
}
