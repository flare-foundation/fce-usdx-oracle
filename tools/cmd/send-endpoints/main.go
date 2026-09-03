// Command send-endpoints publishes an endpoint configuration on-chain through
// TeeOracleInstructionsSender, so the addressed TEE machines receive it as a
// SET_ENDPOINTS instruction.
//
// The config is ABI-encoded by the sender into Endpoints{version, feedId,
// groups}; this tool takes a JSON file for convenience and builds the typed
// group list from it. What lands on-chain — and what EndpointsHash predicts —
// is the ABI form, never the JSON.
//
// Credentials never go this way: the config is calldata and therefore public.
// Secrets travel over the direct channel via usdxprov.
//
// One publication fans out to every machine named with -tee in a single
// transaction, and the fee is operationFee × len(teeIds). The sender assigns
// the version; this tool predicts it (endpointsVersion(feedId) + 1) so it can
// predict the hash every later feed update will have to carry.
//
// In production mode setEndpoints is governance-only: the calldata goes through
// the timelock (executeGovernanceCall) and the executor attaches the fee there.
// This tool's direct Send path only works before switchToProductionMode — for
// local and test deployments. On a real deployment (where the upstream deploy
// script has already switched to production mode) use -out to export the
// governance hand-off — calldata, fee and predicted hash — for the Flare
// governance executor instead of sending.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/oraclepayload"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	instructionSenderF := flag.String("instructionSender", "",
		"TeeOracleInstructionsSender address (default: the TeeOracleInstructionsSender "+
			"entry in the addresses file)")
	configF := flag.String("config", "", "endpoints configuration JSON file")
	feedIDF := flag.String("feedId", "0x20555344582f555344000000000000000000000000",
		"feed id (bytes21 hex); defaults to USDX/USD")
	teeF := flag.String("tee", "",
		"comma-separated TEE machine addresses to publish to (required)")
	claimBackF := flag.String("claimBack", "",
		"address that can claim back unspent fees; zero (default) to skip")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key, used to confirm delivery via a signed GET_READINESS when the "+
			"proxy publishes no attested state (default: $DIRECT_API_KEY)")
	waitF := flag.Duration("wait", 90*time.Second,
		"how long to wait for the machine to report the new config hash; 0 skips the check")
	dryRunF := flag.Bool("dry-run", false, "validate the payload and print the predicted hash, but send nothing")
	encodeF := flag.Bool("encode", false,
		"print the ABI-encoded Endpoints payload for -version and exit, touching no chain: the exact "+
			"bytes setEndpoints would dispatch, for delivering to a dev server's /action directly")
	versionF := flag.Uint64("version", 0,
		"publication version to encode with -encode; on-chain the sender assigns it, so this is only "+
			"for a dev server, whose store requires each publication to be newer than the last")
	outF := flag.String("out", "",
		"write the governance hand-off (calldata, fee, predicted hash) to this JSON file "+
			"instead of sending — the path for production-mode senders, where setEndpoints "+
			"is governance-only and Flare executes it through the timelock")
	flag.Parse()

	if *configF == "" {
		fccutils.FatalWithCause(errors.New("-config is required"))
	}

	feedID, err := parseFeedID(*feedIDF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	configBytes, err := os.ReadFile(*configF)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("reading config file: %s", err))
	}
	if !json.Valid(configBytes) {
		fccutils.FatalWithCause(errors.Errorf("%s is not valid JSON", *configF))
	}

	groups, err := parseEndpoints(configBytes)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}
	if err := validateEndpoints(groups); err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}

	// Chain-less: the same encoder the on-chain path uses, so a dev server is
	// handed the bytes governance would dispatch rather than an approximation.
	if *encodeF {
		if *versionF == 0 {
			fccutils.FatalWithCause(errors.New("-encode needs -version (a dev server's store rejects version 0)"))
		}
		payload, err := oraclepayload.EncodeEndpoints(*versionF, feedID, groups)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("encoding Endpoints: %s", err))
		}
		fmt.Println(hexutil.Encode(payload))
		return
	}

	if *teeF == "" {
		fccutils.FatalWithCause(errors.New("-tee is required (or -encode for a chain-less payload)"))
	}
	instructionSenderAddress, err := configs.ResolveContract(*instructionSenderF, *af, "TeeOracleInstructionsSender")
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the instructions sender: %s — pass -instructionSender "+
			"or run scripts/import-upstream-addresses.sh after the upstream deploy", err))
	}

	teeIDs, err := parseAddresses(*teeF)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("-tee: %s", err))
	}
	if len(teeIDs) == 0 {
		fccutils.FatalWithCause(errors.New("-tee must name at least one machine"))
	}

	var claimBack common.Address
	if *claimBackF != "" {
		if !common.IsHexAddress(*claimBackF) {
			fccutils.FatalWithCause(errors.Errorf("-claimBack %q is not an address", *claimBackF))
		}
		claimBack = common.HexToAddress(*claimBackF)
	}

	logger.Infof("Config from %s, %d group(s):", *configF, len(groups))
	logger.Info(summarise(groups))

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	sender, err := instrutils.Sender(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	currentVersion, err := sender.EndpointsVersion(nil, feedID)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("reading endpointsVersion: %s", err))
	}
	version := currentVersion + 1

	publication, err := instrutils.EncodeSetEndpoints(feedID, teeIDs, groups, claimBack, version)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("encoding setEndpoints: %s", err))
	}
	if err := publication.ResolveFee(testSupport, "SET_ENDPOINTS"); err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the fee: %s", err))
	}

	logger.Infof("Publishing to %d machine(s) for feed %s:", len(teeIDs), *feedIDF)
	for _, id := range teeIDs {
		logger.Infof("  tee: %s", id.Hex())
	}
	logger.Infof("  predicted version:      %d (current %d)", publication.Version, currentVersion)
	logger.Infof("  predicted endpointsHash: %s", publication.PayloadHash.Hex())
	logger.Infof("  fee (× %d machine(s)):   %s wei", len(teeIDs), publication.Fee)

	if *dryRunF {
		logger.Infof("Dry run: nothing sent. The sender assigns the version, so the predicted " +
			"hash is only right if no other SET_ENDPOINTS for this feed lands first.")
		return
	}

	// Governance hand-off: on a real deployment the sender is already in
	// production mode, setEndpoints is governance-only, and Flare governance
	// executes it through the timelock. This repo's contribution is the exact
	// calldata plus the fee, written out for the executor.
	if *outF != "" {
		handoff, err := publication.Handoff(instructionSenderAddress, "setEndpoints")
		if err != nil {
			fccutils.FatalWithCause(err)
		}
		data, err := json.MarshalIndent(handoff, "", "  ")
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("encoding the hand-off: %s", err))
		}
		if err := os.WriteFile(*outF, append(data, '\n'), 0o644); err != nil {
			fccutils.FatalWithCause(errors.Errorf("writing %s: %s", *outF, err))
		}
		logger.Infof("Hand-off written to %s — hand it to the Flare governance executor.", *outF)
		logger.Infof("Nothing was sent: setEndpoints is governance-only once the sender is in production mode.")
		return
	}

	// Direct submission: works only while the caller is governance (before
	// switchToProductionMode). On a real deployment this reverts with the
	// governance guard; the calldata then has to go through executeGovernanceCall.
	txHash, err := publication.Send(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("publishing endpoints: %s", err))
	}

	logger.Infof("Endpoints published on-chain:")
	logger.Infof("  tx:            %s", txHash.Hex())
	logger.Infof("  version:       %d", publication.Version)
	logger.Infof("  endpointsHash: %s", publication.PayloadHash.Hex())

	if *waitF <= 0 {
		// Stated as a conditional, not a fact: whether the machine applied this
		// is exactly what skipping the check leaves unknown.
		logger.Warnf("Skipping the delivery check (-wait 0), so whether the machine applied %s "+
			"is unknown. If it did not, every update it signs is rejected with StaleEndpoints.",
			publication.PayloadHash.Hex())
		return
	}

	// The delivery check confirms against the primary proxy only. With multiple
	// machines use cmd/fleet-status to reconcile every machine's live hash.
	if err := waitForConfig(*pf, *apiKeyF, publication.PayloadHash, *waitF); err != nil {
		if errors.Is(err, fccutils.ErrNoAttestedState) {
			logger.Infof("Delivery not confirmed: %s", err)
			return
		}
		logger.Errorf("%s", err)
		logger.Errorf("The machine is NOT running the published config, so its updates will be " +
			"rejected with StaleEndpoints until it is. Check the extension's logs:")
		logger.Errorf("  docker compose logs extension-tee | grep SET_ENDPOINTS")
		os.Exit(1)
	}

	logger.Infof("Confirmed: the machine reports endpointsHash %s", publication.PayloadHash.Hex())
}

// parseFeedID decodes a bytes21 feed id.
func parseFeedID(s string) ([21]byte, error) {
	var id [21]byte
	raw := common.FromHex(s)
	if len(raw) != 21 {
		return id, errors.Errorf("feedId %q is not 21 bytes", s)
	}
	copy(id[:], raw)
	return id, nil
}

// parseAddresses splits a comma-separated list of hex addresses.
func parseAddresses(s string) ([]common.Address, error) {
	var out []common.Address
	for _, raw := range strings.Split(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !common.IsHexAddress(raw) {
			return nil, errors.Errorf("%q is not an address", raw)
		}
		out = append(out, common.HexToAddress(raw))
	}
	return out, nil
}

// waitForConfig polls the machine's own /state until it reports the hash just
// published, or the budget runs out.
func waitForConfig(proxyURL, apiKey string, want common.Hash, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := 2 * time.Second

	var last string
	for {
		got, err := fetchEndpointsHash(proxyURL)
		switch {
		case errors.Is(err, fccutils.ErrNoAttestedState):
			// Ask the machine instead of giving up. GET_READINESS carries the
			// same hash, signed, over a channel that does not depend on
			// tee-node folding /state into /info.
			if h, qerr := queryEndpointsHash(proxyURL, apiKey, budget); qerr == nil {
				got = h
				if strings.EqualFold(got, want.Hex()) {
					return nil
				}
				last = "the machine reports " + shortOrNone(got)
				break
			} else if apiKey != "" {
				last = "readiness query failed: " + qerr.Error()
				break
			}
			// Nothing will appear later: this tee-node never publishes state and there
			// is no key to ask directly with. Do not spend the budget waiting.
			return err
		case err != nil:
			last = err.Error()
		case strings.EqualFold(got, want.Hex()):
			return nil
		case got == "":
			last = "the machine reports no endpoint configuration at all"
		default:
			last = "the machine reports " + got
		}

		remaining := time.Until(deadline)
		if remaining <= 0 {
			return errors.Errorf("the machine did not report %s within %s: %s",
				want.Hex(), budget, last)
		}
		if backoff > remaining {
			backoff = remaining
		}
		time.Sleep(backoff)
	}
}

// queryEndpointsHash asks the machine directly, for deployments whose tee-node
// publishes no attested state. Signed by the enclave, so it is the same claim
// the attested path would have carried — only the transport differs.
func queryEndpointsHash(proxyURL, apiKey string, budget time.Duration) (string, error) {
	if apiKey == "" {
		return "", errors.New("no API key to query readiness with")
	}
	report, err := keeper.QueryReadiness(context.Background(), proxyURL, apiKey, budget)
	if err != nil {
		return "", err
	}
	return report.Payload.Readiness.EndpointsHash, nil
}

func shortOrNone(h string) string {
	if h == "" {
		return "no endpoint configuration at all"
	}
	return h
}

// fetchEndpointsHash reads the active config hash the machine publishes on
// GET /state, republished inside the proxy's GET /info.
func fetchEndpointsHash(proxyURL string) (string, error) {
	resp, err := http.Get(strings.TrimRight(proxyURL, "/") + "/info")
	if err != nil {
		return "", errors.Errorf("querying the proxy: %s", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return "", errors.Errorf("proxy returned %d", resp.StatusCode)
	}

	var info struct {
		TeeInfo struct {
			State struct {
				State hexutil.Bytes `json:"state"`
			} `json:"state"`
		} `json:"teeInfo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return "", errors.Errorf("decoding /info: %s", err)
	}

	if len(info.TeeInfo.State.State) == 0 {
		return "", fccutils.ErrNoAttestedState
	}

	var body struct {
		State struct {
			Readiness struct {
				EndpointsHash string `json:"endpointsHash"`
			} `json:"readiness"`
		} `json:"state"`
	}
	if err := json.Unmarshal(info.TeeInfo.State.State, &body); err != nil {
		return "", errors.Errorf("decoding the extension state: %s", err)
	}
	return body.State.Readiness.EndpointsHash, nil
}
