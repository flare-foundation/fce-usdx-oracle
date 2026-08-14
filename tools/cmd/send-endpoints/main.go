// Command send-endpoints publishes an endpoint configuration on-chain, so the
// addressed TEE machine receives it as a SET_ENDPOINTS instruction.
//
// Credentials never go this way: the config is calldata and therefore public.
// Secrets travel over the direct channel via usdxprov.
//
// The teeId is derived from the attested publicKey on /info, not a field there.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/support"
	instrutils "fce-usdx-oracle/tools/pkg/utils"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	instructionSenderF := flag.String("instructionSender", "", "instructionSender address")
	configF := flag.String("config", "", "endpoints configuration JSON file, delivered verbatim")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key, used to confirm delivery via a signed GET_READINESS when the "+
			"proxy publishes no attested state (default: $DIRECT_API_KEY)")
	waitF := flag.Duration("wait", 90*time.Second,
		"how long to wait for the machine to report the new config hash; 0 skips the check")
	dryRunF := flag.Bool("dry-run", false, "validate the payload and print the hash, but send nothing")
	flag.Parse()

	if *instructionSenderF == "" || *configF == "" {
		fccutils.FatalWithCause(errors.New("both -instructionSender and -config are required"))
	}
	instructionSenderAddress := common.HexToAddress(*instructionSenderF)

	configBytes, err := os.ReadFile(*configF)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("reading config file: %s", err))
	}
	if !json.Valid(configBytes) {
		fccutils.FatalWithCause(errors.Errorf("%s is not valid JSON", *configF))
	}

	// Decode against the REAL schema before spending gas. json.Valid only says
	// the bytes are JSON; it happily passes a payload from an older schema, which
	// then lands on-chain, sets expectedConfigHash, and is rejected by the
	// extension — leaving the machine running its previous config and every
	// submitPrice from it reverting StaleConfig.
	parsed, err := parseEndpoints(configBytes)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}
	if err := validateEndpoints(parsed); err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}

	var prettyConfig bytes.Buffer
	if err := json.Indent(&prettyConfig, configBytes, "", "  "); err != nil {
		fccutils.FatalWithCause(errors.Errorf("formatting config JSON: %s", err))
	}
	logger.Infof("Config from %s (%d bytes, delivered verbatim on-chain):", *configF, len(configBytes))
	logger.Info(prettyConfig.String())
	logger.Infof("Validated against the extension's schema:")
	logger.Info("\n" + summarise(parsed))

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	teeID, _, err := fccutils.GetTeeProxyID(*pf)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("deriving TEE identity from %s/info: %s", *pf, err))
	}
	logger.Infof("TEE identity derived from attested publicKey: %s", teeID.Hex())

	configHash := crypto.Keccak256Hash(configBytes)

	if *dryRunF {
		logger.Infof("Dry run: payload is valid. configHash would be %s", configHash.Hex())
		return
	}

	instructionID, txHash, err := instrutils.SetEndpoints(testSupport, instructionSenderAddress, teeID, configBytes)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Endpoints published on-chain:")
	logger.Infof("  tx:          %s", txHash.Hex())
	logger.Infof("  instruction: %s", instructionID.Hex())
	logger.Infof("  configHash:  %s", configHash.Hex())

	if *waitF <= 0 {
		// Stated as a conditional, not a fact: whether the machine applied this
		// is exactly what skipping the check leaves unknown.
		logger.Warnf("Skipping the delivery check (-wait 0), so whether the machine applied %s "+
			"is unknown. If it did not, every price it signs is rejected with StaleConfig.",
			configHash.Hex())
		return
	}

	// expectedConfigHash is now written, so from this moment the machine can only
	// submit prices once it is actually RUNNING this config. FCC is
	// fire-and-forget: the instruction may never be delivered, and the extension
	// may reject it. Either way the machine keeps its old configuration and goes
	// silent. Confirm rather than assume.
	// Two outcomes are possible here and they call for opposite reactions, so
	// they are never reported with the same words: the machine contradicting the
	// published hash is a failure, whereas the machine publishing no state at all
	// leaves the question open.
	if err := waitForConfig(*pf, *apiKeyF, configHash, *waitF); err != nil {
		if errors.Is(err, fccutils.ErrNoAttestedState) {
			logger.Infof("Delivery not confirmed: %s", err)
			return
		}
		logger.Errorf("%s", err)
		logger.Errorf("The machine is NOT running the published config, so its prices will be " +
			"rejected with StaleConfig until it is. Check the extension's logs:")
		logger.Errorf("  docker compose logs extension-tee | grep SET_ENDPOINTS")
		os.Exit(1)
	}

	logger.Infof("Confirmed: the machine reports endpointsHash %s", configHash.Hex())
	logger.Infof("Now re-commission it, or it stays uncommissioned in fleet-status:")
	logger.Infof("  go run ./cmd/submit-price -direct -p %s -instructionSender %s",
		*pf, instructionSenderAddress.Hex())
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
			// Nothing will appear later: this tee-node version never publishes
			// state and there is no key to ask directly with. Returning
			// immediately keeps the operator from watching a budget expire on a
			// question that cannot be answered.
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

	// Distinguished before unmarshalling: an empty state would otherwise fail as
	// "unexpected end of JSON input", which reads like a corrupt response rather
	// than an absent one.
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
