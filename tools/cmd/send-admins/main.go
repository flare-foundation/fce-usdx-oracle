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
	"encoding/json"
	"flag"
	"net/http"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
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

	// Decode against the REAL schema before spending gas, for the reason spelled
	// out on the admins type: a rejected rotation leaves the machine on its old
	// admin set while the chain expects the new one, which stops prices AND
	// stops the new admins from provisioning anything.
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
		logger.Warnf("Skipping the delivery check (-wait 0). Until this machine reports %s, "+
			"every price it signs is rejected with StaleAdmins.", adminsHash.Hex())
		return
	}

	// expectedAdminsHash is now written, so from this moment the machine can only
	// submit prices once it is actually RUNNING this admin set. FCC is
	// fire-and-forget: the instruction may never be delivered, and the extension
	// may reject it — a replayed or non-increasing version is rejected outright.
	// Either way the machine keeps its previous set and goes silent. Confirm
	// rather than assume.
	if err := waitForAdmins(*pf, adminsHash, *waitF); err != nil {
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
func waitForAdmins(proxyURL string, want common.Hash, budget time.Duration) error {
	deadline := time.Now().Add(budget)
	backoff := 2 * time.Second

	var last string
	for {
		got, err := fetchAdminsHash(proxyURL)
		switch {
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
