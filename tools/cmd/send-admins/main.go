// Command send-admins publishes an admin signer set on-chain, so the addressed
// TEE machine receives it as a SET_ADMINS instruction.
//
// The payload is addresses and a threshold — nothing secret, and public on
// purpose: who may provision a machine should be as verifiable as which
// providers it reads. Private keys never go anywhere near this tool; admins sign
// provisioning envelopes with usdxprov.
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
	configF := flag.String("config", "", "admin set JSON file, delivered verbatim")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key, used to confirm delivery via a signed GET_READINESS when the "+
			"proxy publishes no attested state (default: $DIRECT_API_KEY)")
	waitF := flag.Duration("wait", 90*time.Second,
		"how long to wait for the machine to report the new admins hash; 0 skips the check")
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

	// Decode against the REAL schema before spending gas, for the reason on the
	// admins type: a rejected rotation stops prices AND stops the new admins.
	parsed, err := parseAdmins(configBytes)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}
	if err := validateAdmins(parsed); err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}

	var prettyConfig bytes.Buffer
	if err := json.Indent(&prettyConfig, configBytes, "", "  "); err != nil {
		fccutils.FatalWithCause(errors.Errorf("formatting config JSON: %s", err))
	}
	logger.Infof("Admin set from %s (%d bytes, delivered verbatim on-chain):", *configF, len(configBytes))
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

	adminsHash := crypto.Keccak256Hash(configBytes)

	if *dryRunF {
		logger.Infof("Dry run: payload is valid. adminsHash would be %s", adminsHash.Hex())
		return
	}

	instructionID, txHash, err := instrutils.SetAdmins(testSupport, instructionSenderAddress, teeID, configBytes)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	logger.Infof("Admin set published on-chain:")
	logger.Infof("  tx:          %s", txHash.Hex())
	logger.Infof("  instruction: %s", instructionID.Hex())
	logger.Infof("  adminsHash:  %s", adminsHash.Hex())

	if *waitF <= 0 {
		// Stated as a conditional, not a fact: whether the machine applied this
		// is exactly what skipping the check leaves unknown.
		logger.Warnf("Skipping the delivery check (-wait 0), so whether the machine applied %s "+
			"is unknown. If it did not, every price it signs is rejected with StaleAdmins.",
			adminsHash.Hex())
		return
	}

	// expectedAdminsHash is written, so the machine can now only submit prices while
	// actually RUNNING this set — and a non-increasing version is rejected outright,
	// so confirm. A contradicting hash is a failure; no state at all is not.
	if err := waitForAdmins(*pf, *apiKeyF, adminsHash, *waitF); err != nil {
		if errors.Is(err, fccutils.ErrNoAttestedState) {
			logger.Infof("Delivery not confirmed: %s", err)
			return
		}
		logger.Errorf("%s", err)
		logger.Errorf("The machine is NOT running the published admin set, so its prices will be " +
			"rejected with StaleAdmins until it is, and SET_SECRET signed by these admins will " +
			"be rejected too. Check the extension's logs:")
		logger.Errorf("  docker compose logs extension-tee | grep SET_ADMINS")
		os.Exit(1)
	}

	logger.Infof("Confirmed: the machine reports adminsHash %s", adminsHash.Hex())
	logger.Infof("Now re-commission it, or it stays uncommissioned in fleet-status:")
	logger.Infof("  go run ./cmd/submit-price -direct -p %s -instructionSender %s",
		*pf, instructionSenderAddress.Hex())
}

// waitForAdmins polls the machine's own /state until it reports the hash just
// published, or the budget runs out.
func waitForAdmins(proxyURL, apiKey string, want common.Hash, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := 2 * time.Second

	var last string
	for {
		got, err := fetchAdminsHash(proxyURL)
		switch {
		case errors.Is(err, fccutils.ErrNoAttestedState):
			// Ask the machine instead of giving up. GET_READINESS carries the
			// same hash, signed, over a channel that does not depend on
			// tee-node folding /state into /info.
			if h, qerr := queryAdminsHash(proxyURL, apiKey, budget); qerr == nil {
				got = h
				if strings.EqualFold(got, want.Hex()) {
					return nil
				}
				last = "the machine reports " + adminsOrNone(got)
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
			last = "the machine reports no admin set at all"
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

// queryAdminsHash asks the machine directly, for deployments whose tee-node
// publishes no attested state. Signed by the enclave, so it is the same claim
// the attested path would have carried — only the transport differs.
func queryAdminsHash(proxyURL, apiKey string, budget time.Duration) (string, error) {
	if apiKey == "" {
		return "", errors.New("no API key to query readiness with")
	}
	report, err := keeper.QueryReadiness(context.Background(), proxyURL, apiKey, budget)
	if err != nil {
		return "", err
	}
	return report.Payload.Readiness.AdminsHash, nil
}

func adminsOrNone(h string) string {
	if h == "" {
		return "no admin set at all"
	}
	return h
}

// fetchAdminsHash reads the active admin set hash the machine publishes on
// GET /state, republished inside the proxy's GET /info.
func fetchAdminsHash(proxyURL string) (string, error) {
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
				AdminsHash string `json:"adminsHash"`
			} `json:"readiness"`
		} `json:"state"`
	}
	if err := json.Unmarshal(info.TeeInfo.State.State, &body); err != nil {
		return "", errors.Errorf("decoding the extension state: %s", err)
	}
	return body.State.Readiness.AdminsHash, nil
}
