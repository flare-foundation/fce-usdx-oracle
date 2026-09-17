// Command retire-tee takes a machine out of the registry's active set by pausing
// it, the registry's one-way retirement.
//
//	go run ./cmd/retire-tee -p http://localhost:6684        # the machine behind that proxy
//	go run ./cmd/retire-tee -tee 0x…                         # a machine by id
//	go run ./cmd/retire-tee -stale [-dry-run]                # every active machine no proxy answers for
//
// A machine's identity is its enclave key, which lives only in the running
// process: stopping the stack loses it, and the next start registers a new
// machine. Without this step the old registrations stay active forever, the
// sender keeps dispatching every publication to them (and quoting the fee for
// each), and the relay keeps failing to deliver. stop-services.sh runs the -p
// form before it stops a stack.
//
// Pausing is the machine owner's call (MACHINE_OWNER_KEY, or the extension
// owner's key where they are one address) on a
// machine at PRODUCTION; a paused machine is never dispatched to and its
// updates are rejected, and there is no unpause. -stale finds the machines
// whose registered URL either does not answer or answers as a different
// machine, which is what a machine from an earlier stack looks like; it never
// touches a machine whose URL answers with its own id.
package main

import (
	"context"
	"flag"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/configs"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

// The registry's TeeStatus values, as fleet-status names them.
const (
	statusProduction uint8 = 2
	statusPaused     uint8 = 4
)

// infoTimeout bounds each probe of a registered URL. A dead stack refuses at
// once; a URL that hangs must not stall the sweep.
const infoTimeout = 5 * time.Second

func main() {
	af := flag.String("a", configs.AddressesFile, "file with deployed addresses")
	cf := flag.String("c", configs.ChainNodeURL, "chain node url")
	pf := flag.String("p", "", "proxy URL of the machine to retire; its id is read from /info")
	teeF := flag.String("tee", "", "id of the machine to retire")
	extensionIDF := flag.String("extensionId", configs.ExtensionID, "extension whose active machines -stale sweeps (default: EXTENSION_ID from .env)")
	stale := flag.Bool("stale", false,
		"retire every active machine whose registered URL does not answer, or answers as another machine")
	dryRun := flag.Bool("dry-run", false, "report what would be paused and send nothing")
	flag.Parse()

	modes := 0
	for _, on := range []bool{*pf != "", *teeF != "", *stale} {
		if on {
			modes++
		}
	}
	if modes != 1 {
		fccutils.FatalWithCause(errors.New("pass exactly one of -p, -tee or -stale"))
	}

	s, err := support.DefaultSupport(*af, *cf)
	if err != nil {
		fccutils.FatalWithCause(err)
	}
	// Pausing is the machine owner's, like registration: the deployment key by
	// default, the machine owner's key where the two differ.
	if key, keyErr := fccutils.OverrideKey("MACHINE_OWNER_KEY"); keyErr != nil {
		fccutils.FatalWithCause(keyErr)
	} else if key != nil {
		s.Prv = key
	}
	owner := crypto.PubkeyToAddress(s.Prv.PublicKey)

	var targets []common.Address
	switch {
	case *teeF != "":
		if !common.IsHexAddress(*teeF) {
			fccutils.FatalWithCause(errors.Errorf("%q is not an address", *teeF))
		}
		targets = []common.Address{common.HexToAddress(*teeF)}
	case *pf != "":
		id, err := machineAt(*pf)
		if err != nil {
			fccutils.FatalWithCause(errors.Errorf("reading the machine behind %s: %s", *pf, err))
		}
		logger.Infof("%s fronts machine %s", strings.TrimRight(*pf, "/"), id.Hex())
		targets = []common.Address{id}
	case *stale:
		extensionID, ok := new(big.Int).SetString(*extensionIDF, 0)
		if !ok || extensionID.Sign() <= 0 {
			fccutils.FatalWithCause(errors.Errorf("%q is not a positive extension id", *extensionIDF))
		}
		targets, err = staleMachines(s, extensionID)
		if err != nil {
			fccutils.FatalWithCause(err)
		}
		if len(targets) == 0 {
			logger.Infof("every active machine on extension %s answers as itself; nothing to retire", extensionID)
			return
		}
	}

	failed := 0
	for _, id := range targets {
		if err := retire(s, owner, id, *dryRun); err != nil {
			logger.Errorf("%s: %v", id.Hex(), err)
			failed++
		}
	}
	if failed > 0 {
		fccutils.FatalWithCause(errors.Errorf("%d of %d machine(s) not retired", failed, len(targets)))
	}
}

// retire pauses one machine after checking that this key may and that the
// status allows it. Idempotent: an already paused machine is reported and skipped.
func retire(s *support.Support, owner, id common.Address, dryRun bool) error {
	opts := &bind.CallOpts{Context: context.Background()}
	machineOwner, err := s.TeeMachineRegistry.GetTeeMachineOwner(opts, id)
	if err != nil {
		return errors.Errorf("reading the owner: %s", err)
	}
	if machineOwner == (common.Address{}) {
		return errors.New("not a registered machine")
	}
	if machineOwner != owner {
		return errors.Errorf("owned by %s, not by this key (%s); only the owner can pause it", machineOwner.Hex(), owner.Hex())
	}
	status, err := s.TeeMachineRegistry.GetTeeMachineStatus(opts, id)
	if err != nil {
		return errors.Errorf("reading the status: %s", err)
	}
	switch status {
	case statusPaused:
		logger.Infof("%s is already paused", id.Hex())
		return nil
	case statusProduction:
	default:
		return errors.Errorf("status %d: only a PRODUCTION machine can be paused by its owner "+
			"(a machine that never reached production is not dispatched to anyway)", status)
	}
	if dryRun {
		logger.Infof("would pause %s", id.Hex())
		return nil
	}

	txOpts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return errors.Errorf("%s", err)
	}
	tx, err := s.TeeMachineRegistry.Pause(txOpts, id)
	if err != nil {
		return errors.Errorf("pause: %s", err)
	}
	if _, err := support.CheckTx(tx, s.ChainClient); err != nil {
		return errors.Errorf("%s", err)
	}
	logger.Infof("paused %s (tx %s); it is no longer dispatched to", id.Hex(), tx.Hash().Hex())
	return nil
}

// staleMachines lists the active machines of an extension whose registered URL
// does not answer, or answers as a different machine. Both are what a machine
// from a stack that was stopped or restarted looks like; a URL that answers
// with the machine's own id is live and left alone.
func staleMachines(s *support.Support, extensionID *big.Int) ([]common.Address, error) {
	active, err := s.TeeMachineRegistry.GetActiveTeeMachines(&bind.CallOpts{Context: context.Background()}, extensionID)
	if err != nil {
		return nil, errors.Errorf("listing the active machines of extension %s: %s", extensionID, err)
	}
	logger.Infof("extension %s has %d active machine(s)", extensionID, len(active.TeeIds))

	var stale []common.Address
	for i, id := range active.TeeIds {
		url := ""
		if i < len(active.Urls) {
			url = strings.TrimRight(strings.TrimSpace(active.Urls[i]), "/")
		}
		if url == "" {
			logger.Infof("  %s  no url registered — stale", id.Hex())
			stale = append(stale, id)
			continue
		}
		live, err := machineAt(url)
		switch {
		case err != nil:
			logger.Infof("  %s  %s does not answer (%v) — stale", id.Hex(), url, err)
			stale = append(stale, id)
		case live != id:
			logger.Infof("  %s  %s answers as %s — stale", id.Hex(), url, live.Hex())
			stale = append(stale, id)
		default:
			logger.Infof("  %s  %s answers as itself — live, kept", id.Hex(), url)
		}
	}
	return stale, nil
}

// machineAt returns the id of the machine a proxy fronts, derived from the
// attested public key in its /info, within infoTimeout.
func machineAt(proxyURL string) (common.Address, error) {
	type result struct {
		id  common.Address
		err error
	}
	done := make(chan result, 1)
	go func() {
		info, err := fccutils.TeeInfo(strings.TrimRight(proxyURL, "/"))
		if err != nil {
			done <- result{err: err}
			return
		}
		id, _, err := fccutils.TeeProxyId(info)
		done <- result{id: id, err: err}
	}()
	select {
	case r := <-done:
		return r.id, r.err
	case <-time.After(infoTimeout):
		return common.Address{}, fmt.Errorf("no answer within %s", infoTimeout)
	}
}
