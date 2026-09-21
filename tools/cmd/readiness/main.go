// Command readiness prints one machine's signed readiness report as JSON on
// stdout: the provisioning block (configuration hashes, ready flag, missing
// secret refs) and the poller's own view (observation age, whether it would
// sign, its counters, source health), with the signer and the request id the
// signature is bound to. It is how the operator scripts read a machine; the
// machine publishes nothing else about itself.
//
//	go run ./cmd/readiness -p https://proxy.example -directApiKey KEY
//	go run ./cmd/readiness -extension http://127.0.0.1:7702     # a dev server, no proxy
//
// -p asks over the proxy's direct channel and needs the proxy's key. -extension
// posts the action to an extension's own /action the way tee-node would, for
// the development harness (mocknode and devserver), which has no proxy.
package main

import (
	"bytes"
	"context"
	"crypto/rand"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/types"
	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	teetypes "github.com/flare-foundation/tee-node/pkg/types"
	"github.com/pkg/errors"
)

// output is the report as printed: the payload's fields flattened, with the
// two bindings a reader may want to check.
type output struct {
	Signer        string `json:"signer"`
	InstructionID string `json:"instructionId"`
	types.ReadinessReport
}

func main() {
	pf := flag.String("p", configs.ExtensionProxyURL, "the machine's proxy url")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for the direct channel (default: $DIRECT_API_KEY)")
	extF := flag.String("extension", "",
		"post to this extension's own /action instead of a proxy (the development harness)")
	timeoutF := flag.Duration("timeout", 30*time.Second, "how long to wait for the answer")
	flag.Parse()

	// stdout carries the JSON and nothing else; the shared logger writes there.
	logger.Set(logger.Config{Level: "ERROR", Console: true})

	ctx, cancel := context.WithTimeout(context.Background(), *timeoutF)
	defer cancel()

	var (
		report readinessreport.Report
		err    error
	)
	if *extF != "" {
		report, err = queryExtension(ctx, *extF)
	} else {
		report, err = keeper.QueryReadiness(ctx, *pf, *apiKeyF, *timeoutF)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, "readiness:", err)
		os.Exit(1)
	}

	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(output{
		Signer:          report.Signer.Hex(),
		InstructionID:   report.InstructionID.Hex(),
		ReadinessReport: report.Payload,
	}); err != nil {
		fmt.Fprintln(os.Stderr, "readiness: writing the report:", err)
		os.Exit(1)
	}
}

// queryExtension raises GET_READINESS on an extension directly, in the action
// envelope tee-node delivers, and verifies the answer the way the proxy path
// does. Development only: a production extension listens on loopback inside
// its VM and is reached through its proxy.
func queryExtension(ctx context.Context, extensionURL string) (readinessreport.Report, error) {
	var id common.Hash
	if _, err := rand.Read(id[:]); err != nil {
		return readinessreport.Report{}, errors.Errorf("drawing an action id: %s", err)
	}
	// The pinned one-byte envelope, matching internal/protocol.ReadinessRequestV1.
	di, err := json.Marshal(teetypes.DirectInstruction{
		OPType:    protocol.OPTypeTeeOracle,
		OPCommand: protocol.OPCommandGetReadiness,
		Message:   hexutil.Bytes{0x01},
	})
	if err != nil {
		return readinessreport.Report{}, errors.Errorf("encoding the instruction: %s", err)
	}
	body, err := json.Marshal(teetypes.Action{Data: teetypes.ActionData{
		ID: id, Type: teetypes.Direct, SubmissionTag: teetypes.Submit, Message: di,
	}})
	if err != nil {
		return readinessreport.Report{}, errors.Errorf("encoding the action: %s", err)
	}

	req, err := http.NewRequestWithContext(ctx, http.MethodPost,
		strings.TrimRight(extensionURL, "/")+"/action", bytes.NewReader(body))
	if err != nil {
		return readinessreport.Report{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	// Bounded rather than DefaultClient: the extension URL is an operator
	// argument and may accept the connection without ever answering.
	resp, err := (&http.Client{Timeout: 30 * time.Second}).Do(req)
	if err != nil {
		return readinessreport.Report{}, errors.Errorf("posting to the extension: %s", err)
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return readinessreport.Report{}, errors.Errorf("reading the extension's answer: %s", err)
	}
	if resp.StatusCode != http.StatusOK {
		return readinessreport.Report{}, errors.Errorf("the extension answered %d: %s",
			resp.StatusCode, strings.TrimSpace(string(raw)))
	}
	var result teetypes.ActionResult
	if err := json.Unmarshal(raw, &result); err != nil {
		return readinessreport.Report{}, errors.Errorf("decoding the action result: %s", err)
	}
	if result.Status != 1 {
		return readinessreport.Report{}, errors.Errorf(
			"the extension refused the readiness request (status %d): %s", result.Status, result.Log)
	}
	report, err := readinessreport.Decode(result.Data)
	if err != nil {
		return readinessreport.Report{}, err
	}
	if report.InstructionID != id {
		return readinessreport.Report{}, errors.Errorf(
			"readiness answers action %s, not the one asked (%s)", report.InstructionID.Hex(), id.Hex())
	}
	return report, nil
}
