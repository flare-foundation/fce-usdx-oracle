// Command fleet-status answers "which of our TEE machines are configured, and
// are they all running the same thing?" in one screen.
//
//	go run ./cmd/fleet-status -instructionSender 0x…
//	go run ./cmd/fleet-status -instructionSender 0x… -json
//
// Three sources, and the point of the tool is that they can disagree:
//
//	registry     which machines exist and their lifecycle status
//	on-chain     what config was SENT to each (sender.expectedConfigHash) and
//	             what each PROVED it was running (store.lastReport)
//	live         what each machine says it is running right now (GET /state)
//
// A machine can be registered, sent a config it rejected, and still report
// ready: true — running the PREVIOUS config. It then fails every submitPrice
// with StaleConfig and the feed quietly stops advancing. The on-chain proof
// column is what makes that visible without trusting the machine's own account
// of itself; the live column is what tells you whether it has recovered.
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
	"fce-usdx-oracle/tools/pkg/contracts/usdxoracle"
	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/flare-foundation/go-flare-common/pkg/contracts/tee/machinemanager"
	"github.com/pkg/errors"
)

// machineStatus is one machine, from every angle.
type machineStatus struct {
	TeeID string `json:"teeId"`
	URL   string `json:"url,omitempty"`

	// --- registry ---
	RegistryStatus uint8  `json:"registryStatus"`
	StatusName     string `json:"statusName"`

	// --- on-chain configuration ---
	// ExpectedConfigHash is what setEndpoints published FOR this machine.
	ExpectedConfigHash string `json:"expectedConfigHash"`
	// ProvenConfigHash is what this machine PROVED by landing a price.
	ProvenConfigHash string `json:"provenConfigHash"`
	ProvenAt         uint64 `json:"provenAt"`
	Submissions      uint32 `json:"submissions"`
	Commissioned     bool   `json:"commissioned"`

	// --- live, from the machine's own proxy ---
	// Empty when the proxy could not be reached, which is never the same as
	// "healthy": Reachable says which.
	Reachable    bool              `json:"reachable"`
	LiveHash     string            `json:"liveConfigHash,omitempty"`
	Ready        bool              `json:"ready"`
	MissingRefs  []string          `json:"missingSecretRefs,omitempty"`
	SourceHealth map[string]string `json:"sourceHealth,omitempty"`

	// Notes are the derived problems — the reason to run this at all.
	Notes []string `json:"notes,omitempty"`
}

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	senderF := flag.String("instructionSender", "", "UsdxOracleInstructionSender address (required)")
	storeF := flag.String("store", "",
		"UsdxPriceStore address (default: the UsdxPriceStore entry in the addresses file)")
	proxyF := flag.String("p", "",
		"override the proxy URL for every machine; default is each machine's registered url")
	timeoutF := flag.Duration("timeout", 10*time.Second, "per-machine budget for the live /info read")
	jsonF := flag.Bool("json", false, "emit JSON instead of a table")
	flag.Parse()

	if *senderF == "" {
		fccutils.FatalWithCause(errors.New("-instructionSender is required"))
	}
	if !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("%q is not an address", *senderF))
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "UsdxPriceStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	statuses, err := collect(s, common.HexToAddress(*senderF), storeAddr, *proxyF, *timeoutF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	if *jsonF {
		enc := json.NewEncoder(os.Stdout)
		enc.SetIndent("", "  ")
		if err := enc.Encode(statuses); err != nil {
			fccutils.FatalWithCause(err)
		}
		return
	}
	render(statuses, storeAddr)
}

func collect(
	s *support.Support, senderAddr, storeAddr common.Address, proxyOverride string, timeout time.Duration,
) ([]machineStatus, error) {
	opts := &bind.CallOpts{Context: context.Background()}

	sender, err := usdxoracle.NewUsdxOracleInstructionSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the instruction sender: %s", err)
	}
	store, err := usdxstore.NewUsdxPriceStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}

	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return nil, errors.Errorf("reading the store's extension id: %s", err)
	}

	machines, err := machinemanager.NewMachineManager(s.Addresses.FlareTeeManager, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the machine registry: %s", err)
	}

	active, err := machines.GetActiveTeeMachines(opts, extensionID)
	if err != nil {
		return nil, errors.Errorf("listing active machines for extension %s: %s", extensionID, err)
	}

	// Machines the store has seen but the registry no longer lists — suspended,
	// banned, or moved to another extension. Their prices are no longer
	// accepted, so leaving them out would hide a shrinking fleet.
	seen, err := store.TeeReports(opts)
	if err != nil {
		return nil, errors.Errorf("reading the store's per-machine reports: %s", err)
	}

	order := append([]common.Address(nil), active.TeeIds...)
	urls := map[common.Address]string{}
	for i, id := range active.TeeIds {
		urls[id] = active.Urls[i]
	}
	inRegistry := map[common.Address]bool{}
	for _, id := range active.TeeIds {
		inRegistry[id] = true
	}
	for _, id := range seen.Tees {
		if !inRegistry[id] {
			order = append(order, id)
		}
	}

	out := make([]machineStatus, 0, len(order))
	for _, id := range order {
		out = append(out, inspect(
			opts, machines, sender, store, id, urls[id], proxyOverride, inRegistry[id], timeout))
	}
	return out, nil
}

func inspect(
	opts *bind.CallOpts,
	machines *machinemanager.MachineManager,
	sender *usdxoracle.UsdxOracleInstructionSenderCaller,
	store *usdxstore.UsdxPriceStoreCaller,
	teeID common.Address, registeredURL, proxyOverride string, inRegistry bool, timeout time.Duration,
) machineStatus {
	m := machineStatus{TeeID: teeID.Hex(), URL: registeredURL}
	if proxyOverride != "" {
		m.URL = proxyOverride
	}

	if status, err := machines.GetTeeMachineStatus(opts, teeID); err == nil {
		m.RegistryStatus = status
		m.StatusName = statusName(status)
	} else {
		m.StatusName = "unreadable"
		m.Notes = append(m.Notes, "registry status could not be read")
	}
	if !inRegistry {
		m.Notes = append(m.Notes,
			"has landed prices before but is no longer an active machine for this extension")
	}

	expected, err := sender.ExpectedConfigHash(opts, teeID)
	if err == nil {
		m.ExpectedConfigHash = common.Hash(expected).Hex()
	}
	if expected == ([32]byte{}) {
		m.Notes = append(m.Notes, "no config published for it — setEndpoints has never been called")
	}

	if report, err := store.LastReport(opts, teeID); err == nil {
		m.ProvenAt = report.AcceptedAt
		m.Submissions = report.Submissions
		if report.AcceptedAt != 0 {
			m.ProvenConfigHash = common.Hash(report.ConfigHash).Hex()
		}
	}
	if proven, err := store.IsProvenConfigured(opts, teeID); err == nil {
		m.Commissioned = proven
	}

	switch {
	case m.ProvenAt == 0 && expected != ([32]byte{}):
		m.Notes = append(m.Notes,
			"never landed a price — run: submit-price -direct -p "+orPlaceholder(m.URL))
	case m.ProvenAt != 0 && !m.Commissioned:
		m.Notes = append(m.Notes,
			"proved an OLD config; every submitPrice reverts StaleConfig until it lands one under the "+
				"current config — re-run: submit-price -direct -p "+orPlaceholder(m.URL))
	}

	readLive(&m, timeout)
	return m
}

// readLive fills in what the machine says about itself. Best effort: an
// unreachable proxy leaves Reachable false rather than reading as healthy.
func readLive(m *machineStatus, timeout time.Duration) {
	if m.URL == "" {
		m.Notes = append(m.Notes, "no proxy url registered — pass -p to read its live state")
		return
	}

	state, err := fetchState(m.URL, timeout)
	if err != nil {
		m.Notes = append(m.Notes, "proxy unreachable: "+err.Error())
		return
	}

	m.Reachable = true
	m.LiveHash = state.Readiness.EndpointsHash
	m.Ready = state.Readiness.Ready
	m.MissingRefs = state.Readiness.MissingRefs
	m.SourceHealth = state.SourceHealth

	if !state.Readiness.EndpointsSet {
		m.Notes = append(m.Notes, "running no endpoint configuration at all")
	}
	if len(state.Readiness.MissingRefs) > 0 {
		m.Notes = append(m.Notes,
			"missing credentials: "+strings.Join(state.Readiness.MissingRefs, ", "))
	}

	// The divergence this tool exists to surface: the chain says one config was
	// published, the machine reports running another. Compared case-insensitively
	// because one side is a Go-formatted hash and the other a JSON string.
	if m.ExpectedConfigHash != "" && m.LiveHash != "" &&
		!strings.EqualFold(m.ExpectedConfigHash, m.LiveHash) {
		m.Notes = append(m.Notes,
			"live config differs from the one published on-chain — it rejected or never received the last setEndpoints")
	}
	for name, health := range state.SourceHealth {
		if health != "ok" && health != "unknown" {
			m.Notes = append(m.Notes, fmt.Sprintf("source %s is %s", name, health))
		}
	}
}

// liveState is the extension's GET /state body, as republished inside the
// proxy's GET /info.
type liveState struct {
	Readiness struct {
		EndpointsSet  bool     `json:"endpointsSet"`
		EndpointsHash string   `json:"endpointsHash"`
		Ready         bool     `json:"ready"`
		MissingRefs   []string `json:"missingSecretRefs"`
	} `json:"readiness"`
	SourceHealth map[string]string `json:"sourceHealth"`
}

func fetchState(proxyURL string, timeout time.Duration) (liveState, error) {
	client := &http.Client{Timeout: timeout}

	resp, err := client.Get(strings.TrimRight(proxyURL, "/") + "/info")
	if err != nil {
		return liveState{}, err
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		return liveState{}, errors.Errorf("proxy returned %d", resp.StatusCode)
	}

	// TeeState.State carries the extension's /state body as hex bytes.
	var info struct {
		TeeInfo struct {
			State struct {
				State hexutil.Bytes `json:"state"`
			} `json:"state"`
		} `json:"teeInfo"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&info); err != nil {
		return liveState{}, errors.Errorf("decoding /info: %s", err)
	}
	if len(info.TeeInfo.State.State) == 0 {
		return liveState{}, errors.New("/info carried no extension state")
	}

	var body struct {
		State liveState `json:"state"`
	}
	if err := json.Unmarshal(info.TeeInfo.State.State, &body); err != nil {
		return liveState{}, errors.Errorf("decoding the extension state: %s", err)
	}
	return body.State, nil
}

func render(statuses []machineStatus, storeAddr common.Address) {
	fmt.Printf("UsdxPriceStore: %s\n", storeAddr.Hex())
	fmt.Printf("%d machine(s)\n", len(statuses))

	commissioned := 0
	for _, m := range statuses {
		if m.Commissioned {
			commissioned++
		}
	}

	for _, m := range statuses {
		fmt.Printf("\n%s\n", m.TeeID)
		fmt.Printf("  registry      %s\n", m.StatusName)
		if m.URL != "" {
			fmt.Printf("  proxy         %s\n", m.URL)
		}
		fmt.Printf("  config sent   %s\n", shortHash(m.ExpectedConfigHash))
		fmt.Printf("  config proven %s%s\n", shortHash(m.ProvenConfigHash), provenSuffix(m))
		fmt.Printf("  live          %s\n", liveSummary(m))
		fmt.Printf("  commissioned  %s\n", yesNo(m.Commissioned))
		for _, note := range m.Notes {
			fmt.Printf("  ! %s\n", note)
		}
	}

	fmt.Printf("\n%d of %d machine(s) have proven the configuration currently published for them.\n",
		commissioned, len(statuses))
	if commissioned < len(statuses) {
		fmt.Println("A machine that has not is one whose prices the store will reject.")
	}
}

func provenSuffix(m machineStatus) string {
	if m.ProvenAt == 0 {
		return ""
	}
	return fmt.Sprintf("  (%d submission(s), last accepted %s)",
		m.Submissions, time.Unix(int64(m.ProvenAt), 0).UTC().Format(time.RFC3339))
}

func liveSummary(m machineStatus) string {
	if !m.Reachable {
		return "unreachable"
	}
	return fmt.Sprintf("%s  ready=%s", shortHash(m.LiveHash), yesNo(m.Ready))
}

func shortHash(h string) string {
	switch {
	case h == "":
		return "(none)"
	case strings.EqualFold(h, "0x"+strings.Repeat("0", 64)):
		return "(none)"
	case len(h) > 18:
		return h[:18] + "…"
	default:
		return h
	}
}

func yesNo(b bool) string {
	if b {
		return "yes"
	}
	return "NO"
}

func orPlaceholder(url string) string {
	if url == "" {
		return "<that machine's proxy>"
	}
	return url
}

// statusName mirrors ITeeMachineRegistry.TeeStatus, which the ABI flattens to a
// bare uint8. Only PRODUCTION (2) submits prices the store will accept.
func statusName(status uint8) string {
	names := []string{"NONE", "INITIALIZED", "PRODUCTION", "SUSPENDED", "PAUSED", "BANNED"}
	if int(status) < len(names) {
		return fmt.Sprintf("%s (%d)", names[status], status)
	}
	return fmt.Sprintf("unknown (%d)", status)
}
