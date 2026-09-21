// Package direct speaks the proxy's direct channel: POST /direct enqueues an
// action for the machine, GET /action/result/<id> reads the machine's own
// verdict on it, and, for the development harness only, POST /action hands an
// action straight to an extension in the envelope tee-node would deliver.
//
// The direct channel carries what must not go on chain: a readiness query,
// which is stale within minutes and not worth a fee, and a SET_SECRET envelope,
// whose ciphertext would otherwise be permanent and public. A price is always
// an on-chain instruction.
package direct

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/ethereum/go-ethereum/common"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// Timeout bounds one request. Every URL here is an operator argument or one the
// registry publishes, each chosen by that machine's owner, so an endpoint that
// accepts the connection and never answers costs this and no more.
const Timeout = 10 * time.Second

// MaxBody caps a response before it is decoded. Real results are a few
// kilobytes; the cap is what stops an endless body from growing the caller
// without bound.
const MaxBody = 1 << 20

// Terminal action statuses. Anything else means still in flight.
const (
	StatusFailed    uint8 = 0
	StatusSucceeded uint8 = 1
)

// client bounds every phase of a request, including the ones a context deadline
// alone does not cover once a body has started arriving.
var client = &http.Client{Timeout: Timeout}

// Post enqueues one direct action on a proxy and returns the id it assigned,
// which is what its result is filed under. The op pair is taken loose rather
// than read off the instruction sender: the direct commands have no on-chain
// counterpart to read it from.
func Post(
	ctx context.Context, proxyURL, apiKey string,
	opType, opCommand common.Hash, message []byte,
) (common.Hash, error) {
	// A DirectInstruction, three fields with the payload under "message": what
	// the proxy forwards verbatim. A DataFixed does not fail here; the
	// extension just sees an empty payload.
	body, err := json.Marshal(teetypes.DirectInstruction{
		OPType: opType, OPCommand: opCommand, Message: message,
	})
	if err != nil {
		return common.Hash{}, errors.Errorf("encoding the direct instruction: %s", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(proxyURL, "/")+"/direct", bytes.NewReader(body))
	if err != nil {
		return common.Hash{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	if apiKey != "" {
		req.Header.Set("X-API-Key", apiKey)
	}

	resp, err := client.Do(req)
	if err != nil {
		return common.Hash{}, errors.Errorf("posting to the proxy: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()

	out, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<16))
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		// The body is the proxy's own error text; 401 here means the API key,
		// not the extension.
		return common.Hash{}, errors.Errorf("proxy returned %d: %s",
			resp.StatusCode, strings.TrimSpace(string(out)))
	}

	// The proxy echoes the action it enqueued, which is where the id comes from.
	var action teetypes.Action
	if err := json.Unmarshal(out, &action); err != nil {
		return common.Hash{}, errors.Errorf("decoding the proxy response: %s", err)
	}
	if action.Data.ID == (common.Hash{}) {
		return common.Hash{}, errors.New("the proxy returned no action id")
	}
	return action.Data.ID, nil
}

// Result reads one action's result off a proxy and reports whether it is
// terminal. A 404 is the normal state until tee-node has answered. The request
// carries the caller's cancellation, the client bounds how long it may take,
// and the body is capped before it is decoded.
func Result(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
) (teetypes.ActionResult, bool, error) {
	url := strings.TrimRight(proxyURL, "/") +
		"/action/result/" + id.Hex() + "?submissionTag=" + string(tag)

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, url, nil)
	if err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("building the proxy request: %s", err)
	}

	resp, err := client.Do(req)
	if err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("querying the proxy: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()

	if resp.StatusCode != http.StatusOK {
		return teetypes.ActionResult{}, false, errors.Errorf("proxy returned %d", resp.StatusCode)
	}

	var body teetypes.ActionResponse
	if err := json.NewDecoder(io.LimitReader(resp.Body, MaxBody)).Decode(&body); err != nil {
		return teetypes.ActionResult{}, false, errors.Errorf("decoding the proxy response: %s", err)
	}

	status := body.Result.Status
	return body.Result, status == StatusSucceeded || status == StatusFailed, nil
}

// initialBackoff and maxBackoff pace Await's rounds: a result that has not
// landed yet reads as a 404 on every round until it does.
var initialBackoff = time.Second

const maxBackoff = 16 * time.Second

// Await polls one proxy until the action's result is terminal or timeout
// passes; the caller's cancellation ends it early. The wait is what turns "the
// proxy accepted it" into "the machine answered": a state read straight after a
// submit races the machine otherwise. Direct results are filed under
// teetypes.Submit; the wrong tag 404s for the whole window rather than erroring.
func Await(
	ctx context.Context, proxyURL string, id common.Hash, tag teetypes.SubmissionTag,
	timeout time.Duration,
) (teetypes.ActionResult, error) {
	deadline := time.Now().Add(timeout)
	backoff := initialBackoff
	// What the proxy last said, for the failure line.
	var last string
	for {
		res, terminal, err := Result(ctx, proxyURL, id, tag)
		switch {
		case err != nil:
			last = err.Error()
		case terminal:
			return res, nil
		default:
			last = fmt.Sprintf("pending (status %d)", res.Status)
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return teetypes.ActionResult{}, errors.Errorf(
				"no terminal result for %s within %s (%s)", id.Hex(), timeout, last)
		}
		if backoff > remaining {
			backoff = remaining
		}
		select {
		case <-ctx.Done():
			return teetypes.ActionResult{}, ctx.Err()
		case <-time.After(backoff):
		}
		if backoff < maxBackoff {
			backoff *= 2
		}
	}
}

// PostToExtension hands one direct action straight to an extension's /action
// and returns the id it was given and the extension's answer. Development
// only: a production extension listens on loopback inside its VM and is
// reached through its proxy. The action is typed Direct so a command the
// extension refuses over the instruction channel is refused here too, and its
// message is the DirectInstruction the proxy really sends, never a DataFixed.
func PostToExtension(
	ctx context.Context, extensionURL string,
	opType, opCommand common.Hash, message []byte,
) (common.Hash, teetypes.ActionResult, error) {
	var id common.Hash
	if _, err := rand.Read(id[:]); err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("drawing an action id: %s", err)
	}
	instruction, err := json.Marshal(teetypes.DirectInstruction{
		OPType: opType, OPCommand: opCommand, Message: message,
	})
	if err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("encoding the instruction: %s", err)
	}
	body, err := json.Marshal(teetypes.Action{Data: teetypes.ActionData{
		ID: id, Type: teetypes.Direct, SubmissionTag: teetypes.Submit, Message: instruction,
	}})
	if err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("encoding the action: %s", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(extensionURL, "/")+"/action", bytes.NewReader(body))
	if err != nil {
		return common.Hash{}, teetypes.ActionResult{}, err
	}
	req.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(req)
	if err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("posting to the extension: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, MaxBody))
	if err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("reading the extension's answer: %s", err)
	}
	if resp.StatusCode != http.StatusOK {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("the extension answered %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result teetypes.ActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return common.Hash{}, teetypes.ActionResult{}, errors.Errorf("decoding the action result: %s", err)
	}
	return id, result, nil
}
