// Command send-admins publishes an admin signer set on-chain through
// TeeOracleInstructionsSender, so the addressed TEE machines receive it as a
// SET_ADMINS instruction.
//
// The payload is addresses and thresholds — nothing secret, and public on
// purpose: who may provision a machine should be as verifiable as which
// providers it reads. Private keys never go anywhere near this tool; admins sign
// provisioning envelopes with usdxprov.
//
// The contract ABI-encodes Admins{version, feedId, roles} and assigns the
// version; this tool takes a role-tagged JSON file and builds the typed AdminRole
// list from it. It predicts the version (adminsVersion(feedId) + 1) so it can
// predict the hash every later feed update and SET_SECRET will have to carry.
//
// One publication fans out to every machine active on the extension when the
// call executes; the sender reads that set itself and quotes the fee for it
// (getAdminsPublicationFee), which this tool reports and attaches. In production
// mode setAdmins is governance-only and the calldata goes through the timelock; the
// direct Send path works only before switchToProductionMode. On a real
// deployment (where the upstream deploy script has already switched to
// production mode) use -out to export the governance hand-off — calldata, fee
// and predicted hash — for the Flare governance executor instead of sending.
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
	"github.com/ethereum/go-ethereum/crypto"
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
	configF := flag.String("config", "", "admin set JSON file")
	feedIDF := flag.String("feedId", "0x20555344582f555344000000000000000000000000",
		"feed id (bytes21 hex); defaults to USDX/USD")
	claimBackF := flag.String("claimBack", "",
		"payer of record for the dispatched instruction, written on-chain for the reward "+
			"calculation (default: this tool's key; with -out, name the executor's wallet)")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key, used to confirm delivery via a signed GET_READINESS when the "+
			"proxy publishes no attested state (default: $DIRECT_API_KEY)")
	waitF := flag.Duration("wait", 90*time.Second,
		"how long to wait for the machine to report the new admins hash; 0 skips the check")
	dryRunF := flag.Bool("dry-run", false, "validate the payload and print the predicted hash, but send nothing")
	encodeF := flag.Bool("encode", false,
		"print the ABI-encoded Admins payload for -version and exit, touching no chain: the exact "+
			"bytes setAdmins would dispatch, for delivering to a dev server's /action directly")
	versionF := flag.Uint64("version", 0,
		"publication version to encode with -encode; on-chain the sender assigns it, so this is only "+
			"for a dev server, whose store requires each publication to be newer than the last")
	outF := flag.String("out", "",
		"write the governance hand-off (calldata, fee, predicted hash) to this JSON file "+
			"instead of sending — the path for production-mode senders, where setAdmins "+
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

	// Decode against the REAL schema before spending gas: a rejected rotation
	// stops updates AND stops the new admins landing.
	roles, err := parseAdmins(configBytes)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}
	if err := validateAdmins(roles); err != nil {
		fccutils.FatalWithCause(errors.Errorf("%s: %s", *configF, err))
	}

	// Chain-less: the same encoder the on-chain path uses, so a dev server is
	// handed the bytes governance would dispatch rather than an approximation.
	if *encodeF {
		if *versionF == 0 {
			fccutils.FatalWithCause(errors.New("-encode needs -version (a dev server's store rejects version 0)"))
		}
		payload, err := oraclepayload.EncodeAdmins(*versionF, feedID, roles)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("encoding Admins: %s", err))
		}
		fmt.Println(hexutil.Encode(payload))
		return
	}

	instructionSenderAddress, err := configs.ResolveContract(*instructionSenderF, *af, "TeeOracleInstructionsSender")
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the instructions sender: %s — pass -instructionSender "+
			"or run scripts/import-upstream-addresses.sh after the upstream deploy", err))
	}

	var claimBack common.Address
	if *claimBackF != "" {
		if !common.IsHexAddress(*claimBackF) {
			fccutils.FatalWithCause(errors.Errorf("-claimBack %q is not an address", *claimBackF))
		}
		claimBack = common.HexToAddress(*claimBackF)
	}

	logger.Infof("Admin set from %s:", *configF)
	logger.Info(summarise(roles))

	testSupport, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	sender, err := instrutils.Sender(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	currentVersion, err := sender.AdminsVersion(nil, feedID)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("reading adminsVersion: %s", err))
	}
	version := currentVersion + 1

	// The sender records the payer for the reward calculation and refuses the
	// zero address; absent a choice, the key sending (or, with -out, quoting) is it.
	if claimBack == (common.Address{}) {
		claimBack = crypto.PubkeyToAddress(testSupport.Prv.PublicKey)
	}

	publication, err := instrutils.EncodeSetAdmins(feedID, roles, claimBack, version)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("encoding setAdmins: %s", err))
	}
	if err := publication.ResolveFee(testSupport, instructionSenderAddress, instrutils.OpCommandSetAdmins); err != nil {
		fccutils.FatalWithCause(errors.Errorf("resolving the fee: %s", err))
	}

	logger.Infof("Publishing for feed %s; the sender dispatches to its %d active machine(s):", *feedIDF, len(publication.TeeIDs))
	for _, id := range publication.TeeIDs {
		logger.Infof("  tee: %s", id.Hex())
	}
	logger.Infof("  predicted version:    %d (current %d)", publication.Version, currentVersion)
	logger.Infof("  predicted adminsHash: %s", publication.PayloadHash.Hex())
	logger.Infof("  fee quoted by the sender: %s wei", publication.Fee)
	logger.Infof("  claim-back address:   %s", claimBack.Hex())

	if *dryRunF {
		logger.Infof("Dry run: nothing sent. The sender assigns the version, so the predicted " +
			"hash is only right if no other SET_ADMINS for this feed lands first.")
		return
	}

	// Governance hand-off: on a real deployment the sender is already in
	// production mode, setAdmins is governance-only, and Flare governance
	// executes it through the timelock. This repo's contribution is the exact
	// calldata plus the fee, written out for the executor.
	if *outF != "" {
		handoff, err := publication.Handoff(instructionSenderAddress, "setAdmins")
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
		logger.Infof("Nothing was sent: setAdmins is governance-only once the sender is in production mode.")
		return
	}

	// Direct submission: works only while the caller is governance (before
	// switchToProductionMode). On a real deployment this reverts with the
	// governance guard; the calldata then has to go through executeGovernanceCall.
	txHash, err := publication.Send(testSupport, instructionSenderAddress)
	if err != nil {
		fccutils.FatalWithCause(errors.Errorf("publishing admins: %s", err))
	}

	logger.Infof("Admin set published on-chain:")
	logger.Infof("  tx:         %s", txHash.Hex())
	logger.Infof("  version:    %d", publication.Version)
	logger.Infof("  adminsHash: %s", publication.PayloadHash.Hex())

	if *waitF <= 0 {
		// Stated as a conditional, not a fact: whether the machine applied this
		// is exactly what skipping the check leaves unknown.
		logger.Warnf("Skipping the delivery check (-wait 0), so whether the machine applied %s "+
			"is unknown. If it did not, every update it signs is rejected with StaleAdmins.",
			publication.PayloadHash.Hex())
		return
	}

	// The delivery check confirms against the primary proxy only. With multiple
	// machines use cmd/fleet-status to reconcile every machine's live hash.
	if err := waitForAdmins(*pf, *apiKeyF, publication.PayloadHash, *waitF); err != nil {
		if errors.Is(err, fccutils.ErrNoAttestedState) {
			logger.Infof("Delivery not confirmed: %s", err)
			return
		}
		logger.Errorf("%s", err)
		logger.Errorf("The machine is NOT running the published admin set, so its updates will be " +
			"rejected with StaleAdmins until it is, and SET_SECRET signed by these admins will " +
			"be rejected too. Check the extension's logs:")
		logger.Errorf("  docker compose logs extension-tee | grep SET_ADMINS")
		os.Exit(1)
	}

	logger.Infof("Confirmed: the machine reports adminsHash %s", publication.PayloadHash.Hex())
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
