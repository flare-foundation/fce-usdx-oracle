// Command fleet-status answers "which of our TEE machines are configured for
// the feed, and are they all running the same thing?" in one screen.
//
//	go run ./cmd/fleet-status -store 0x… -feedId 0x20555344582f…
//	go run ./cmd/fleet-status -store 0x… -feedId 0x20555344582f… -json
//
// The instructions sender is derived from the store's pointer unless given with
// -instructionSender.
//
// Three sources, and the point of the tool is that they can disagree:
//
//	registry     which machines exist and their lifecycle status
//	on-chain     the feed's latest published configuration (latestEndpointsHash /
//	             latestAdminsHash, plus versions) and whether each machine is at
//	             it (isTeeIdConfigured — the precondition requestFeedUpdate
//	             enforces per target)
//	live         what each machine says it is running right now: its signed
//	             readiness report, asked over the proxy's direct channel, so
//	             this column needs the proxy's key (-directApiKey)
//
// A machine can be registered, sent a config it rejected, and still report
// ready: true — running the PREVIOUS config. It then fails every submitFeedUpdates
// with StaleEndpoints and the feed quietly stops advancing. The on-chain column
// is what makes that visible without trusting the machine's own account of
// itself; the live column is what tells you whether it has recovered.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	senderF := flag.String("instructionSender", "",
		"TeeOracleInstructionsSender address; optional — when omitted it is derived "+
			"from the store's instructionsSender() pointer")
	feedIDF := flag.String("feedId", configs.FeedID,
		"feed id (bytes21 hex); defaults to USDX/USD")
	storeF := flag.String("store", "",
		"TeeOracleFeedStore address (default: the TeeOracleFeedStore entry in the addresses file)")
	jsonOut := flag.Bool("json", false, "emit one JSON object per machine instead of a table")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for the live columns, read over GET_READINESS; without one they stay "+
			"empty (default: $DIRECT_API_KEY)")
	flag.Parse()

	if *senderF != "" && !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("%q is not an address", *senderF))
	}

	feedRaw := common.FromHex(*feedIDF)
	if len(feedRaw) != 21 {
		fccutils.FatalWithCause(errors.Errorf("feedId %q is not 21 bytes", *feedIDF))
	}
	var feedID [21]byte
	copy(feedID[:], feedRaw)

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "TeeOracleFeedStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// The instructions sender is derived from the store's own pointer unless given
	// explicitly — the store's instructionsSender() is the single source of truth,
	// the same address the preflight checks.
	var senderAddr common.Address
	if *senderF != "" {
		senderAddr = common.HexToAddress(*senderF)
	} else {
		store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
		if err != nil {
			fccutils.FatalWithCause(err)
		}
		senderAddr, err = store.InstructionsSender(nil)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("deriving the instructions sender from the store: %s", err))
		}
	}

	machines, err := buildReport(context.Background(), s, senderAddr, storeAddr, feedID, *apiKeyF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	if *jsonOut {
		enc := json.NewEncoder(os.Stdout)
		if err := enc.Encode(machines); err != nil {
			fccutils.FatalWithCause(err)
		}
		return
	}

	printTable(machines)
}

// Machine is one row of the report.
type Machine struct {
	TeeID  common.Address `json:"teeId"`
	Status string         `json:"status"`

	// On-chain. Configured is per machine; the hashes and versions are the
	// feed's latest published generation, which every machine must be at.
	Configured       bool   `json:"configured"`
	EndpointsHash    string `json:"endpointsHash,omitempty"`
	EndpointsVersion uint64 `json:"endpointsVersion,omitempty"`
	AdminsHash       string `json:"adminsHash,omitempty"`
	AdminsVersion    uint64 `json:"adminsVersion,omitempty"`

	// The store's latest accepted observation, if any. Replaces lastReport and
	// isProvenConfigured, both gone: the store keeps no per-machine bookkeeping.
	ObservedAt uint64 `json:"observedAt,omitempty"`

	// Live, from the signed readiness report. Best-effort.
	LiveEndpointsHash string            `json:"liveEndpointsHash,omitempty"`
	LiveAdminsHash    string            `json:"liveAdminsHash,omitempty"`
	LiveReady         bool              `json:"liveReady,omitempty"`
	SourceHealth      map[string]string `json:"sourceHealth,omitempty"`
}

func buildReport(
	ctx context.Context, s *support.Support, senderAddr, storeAddr common.Address, feedID [21]byte,
	apiKey string,
) ([]Machine, error) {
	sender, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the sender: %s", err)
	}

	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}

	// The store's latest accepted observation. One global stamp now — per-machine
	// bookkeeping is gone.
	storeObservedAt, _ := store.ObservedAt(&bind.CallOpts{Context: ctx})

	// The registry lists machines per extension; the feed store only accepts
	// signers from the sender's own.
	extID, err := sender.ExtensionId(&bind.CallOpts{Context: ctx})
	if err != nil {
		return nil, errors.Errorf("reading the extension id: %s", err)
	}
	active, err := s.TeeMachineRegistry.GetActiveTeeMachines(&bind.CallOpts{Context: ctx}, extID)
	if err != nil {
		return nil, errors.Errorf("listing machines for extension %s: %s", extID, err)
	}

	opts := &bind.CallOpts{Context: ctx}
	endpointsVersion, _ := sender.EndpointsVersion(opts, feedID)
	adminsVersion, _ := sender.AdminsVersion(opts, feedID)
	latestEndpoints, _ := sender.LatestEndpointsHash(opts, feedID)
	latestAdmins, _ := sender.LatestAdminsHash(opts, feedID)

	machines := make([]Machine, 0, len(active.TeeIds))
	for i, id := range active.TeeIds {
		m := Machine{
			TeeID:            id,
			ObservedAt:       storeObservedAt,
			EndpointsVersion: endpointsVersion,
			AdminsVersion:    adminsVersion,
		}

		if status, err := s.TeeMachineRegistry.GetTeeMachineStatus(opts, id); err == nil {
			m.Status = teestatus(status)
		}

		if configured, err := sender.IsTeeIdConfigured(opts, feedID, id); err == nil {
			m.Configured = configured
		}

		m.EndpointsHash = common.Hash(latestEndpoints).Hex()
		m.AdminsHash = common.Hash(latestAdmins).Hex()

		// Live, from the machine's signed readiness report. Best-effort: a machine
		// that cannot be asked is not necessarily broken, and without the proxy's
		// key it cannot be asked at all.
		url := ""
		if i < len(active.Urls) {
			url = strings.TrimRight(strings.TrimSpace(active.Urls[i]), "/")
		}
		if url != "" && apiKey != "" {
			liveState(ctx, url, apiKey, &m)
		}

		machines = append(machines, m)
	}

	return machines, nil
}

func liveState(ctx context.Context, proxyURL, apiKey string, m *Machine) {
	report, err := keeper.QueryReadiness(ctx, proxyURL, apiKey, liveQueryTimeout)
	if err != nil {
		return
	}
	m.LiveEndpointsHash = report.Payload.Readiness.EndpointsHash
	m.LiveAdminsHash = report.Payload.Readiness.AdminsHash
	m.LiveReady = report.Payload.Readiness.Ready
	m.SourceHealth = report.Payload.SourceHealth
}

// liveQueryTimeout bounds one machine's readiness round trip.
const liveQueryTimeout = 15 * time.Second

func teestatus(s uint8) string {
	switch s {
	case 0:
		return "NONE"
	case 1:
		return "REGISTERED"
	case 2:
		return "PRODUCTION"
	case 3:
		return "DECOMMISSIONED"
	case 4:
		return "PAUSED"
	default:
		return fmt.Sprintf("UNKNOWN(%d)", s)
	}
}

func shortHash(h string) string {
	if len(h) <= 12 {
		return h
	}
	return h[:6] + "…" + h[len(h)-4:]
}

func printTable(machines []Machine) {
	if len(machines) == 0 {
		fmt.Println("no machines listed for this extension")
		return
	}

	fmt.Printf("%-42s  %-13s  %-10s  %-13s  %-13s  %-13s  %-6s\n",
		"tee id", "status", "configured", "endpoints", "admins", "live", "ready")
	fmt.Println(strings.Repeat("-", 125))

	for _, m := range machines {
		endpoints := "—"
		if m.EndpointsHash != "" && m.EndpointsHash != "0x0000000000000000000000000000000000000000000000000000000000000000" {
			endpoints = shortHash(m.EndpointsHash)
		}
		admins := "—"
		if m.AdminsHash != "" && m.AdminsHash != "0x0000000000000000000000000000000000000000000000000000000000000000" {
			admins = shortHash(m.AdminsHash)
		}
		live := "—"
		if m.LiveEndpointsHash != "" {
			live = shortHash(m.LiveEndpointsHash)
		}
		ready := "—"
		if m.LiveReady {
			ready = "yes"
		} else if m.LiveEndpointsHash != "" {
			ready = "no"
		}

		fmt.Printf("%-42s  %-13s  %-10v  %-13s  %-13s  %-13s  %-6s\n",
			m.TeeID.Hex(), m.Status, m.Configured, endpoints, admins, live, ready)
	}

	// Drift summary: the rows whose live hash disagrees with what was sent.
	var drifted []string
	for _, m := range machines {
		if m.EndpointsHash != "" && m.LiveEndpointsHash != "" &&
			!strings.EqualFold(m.EndpointsHash, m.LiveEndpointsHash) {
			drifted = append(drifted, m.TeeID.Hex())
		}
	}
	if len(drifted) > 0 {
		fmt.Printf("\n⚠ %d machine(s) running a different config than was sent: %s\n",
			len(drifted), strings.Join(drifted, ", "))
	}
	if storeObservedAt := machines[0].ObservedAt; storeObservedAt > 0 {
		age := time.Since(time.Unix(int64(storeObservedAt), 0)).Round(time.Second)
		fmt.Printf("\nstore observedAt: %d (%s ago)\n", storeObservedAt, age)
	}
}
