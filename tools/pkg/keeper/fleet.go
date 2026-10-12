package keeper

import (
	"context"
	"slices"
	"strings"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/contracts/teeoraclestore"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/flare-foundation/go-flare-common/pkg/logger"
	"github.com/pkg/errors"
)

// Machine is one TEE machine the registry lists for our extension, with the
// proxy URL to reach it on.
type Machine struct {
	TeeID common.Address
	Proxy string
}

// ActiveMachines lists the machines registered to extension extensionID.
//
// The id is read off the store, not configured, so the keeper cannot poll a
// fleet whose updates the store would reject. Machines listed without a URL
// are dropped — a machine reachable at a different address than it registered
// can be reached via ProxyOverride on LoopConfig.
func ActiveMachines(
	ctx context.Context, s *support.Support, storeAddr common.Address, proxyOverride string,
) ([]Machine, error) {
	opts := &bind.CallOpts{Context: ctx}

	store, err := teeoraclestore.NewIITeeOracleFeedStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the store: %s", err)
	}
	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return nil, errors.Errorf("reading the store's extension id: %s", err)
	}

	active, err := s.TeeMachineRegistry.GetActiveTeeMachines(opts, extensionID)
	if err != nil {
		return nil, errors.Errorf("listing active machines for extension %s: %s", extensionID, err)
	}

	return machineList(active.TeeIds, active.Urls, proxyOverride), nil
}

// machineList pairs each machine with the URL to reach it on.
func machineList(teeIDs []common.Address, urls []string, proxyOverride string) []Machine {
	machines := make([]Machine, 0, len(teeIDs))

	for i, id := range teeIDs {
		url := proxyOverride
		if url == "" && i < len(urls) {
			url = urls[i]
		}
		url = strings.TrimRight(strings.TrimSpace(url), "/")
		if url == "" {
			logger.Warnf("machine %s has no registered url — it cannot be reached directly; "+
				"re-register it with one, or pass -p to override every url", id.Hex())
			continue
		}
		machines = append(machines, Machine{TeeID: id, Proxy: url})
	}
	return machines
}

// RequestableMachines narrows machines to the ones a feed request may name.
// requestFeedUpdate reverts as a whole (TeeIdNotConfigured) if any target is
// behind the feed's latest endpoints or admins publication, so one machine
// waiting for a push would otherwise stop the feed. The sender's push-target
// views list exactly those machines, from the same per-machine records the
// request checks.
func RequestableMachines(
	ctx context.Context, s *support.Support, senderAddr common.Address, feedID [21]byte, machines []Machine,
) ([]common.Address, error) {
	opts := &bind.CallOpts{Context: ctx}

	sender, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddr, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding the instructions sender: %s", err)
	}
	endpointsBehind, err := sender.GetEndpointsPushTargets(opts, feedID)
	if err != nil {
		return nil, errors.Errorf("reading the machines behind the endpoints publication: %s", err)
	}
	adminsBehind, err := sender.GetAdminsPushTargets(opts, feedID)
	if err != nil {
		return nil, errors.Errorf("reading the machines behind the admins publication: %s", err)
	}
	return requestable(machines, endpointsBehind, adminsBehind), nil
}

// requestable is every machine not named in any of the behind lists, in fleet
// order.
func requestable(machines []Machine, behind ...[]common.Address) []common.Address {
	skip := map[common.Address]bool{}
	for _, list := range behind {
		for _, id := range list {
			skip[id] = true
		}
	}
	out := make([]common.Address, 0, len(machines))
	for _, m := range machines {
		if !skip[m.TeeID] {
			out = append(out, m.TeeID)
		}
	}
	return out
}

// fleet is the keeper's view of the machines that could serve its instruction,
// re-read every cycle: each addressed machine files its result on its own proxy,
// so every one is polled.
type fleet struct {
	machines []Machine

	// requestable are the machines a request may name: active, reachable and at
	// the feed's latest configuration. Read only for a keeper that picks its own
	// targets.
	requestable []common.Address
}

// refresh re-reads the registry so machines added, restarted or paused mid-run
// are picked up without a restart. A read failure keeps the previous lists: a
// briefly unreadable registry is not a reason to stop sending prices.
func (f *fleet) refresh(ctx context.Context, s *support.Support, cfg LoopConfig) {
	machines, err := ActiveMachines(ctx, s, cfg.Store, cfg.ProxyOverride)
	if err != nil {
		logger.Warnf("could not list the fleet, keeping the previous %d machine(s): %s",
			len(f.machines), err)
		return
	}
	if len(machines) == 0 {
		logger.Warnf("the registry lists no reachable machine for this extension, keeping the previous %d",
			len(f.machines))
		return
	}

	if !sameAddresses(teeIDsOf(machines), teeIDsOf(f.machines)) {
		logger.Infof("fleet is now %d machine(s): %s", len(machines), describeMachines(machines))
	}
	f.machines = machines

	if !cfg.picksTargets() {
		return
	}
	targets, err := RequestableMachines(ctx, s, cfg.Sender, cfg.FeedID, machines)
	if err != nil {
		logger.Warnf("could not read which machines are at the feed's latest configuration, "+
			"keeping the previous %d target(s): %s", len(f.requestable), err)
		return
	}
	if !sameAddresses(targets, f.requestable) {
		logger.Infof("requesting from %d machine(s): %s", len(targets), hexList(targets))
		for _, m := range machines {
			if !slices.Contains(targets, m.TeeID) {
				logger.Warnf("machine %s is behind the feed's latest endpoints or admins publication — "+
					"left out of requests until pushEndpoints / pushAdmins reaches it", m.TeeID.Hex())
			}
		}
	}
	f.requestable = targets
}

func teeIDsOf(machines []Machine) []common.Address {
	out := make([]common.Address, 0, len(machines))
	for _, m := range machines {
		out = append(out, m.TeeID)
	}
	return out
}

// sameAddresses compares two lists as sets: the registry's order is not a
// change worth reporting.
func sameAddresses(a, b []common.Address) bool {
	if len(a) != len(b) {
		return false
	}
	for _, id := range a {
		if !slices.Contains(b, id) {
			return false
		}
	}
	return true
}

func hexList(ids []common.Address) string {
	out := make([]string, 0, len(ids))
	for _, id := range ids {
		out = append(out, id.Hex())
	}
	return strings.Join(out, ", ")
}

func describeMachines(machines []Machine) string {
	out := make([]string, 0, len(machines))
	for _, m := range machines {
		out = append(out, m.TeeID.Hex()+" at "+m.Proxy)
	}
	return strings.Join(out, ", ")
}

// proxies is every machine's URL: a machine's result is retrievable only from
// its own proxy.
func (f *fleet) proxies() []string {
	out := make([]string, 0, len(f.machines))
	for _, m := range f.machines {
		out = append(out, m.Proxy)
	}
	return out
}
