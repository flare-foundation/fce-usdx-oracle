package keeper

import (
	"context"
	"strings"

	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
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

// ActiveMachines lists the machines registered to the store's extension. The id is
// read off the store, not configured, so the keeper cannot rotate over a fleet whose
// prices the store would reject. Machines listed without a URL are dropped.
func ActiveMachines(
	ctx context.Context, s *support.Support, storeAddr common.Address, proxyOverride string,
) ([]Machine, error) {
	opts := &bind.CallOpts{Context: ctx}

	store, err := usdxstore.NewUsdxPriceStoreCaller(storeAddr, s.ChainClient)
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

// fleet is the keeper's view of the machines it may send to, plus whose turn it is
// next. The rotation index is the only state the loop keeps across cycles, and it
// is not correctness-bearing: any registered machine may serve any cycle.
type fleet struct {
	machines []Machine
	next     int
}

// refresh re-reads the registry so machines added or suspended mid-run are picked
// up without a restart. A read failure keeps the previous list: a briefly
// unreadable registry is not a reason to stop sending prices.
func (f *fleet) refresh(ctx context.Context, s *support.Support, storeAddr common.Address, proxyOverride string) {
	machines, err := ActiveMachines(ctx, s, storeAddr, proxyOverride)
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

	if len(machines) != len(f.machines) {
		logger.Infof("fleet is now %d machine(s)", len(machines))
	}
	f.machines = machines
	if f.next >= len(f.machines) {
		f.next = 0
	}
}

// take returns the machine whose turn it is and advances the rotation.
func (f *fleet) take() (Machine, bool) {
	if len(f.machines) == 0 {
		return Machine{}, false
	}
	m := f.machines[f.next%len(f.machines)]
	f.next = (f.next + 1) % len(f.machines)
	return m, true
}

// proxies is every machine's URL, which is what an on-chain cycle has to poll:
// the instruction goes to a machine the registry picks at random and the result
// is retrievable only from that machine's own proxy.
func (f *fleet) proxies() []string {
	out := make([]string, 0, len(f.machines))
	for _, m := range f.machines {
		out = append(out, m.Proxy)
	}
	return out
}
