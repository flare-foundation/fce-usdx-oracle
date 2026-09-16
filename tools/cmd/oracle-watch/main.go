// Command oracle-watch follows one machine's own account of itself over time:
// every interval it asks the machine for its signed readiness report over the
// proxy's direct channel, appends one JSON sample per poll to a records file,
// and prints every change worth a look — a source's health, the error class of
// the last observation, the ready and would-sign flags, a failed observation.
//
// It reads and never sends: no fee, no transaction, no instruction. The machine
// observes on its own timer regardless of the keeper's cycles, so this is where
// the failures between cycles become visible; the keeper's records sample the
// same report once per cycle. Only failure classes ever reach the report, never
// error text: the reasons live in the machine's logs.
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"os"
	"os/signal"
	"path/filepath"
	"sort"
	"syscall"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/keeper"
	"fce-usdx-oracle/tools/pkg/readinessreport"

	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

// sample is one poll: the machine's signed answer, verified, or the error
// reading it. Errors are recorded as such rather than dropped, so a gap in the
// machine's answers is a visible fact and not a missing line.
type sample struct {
	At    time.Time `json:"at"`
	Proxy string    `json:"proxy"`

	Readiness      *readinessreport.Payload `json:"readiness,omitempty"`
	ReadinessError string                   `json:"readinessError,omitempty"`
	// Signer is the machine the signature recovers to; the report is bound to
	// the request that asked, so it is this machine's answer to this poll.
	Signer string `json:"signer,omitempty"`
}

func main() {
	pf := flag.String("p", configs.ExtensionProxyURL, "the machine's proxy url")
	apiKeyF := flag.String("directApiKey", os.Getenv("DIRECT_API_KEY"),
		"proxy API key for GET_READINESS (default: $DIRECT_API_KEY)")
	intervalF := flag.Duration("interval", 2*time.Minute,
		"how often to poll; the machine observes every 2 minutes, so faster shows nothing new")
	onceF := flag.Bool("once", false, "poll once, print the sample, exit")
	recordsF := flag.String("records", "", "append JSON samples to this file (default: stdout)")
	timeoutF := flag.Duration("timeout", 30*time.Second, "how long to wait for each answer")
	flag.Parse()

	if *apiKeyF == "" {
		fccutils.FatalWithCause(errors.New(
			"a proxy API key is required (-directApiKey or DIRECT_API_KEY): the machine's account " +
				"of itself is its signed readiness report, asked over the proxy's direct channel"))
	}

	out, closeOut, err := openRecords(*recordsF)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	defer closeOut()
	enc := json.NewEncoder(out)

	ctx, stop := signal.NotifyContext(context.Background(), os.Interrupt, syscall.SIGTERM)
	defer stop()

	logger.Infof("watching %s every %s", *pf, *intervalF)

	var prev *sample
	for {
		s := poll(ctx, *pf, *apiKeyF, *timeoutF)
		// The records are telemetry: a write error is reported and the watch goes on.
		if err := enc.Encode(s); err != nil {
			logger.Warnf("writing the sample: %s", err)
		}
		report(prev, s)
		prev = &s
		if *onceF {
			return
		}
		select {
		case <-ctx.Done():
			logger.Info("stopped")
			return
		case <-time.After(*intervalF):
		}
	}
}

// poll asks the machine once.
func poll(ctx context.Context, proxy, apiKey string, timeout time.Duration) sample {
	s := sample{At: time.Now().UTC(), Proxy: proxy}
	rctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	rep, err := keeper.QueryReadiness(rctx, proxy, apiKey, timeout)
	if err != nil {
		s.ReadinessError = err.Error()
		return s
	}
	p := rep.Payload
	s.Readiness = &p
	s.Signer = rep.Signer.Hex()
	return s
}

// report prints what changed since the previous sample, and the whole picture
// for the first one.
func report(prev *sample, cur sample) {
	stamp := cur.At.Format("15:04:05")
	if cur.ReadinessError != "" {
		logger.Warnf("%s readiness: %s", stamp, cur.ReadinessError)
	}

	health, class, ready, wouldSign, failures, age := observed(&cur)
	if prev == nil {
		names := make([]string, 0, len(health))
		for n := range health {
			names = append(names, n)
		}
		sort.Strings(names)
		for _, n := range names {
			logger.Infof("%s %-16s %s", stamp, n, health[n])
		}
		logger.Infof("%s ready=%v wouldSign=%v observationAge=%ds failures=%d lastErrorClass=%q",
			stamp, ready, wouldSign, age, failures, class)
		return
	}

	pHealth, pClass, pReady, pWouldSign, pFailures, _ := observed(prev)
	names := make(map[string]struct{}, len(health)+len(pHealth))
	for n := range health {
		names[n] = struct{}{}
	}
	for n := range pHealth {
		names[n] = struct{}{}
	}
	sorted := make([]string, 0, len(names))
	for n := range names {
		sorted = append(sorted, n)
	}
	sort.Strings(sorted)
	for _, n := range sorted {
		if health[n] != pHealth[n] {
			logger.Infof("%s %-16s %s → %s", stamp, n, orUnknown(pHealth[n]), orUnknown(health[n]))
		}
	}
	if failures > pFailures {
		logger.Warnf("%s observation failed (%d so far), class %q", stamp, failures, class)
	} else if class != pClass {
		logger.Infof("%s lastErrorClass %q → %q", stamp, pClass, class)
	}
	if ready != pReady {
		logger.Infof("%s ready %v → %v", stamp, pReady, ready)
	}
	if wouldSign != pWouldSign {
		logger.Warnf("%s wouldSign %v → %v (observation age %ds)", stamp, pWouldSign, wouldSign, age)
	}
}

// observed picks the fields the change report is built from; a sample without
// an answer reads as empty.
func observed(s *sample) (health map[string]string, class string, ready, wouldSign bool, failures int, age uint64) {
	if s.Readiness == nil {
		return map[string]string{}, "", false, false, 0, 0
	}
	r := s.Readiness
	return r.SourceHealth, r.LastErrorClass, r.Readiness.Ready, r.WouldSign, r.Failures, r.ObservationAge
}

func orUnknown(v string) string {
	if v == "" {
		return "(absent)"
	}
	return v
}

func openRecords(path string) (io.Writer, func(), error) {
	if path == "" {
		return os.Stdout, func() {}, nil
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, nil, fmt.Errorf("creating the directory of %s: %w", path, err)
	}
	f, err := os.OpenFile(path, os.O_APPEND|os.O_CREATE|os.O_WRONLY, 0o644)
	if err != nil {
		return nil, nil, fmt.Errorf("opening %s: %w", path, err)
	}
	return f, func() { _ = f.Close() }, nil
}
