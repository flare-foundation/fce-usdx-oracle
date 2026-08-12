// Command keeper drives the USDX oracle price feed on a timer: request a price
// update, poll the proxy for the signed result, relay it to UsdxPriceStore, and
// emit one JSON record per cycle.
//
//	go run ./cmd/keeper -instructionSender 0x… -interval 10m
//	go run ./cmd/keeper -instructionSender 0x… -keeperId keeper-b -records cycles.jsonl
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
	"syscall"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
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
	pf := flag.String("p", configs.ExtensionProxyURL, "extension proxy url")
	senderF := flag.String("instructionSender", "", "UsdxOracleInstructionSender address (required)")
	storeF := flag.String("store", "",
		"UsdxPriceStore address (default: the UsdxPriceStore entry in the addresses file)")
	keeperIDF := flag.String("keeperId", defaultKeeperID(),
		"stable identifier for this instance, so records from ≥2 keepers can be told apart")
	intervalF := flag.Duration("interval", 10*time.Minute,
		"how often to start a cycle; match the ~10-minute source refresh")
	pollF := flag.Duration("pollTimeout", 5*time.Minute,
		"how long to wait for a terminal action result")
	submitF := flag.Duration("submitTimeout", 3*time.Minute,
		"how long to wait for the submitPrice receipt")
	recordsF := flag.String("records", "",
		"append JSON cycle records to this file (default: stdout)")
	onceF := flag.Bool("once", false, "run a single cycle and exit — useful for a cron-driven keeper")
	directF := flag.Bool("direct", false,
		"raise GET_PRICE on the proxy instead of on-chain: no registry fee, no trigger transaction, "+
			"and the request goes to the machine behind -p rather than a random one")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for -direct (default: $DIRECT_API_KEY)")
	flag.Parse()

	if *senderF == "" {
		fccutils.FatalWithCause(errors.New("-instructionSender is required"))
	}
	if !common.IsHexAddress(*senderF) {
		fccutils.FatalWithCause(errors.Errorf("%q is not an address", *senderF))
	}
	// Checked here rather than at the first cycle: a keeper that starts and then
	// fails every tick on a missing key looks like an outage.
	if *directF && *apiKeyF == "" {
		fccutils.FatalWithCause(errors.New(
			"-direct needs an API key: pass -directApiKey or set DIRECT_API_KEY"))
	}

	// Both deadlines come out of the store's maxAge budget, and a cycle that
	// cannot finish inside its interval will always be late.
	if *pollF+*submitF >= *intervalF {
		logger.Warnf(
			"pollTimeout + submitTimeout (%s) is not less than the interval (%s): "+
				"a slow cycle will delay the next tick",
			*pollF+*submitF, *intervalF)
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	storeAddr, err := configs.ResolveContract(*storeF, *af, "UsdxPriceStore")
	if err != nil {
		fccutils.FatalWithCause(err)
	}

	records, closeRecords, err := openRecords(*recordsF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	defer closeRecords()

	cfg := keeper.LoopConfig{
		Config: keeper.Config{
			KeeperID:      *keeperIDF,
			Sender:        common.HexToAddress(*senderF),
			Store:         storeAddr,
			Proxy:         *pf,
			Direct:        *directF,
			DirectAPIKey:  *apiKeyF,
			PollTimeout:   *pollF,
			SubmitTimeout: *submitF,
		},
		Interval: *intervalF,
		Emitter:  keeper.NewEmitter(records),
	}

	// Cancelling lets an in-flight cycle unwind at its next checkpoint rather
	// than being killed mid-submission.
	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	if *onceF {
		rec, err := keeper.RunCycle(ctx, s, cfg.Config)
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
