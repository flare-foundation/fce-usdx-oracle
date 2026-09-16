// Command keeper drives the USDX oracle price feed on a timer: request a feed
// update from the configured machines, poll the proxy for the signed result,
// relay it to TeeOracleFeedStore, and emit one JSON record per cycle.
//
//	go run ./cmd/keeper -tee @a,@b,@c -interval 10m
//	go run ./cmd/keeper -tee @a,@b,@c -once   # one cycle, for cron
//
// The store resolves from -store or the TeeOracleFeedStore entry in the
// addresses file; the instructions sender is derived from the store's own
// instructionsSender() unless given explicitly with -instructionSender.
//
// Every cycle requests through requestFeedUpdate(feedId, teeIds) on the
// instruction sender, which pays the registry fee and dispatches one instruction
// per named machine. The keeper picks the targets (-tee), so the result is
// polled on every named machine's proxy until one answers. Before paying, it
// asks those machines over GET_READINESS whether they would sign at all — a fee
// spent on a machine with a stale observation buys a predictable failure.
//
// The keeper holds no authority, so a compromised one costs gas, not
// correctness. Run at least two: their submissions collide harmlessly, exactly
// one wins each cycle and the rest record race_lost. It does not alert — paging
// on the records, and on on-chain feed age, is the monitoring layer's job.
package main

import (
	"context"
	"flag"
	"os"
	"os/signal"
	"path/filepath"
	"strings"
	"syscall"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", configs.ExtensionProxyURL,
		"extension proxy url; set explicitly it OVERRIDES the url the registry publishes for every "+
			"machine (right only with one machine), otherwise each machine is reached at its own "+
			"registered url and this is only the fallback when the registry lists none")
	senderF := flag.String("instructionSender", "",
		"TeeOracleInstructionsSender address; optional — when omitted it is derived "+
			"from the store's instructionsSender() pointer")
	storeF := flag.String("store", "",
		"TeeOracleFeedStore address (default: the TeeOracleFeedStore entry in the addresses file)")
	feedIDF := flag.String("feedId", "0x21555344582f555344000000000000000000000000",
		"feed id (bytes21 hex); defaults to USDX/USD")
	teeF := flag.String("tee", "",
		"comma-separated TEE machine addresses to request from (required)")
	keeperIDF := flag.String("keeperId", defaultKeeperID(),
		"stable identifier for this instance, so records from ≥2 keepers can be told apart")
	intervalF := flag.Duration("interval", 10*time.Minute,
		"how often to start a cycle; match the ~10-minute source refresh")
	pollF := flag.Duration("pollTimeout", 5*time.Minute,
		"how long to wait for a terminal action result")
	submitF := flag.Duration("submitTimeout", 3*time.Minute,
		"how long to wait for the submitFeedUpdates receipt")
	recordsF := flag.String("records", "",
		"append JSON cycle records to this file (default: stdout)")
	onceF := flag.Bool("once", false, "run a single cycle and exit — useful for a cron-driven keeper")
	dryRunF := flag.Bool("dry-run", false,
		"run the cycle through the on-chain preflight and stop before submitting; the request is still sent and paid for")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for the GET_READINESS preflight, which is skipped without one "+
			"(default: $DIRECT_API_KEY)")
	flag.Parse()

	// Which flags were given by hand — -p means one thing as an override and
	// another as a fallback.
	set := map[string]bool{}
	flag.Visit(func(f *flag.Flag) { set[f.Name] = true })

	if *senderF != "" && !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("%q is not an address", *senderF))
	}
	if *apiKeyF == "" {
		// Not fatal: the preflight is a saving, not a requirement. Warned about
		// because the symptom otherwise is fees quietly spent on refusals.
		logger.Warnf("no API key: every cycle pays the registry fee without asking the machines " +
			"first whether they would sign — pass -directApiKey or set DIRECT_API_KEY")
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "TeeOracleFeedStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	// The instructions sender is derived from the store's own pointer unless
	// given explicitly — the store's instructionsSender() is the single source
	// of truth, and the same address the preflight checks. Keeping it optional
	// lets operations pass only -store.
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
		logger.Infof("derived instructions sender from store: %s", senderAddr.Hex())
	}

	feedRaw := common.FromHex(*feedIDF)
	if len(feedRaw) != 21 {
		fccutils.FatalWithCause(errors.Errorf("feedId %q is not 21 bytes", *feedIDF))
	}
	var feed [21]byte
	copy(feed[:], feedRaw)

	teeIDs, err := parseTeeIDs(*teeF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	if len(teeIDs) == 0 {
		fccutils.FatalWithCause(errors.New("-tee is required: at least one TEE machine address"))
	}

	records, closeRecords, err := openRecords(*recordsF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	defer closeRecords()

	// -p overrides every machine's registered url only when it was given by
	// hand: its default is an env-driven fallback, and silently treating that as
	// an override would send every machine's request to one proxy.
	proxyOverride := ""
	if set["p"] {
		proxyOverride = *pf
	}

	cfg := keeper.LoopConfig{
		Config: keeper.Config{
			KeeperID:      *keeperIDF,
			Sender:        senderAddr,
			Store:         storeAddr,
			FeedID:        feed,
			TeeIDs:        teeIDs,
			Proxy:         *pf,
			DirectAPIKey:  *apiKeyF,
			PollTimeout:   *pollF,
			SubmitTimeout: *submitF,
			DryRun:        *dryRunF,
		},
		Interval:      *intervalF,
		ProxyOverride: proxyOverride,
		Emitter:       keeper.NewEmitter(records),
	}

	// Cancelling lets an in-flight cycle unwind at its next checkpoint rather
	// than being killed mid-submission.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *onceF {
		rec, err := keeper.RunOnce(ctx, s, cfg)
		cfg.Emitter.Emit(rec)
		if err != nil && !rec.Outcome.Advanced() {
			fccutils.FatalWithCause(err)
		}
		logger.Infof("cycle finished: %s", rec.Outcome)
		return
	}

	if err := keeper.Run(ctx, s, cfg); err != nil {
		fccutils.FatalWithCause(err)
	}
}

// openRecords defaults to stdout, which a process supervisor captures.
func openRecords(path string) (*os.File, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, errors.Errorf("creating the directory of %s: %s", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, errors.Errorf("opening %s: %s", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}

// defaultKeeperID falls back to the hostname, distinct per instance.
func defaultKeeperID() string {
	if h, err := os.Hostname(); err == nil && h != "" {
		return h
	}
	return "keeper"
}

// parseTeeIDs splits a comma-separated list of hex addresses, trimming
// spaces and ignoring empty entries.
func parseTeeIDs(s string) ([]common.Address, error) {
	out := make([]common.Address, 0, strings.Count(s, ",")+1)
	for raw := range strings.SplitSeq(s, ",") {
		raw = strings.TrimSpace(raw)
		if raw == "" {
			continue
		}
		if !common.IsHexAddress(raw) {
			return nil, errors.Errorf("%q is not a hex address", raw)
		}
		out = append(out, common.HexToAddress(raw))
	}
	return out, nil
}
