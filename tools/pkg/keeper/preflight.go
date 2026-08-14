package keeper

import (
	"context"
	"fmt"

	"fce-usdx-oracle/tools/pkg/contracts/usdxoracle"
	"fce-usdx-oracle/tools/pkg/contracts/usdxstore"
	"fce-usdx-oracle/tools/pkg/pricereport"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// Rejection is a report the store will not accept, named by the guard that
// rejects it. The name matters more than the message: StaleConfig needs a
// setEndpoints call, NotNewer usually needs nothing, and "execution reverted"
// distinguishes neither.
type Rejection struct {
	Guard string
	Msg   string

	// Set for NotNewer, so the caller can tell a lost race from a stale submission.
	StoreObservedAt uint64
}

func (r *Rejection) Error() string { return r.Msg }

func reject(guard, format string, args ...any) *Rejection {
	return &Rejection{Guard: guard, Msg: guard + ": " + fmt.Sprintf(format, args...)}
}

// AsRejection reports whether err is a named store rejection.
func AsRejection(err error) (*Rejection, bool) {
	var r *Rejection
	if errors.As(err, &r) {
		return r, true
	}
	return nil, false
}

// Preflight checks a report against every store guard before spending gas.
// Each check duplicates one the contract makes, turning a revert into a named,
// actionable reason before a transaction is paid for.
func Preflight(s *support.Support, storeAddr common.Address, r pricereport.Report) error {
	store, err := usdxstore.NewUsdxPriceStoreCaller(storeAddr, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the store: %s", err)
	}
	opts := &bind.CallOpts{Context: context.Background()}

	domain, err := store.DOMAIN(opts)
	if err != nil {
		return errors.Errorf(
			"reading DOMAIN from %s: %s — check the address really is a UsdxPriceStore",
			storeAddr.Hex(), err)
	}
	if common.Hash(domain) != r.Domain {
		return reject("WrongDomain", "report carries %s, the store expects %s",
			r.Domain.Hex(), common.Hash(domain).Hex())
	}

	if decimals, err := store.DECIMALS(opts); err != nil {
		return errors.Errorf("reading DECIMALS: %s", err)
	} else if decimals != r.Decimals {
		return reject("WrongDecimals", "report is scaled to %d, the store to %d",
			r.Decimals, decimals)
	}

	if paused, err := store.Paused(opts); err != nil {
		return errors.Errorf("reading paused: %s", err)
	} else if paused {
		return reject("IsPaused", "the store is paused and accepts no submissions")
	}

	observedAt, err := store.ObservedAt(opts)
	if err != nil {
		return errors.Errorf("reading observedAt: %s", err)
	}
	if r.ObservedAt <= observedAt {
		rj := reject("NotNewer",
			"the store already holds observedAt=%d and this report is stamped %d",
			observedAt, r.ObservedAt)
		rj.StoreObservedAt = observedAt
		return rj
	}

	if err := checkBounds(store, opts, r); err != nil {
		return err
	}
	if err := checkAge(s, store, opts, r); err != nil {
		return err
	}
	return checkSignerAndConfig(s, store, opts, r)
}

func checkBounds(store *usdxstore.UsdxPriceStoreCaller, opts *bind.CallOpts, r pricereport.Report) error {
	minPrice, err := store.MinPrice(opts)
	if err != nil {
		return errors.Errorf("reading minPrice: %s", err)
	}
	maxPrice, err := store.MaxPrice(opts)
	if err != nil {
		return errors.Errorf("reading maxPrice: %s", err)
	}
	if r.Price.Cmp(minPrice) < 0 || r.Price.Cmp(maxPrice) > 0 {
		// Bounds set at par mean a genuine depeg reverts here rather than
		// publishing — a governance decision, not a tooling bug.
		return reject("OutOfBounds", "price %s is outside the accepted band [%s, %s]",
			r.Price, minPrice, maxPrice)
	}
	return nil
}

func checkAge(s *support.Support, store *usdxstore.UsdxPriceStoreCaller, opts *bind.CallOpts, r pricereport.Report) error {
	head, err := s.ChainClient.HeaderByNumber(opts.Context, nil)
	if err != nil {
		return errors.Errorf("reading chain head: %s", err)
	}
	now := head.Time

	skew, err := store.MAXFUTURESKEW(opts)
	if err != nil {
		return errors.Errorf("reading MAX_FUTURE_SKEW: %s", err)
	}
	if r.ObservedAt > now+skew {
		return reject("TooFarAhead",
			"report is stamped %d, chain time is %d and the skew allowance is %ds — check the TEE's clock",
			r.ObservedAt, now, skew)
	}

	maxAge, err := store.MaxAge(opts)
	if err != nil {
		return errors.Errorf("reading maxAge: %s", err)
	}
	if r.ObservedAt < now && now-r.ObservedAt > maxAge {
		return reject("TooOld",
			"report is %ds old and the store accepts %ds — the poll and submit deadlines have to fit inside maxAge",
			now-r.ObservedAt, maxAge)
	}
	return nil
}

// checkSignerAndConfig asks the store's two questions: is this machine
// registered to our extension, and is it running the configuration we published?
func checkSignerAndConfig(
	s *support.Support, store *usdxstore.UsdxPriceStoreCaller, opts *bind.CallOpts, r pricereport.Report,
) error {
	teeID, err := r.Signer()
	if err != nil {
		return reject("BadSignature", "report does not recover to a signer: %s", err)
	}

	extensionID, err := store.ExtensionId(opts)
	if err != nil {
		return errors.Errorf("reading extensionId: %s", err)
	}
	signerExtension, err := s.TeeMachineRegistry.GetExtensionId(opts, teeID)
	if err != nil {
		return reject("UnknownSigner",
			"the registry does not know %s — is the machine registered? (%s)", teeID.Hex(), err)
	}
	if signerExtension.Cmp(extensionID) != 0 {
		return reject("UnknownSigner", "%s is registered to extension %s, the store accepts %s",
			teeID.Hex(), signerExtension, extensionID)
	}

	status, err := store.REQUIREDTEESTATUS(opts)
	if err != nil {
		return errors.Errorf("reading REQUIRED_TEE_STATUS: %s", err)
	}
	machineStatus, err := s.TeeMachineRegistry.GetTeeMachineStatus(opts, teeID)
	if err != nil {
		return errors.Errorf("reading the machine status of %s: %s", teeID.Hex(), err)
	}
	if machineStatus != status {
		return reject("UnknownSigner", "machine %s is in status %d, the store requires %d (PRODUCTION)",
			teeID.Hex(), machineStatus, status)
	}

	configSource, err := store.ConfigSource(opts)
	if err != nil {
		return errors.Errorf("reading configSource: %s", err)
	}
	sender, err := usdxoracle.NewUsdxOracleInstructionSenderCaller(configSource, s.ChainClient)
	if err != nil {
		return errors.Errorf("binding the config source: %s", err)
	}
	expected, err := sender.ExpectedConfigHash(opts, teeID)
	if err != nil {
		return reject("ConfigSourceUnavailable", "reading expectedConfigHash from %s: %s",
			configSource.Hex(), err)
	}
	if common.Hash(expected) == (common.Hash{}) {
		return reject("NoConfigPublished",
			"no endpoint configuration has been sent to %s — run send-endpoints first", teeID.Hex())
	}
	if common.Hash(expected) != r.ConfigHash {
		return reject("StaleConfig",
			"the machine computed this price under %s but %s expects %s; "+
				"the machine never applied the published configuration, or a newer one was sent",
			r.ConfigHash.Hex(), configSource.Hex(), common.Hash(expected).Hex())
	}

	// The same check for the admin set. Without it this preflight would report a
	// submittable report and the transaction would then revert StaleAdmins,
	// which is exactly the confusion it exists to prevent.
	expectedAdmins, err := sender.ExpectedAdminsHash(opts, teeID)
	if err != nil {
		return reject("ConfigSourceUnavailable", "reading expectedAdminsHash from %s: %s",
			configSource.Hex(), err)
	}
	if common.Hash(expectedAdmins) == (common.Hash{}) {
		return reject("NoAdminsPublished",
			"no admin set has been sent to %s — run send-admins first", teeID.Hex())
	}
	if common.Hash(expectedAdmins) != r.AdminsHash {
		return reject("StaleAdmins",
			"the machine computed this price under admin set %s but %s expects %s; "+
				"the machine never applied the published admin set, or a newer one was sent",
			r.AdminsHash.Hex(), configSource.Hex(), common.Hash(expectedAdmins).Hex())
	}
	return nil
}
