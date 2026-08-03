// Package setup assembles the extension from its pinned configuration.
//
// # What stays in the image, and why only this
//
// Endpoints arrive by on-chain instruction and credentials by direct
// instruction (see internal/runtimecfg and internal/provision). What remains
// compiled in with go:embed is the part that must not be substitutable:
//
//   - the host allowlist, which bounds every endpoint that can ever be
//     configured. Without it, whoever can set endpoints can set the price, and
//     publishing endpoints on-chain would be a substitution channel rather than
//     a transparency measure.
//   - the target binding — chain id, store address, feed id, decimals — which
//     the signed report commits to. Caller-supplied, anyone could obtain a TEE
//     signature over a report bound to a store they control.
//   - the observation window and deadline, since widening the window weakens
//     the TOCTOU bound the conservative double-read depends on.
//
// The admin signer set is pinned too, in internal/provision.
//
// The network is selected at BUILD time via -ldflags, not at runtime:
//
//	go build -ldflags "-X fce-usdx-oracle/internal/setup.network=mainnet"
//
// so mainnet and Coston2 are different artifacts with different code hashes.
package setup

import (
	"encoding/json"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/internal/provision"
	"fce-usdx-oracle/internal/report"
	"fce-usdx-oracle/internal/runtimecfg"
	"fce-usdx-oracle/internal/teenode"

	"github.com/ethereum/go-ethereum/common"
)

// network selects which embedded config file is used. Set at build time with
// -ldflags "-X fce-usdx-oracle/internal/setup.network=mainnet".
var network = "coston2"

// SchemaVersion is the config schema this build understands. A mismatch is a
// hard failure: silently ignoring unknown fields in a file that bounds the
// price is how a stale config keeps a feed running on wrong values.
const SchemaVersion = 2

// File is the pinned configuration schema.
type File struct {
	SchemaVersion int         `json:"schemaVersion"`
	Network       string      `json:"network"`
	Price         Price       `json:"price"`
	Target        Target      `json:"target"`
	Observation   Observation `json:"observation"`

	// AllowedHosts bounds every endpoint that can ever be configured.
	AllowedHosts []string `json:"allowedHosts"`
}

// Price is the output encoding.
type Price struct {
	Decimals   int8   `json:"decimals"`
	HaircutBps uint16 `json:"haircutBps"`
}

// Target binds the signed report to one deployment.
type Target struct {
	ChainID      uint64 `json:"chainId"`
	Store        string `json:"store"`
	FeedCategory uint8  `json:"feedCategory"`
	FeedName     string `json:"feedName"`
}

// Observation bounds the timing of one price observation.
type Observation struct {
	DeadlineMs int `json:"deadlineMs"`
	WindowMs   int `json:"windowMs"`
}

// Extension is everything the server needs to run.
type Extension struct {
	Store       *runtimecfg.Store
	Provisioner *runtimecfg.Provisioner
	Node        *teenode.Client
}

// Load reads the embedded configuration and returns an unprovisioned Extension.
//
// It does NOT wait for provisioning: endpoints and credentials arrive later, by
// instruction. The extension serves not_configured until they do.
func Load(signPort int) (*Extension, error) {
	f, err := parse(network)
	if err != nil {
		return nil, err
	}

	pinned, err := Pinned(f)
	if err != nil {
		return nil, err
	}

	node := teenode.New(signPort, 10*time.Second)
	store := runtimecfg.New(pinned)
	// The machine's own address is resolved lazily on first provisioning.
	verifier := provision.NewVerifier(pinned.ChainID, common.Address{})

	return &Extension{
		Store:       store,
		Provisioner: runtimecfg.NewProvisioner(store, verifier, node),
		Node:        node,
	}, nil
}

// parse decodes and version-checks an embedded config.
func parse(name string) (File, error) {
	raw, err := embeddedConfig(name)
	if err != nil {
		return File{}, err
	}

	var f File
	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&f); err != nil {
		return File{}, fmt.Errorf("setup: parsing %s config: %w", name, err)
	}
	if f.SchemaVersion != SchemaVersion {
		return File{}, fmt.Errorf(
			"setup: config schemaVersion %d, this build understands %d",
			f.SchemaVersion, SchemaVersion)
	}
	if f.Network != name {
		return File{}, fmt.Errorf(
			"setup: config declares network %q but this build selected %q", f.Network, name)
	}
	return f, nil
}

// Pinned converts a parsed File into the immutable half of the configuration.
func Pinned(f File) (runtimecfg.Pinned, error) {
	var p runtimecfg.Pinned

	feedID, err := report.ParseFeedID(f.Target.FeedCategory, f.Target.FeedName)
	if err != nil {
		return p, fmt.Errorf(
			"setup: feed category %d / name %q is invalid (category must be 32..63, "+
				"name 1..20 ASCII bytes)", f.Target.FeedCategory, f.Target.FeedName)
	}

	if !common.IsHexAddress(f.Target.Store) {
		return p, errors.New("setup: target.store is not an address")
	}
	store := common.HexToAddress(f.Target.Store)
	if store == (common.Address{}) {
		return p, errors.New("setup: target.store is the zero address")
	}
	if f.Target.ChainID == 0 {
		return p, errors.New("setup: target.chainId is required")
	}
	if f.Price.Decimals < 0 {
		return p, errors.New("setup: price.decimals must be non-negative")
	}
	if f.Observation.DeadlineMs <= 0 || f.Observation.WindowMs <= 0 {
		return p, errors.New("setup: observation deadline and window must be positive")
	}

	if len(f.AllowedHosts) == 0 {
		// An empty allowlist would permit nothing, which fails safe but is
		// certainly a mistake — nothing could ever be configured.
		return p, errors.New("setup: allowedHosts must not be empty")
	}
	for _, h := range f.AllowedHosts {
		if h == "" || strings.Contains(h, "/") || strings.Contains(h, ":") ||
			strings.Contains(h, "*") {
			// Hostnames only. A scheme, port or wildcard here would widen the
			// allowlist in ways the exact-match check cannot express.
			return p, fmt.Errorf("setup: allowedHosts entry %q must be a bare hostname", h)
		}
	}

	return runtimecfg.Pinned{
		AllowedHosts: f.AllowedHosts,
		ChainID:      new(big.Int).SetUint64(f.Target.ChainID),
		Store:        store,
		FeedID:       feedID,
		Decimals:     f.Price.Decimals,
		HaircutBps:   f.Price.HaircutBps,
		Deadline:     time.Duration(f.Observation.DeadlineMs) * time.Millisecond,
		Window:       time.Duration(f.Observation.WindowMs) * time.Millisecond,
	}, nil
}
