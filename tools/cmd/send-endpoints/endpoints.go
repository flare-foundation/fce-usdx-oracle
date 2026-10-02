package main

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"io"
	"sort"
	"strconv"
	"strings"

	"fce-usdx-oracle/pkg/protocol"
	"fce-usdx-oracle/pkg/wire/configpayload"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// The SET_ENDPOINTS payload schema and the rules a machine applies to it. Worth
// checking because a bad payload fails SILENTLY: latestEndpointsHash is written,
// every machine refuses the instruction, and every submitFeedUpdates after it
// reverts StaleEndpoints.
//
// The on-chain payload is ABI-encoded by the sender, but this tool takes a JSON
// file for operator convenience and builds the typed EndpointGroup list from it.
// JSON is only an input format; what lands on-chain is what EncodeSetEndpoints
// packs, and that is what EndpointsHash predicts.
//
// Each endpoint is PUBLIC (url on-chain) or PRIVATE (a salted commitment on-chain,
// url delivered off-chain under the secret ref):
//
//	{ "flare": { "threshold": 2, "endpoints": [
//	    { "url": "https://flare-api.flare.network/ext/C/rpc" },
//	    { "url": "https://rpc.ankr.com/flare/{secret}", "secret": "ankr_flare_key" }
//	]}}
//
// A PRIVATE endpoint carries urlHash instead of url:
//
//	{ "hex_cash": { "threshold": 1, "endpoints": [
//	    { "urlHash": "0x…", "secret": "hex_cash_url" }
//	]}}
type groupConfig struct {
	Threshold int              `json:"threshold"`
	Endpoints []endpointConfig `json:"endpoints"`
}

type endpointConfig struct {
	URL     string `json:"url"`
	URLHash string `json:"urlHash"`
	Secret  string `json:"secret,omitempty"`
}

// secretPlaceholder is the token the enclave replaces at dial time.
const secretPlaceholder = "{secret}"

// hexCashGroup is the cash endpoint's group tag. The extension pins it beside
// the bonds URL in internal/config (HexCashGroup), which the tools do not
// import, so it is the one group tag restated here.
const hexCashGroup = "hex_cash"

// maxPayloadBytes is the largest SET_ENDPOINTS message a machine decodes
// (internal/provisioning/store). The sender sets no limit of its own, so a
// larger payload is recorded on chain and refused by every machine.
const maxPayloadBytes = 256 << 10

// knownGroups are the tags the extension build pins: one per pinned chain
// (protocol.Chains), then hex_cash. A payload missing one undercounts supply,
// which OVERSTATES the price — so a missing group is rejected rather than
// treated as "that chain is off". An unknown group is likewise rejected:
// publisher and build disagreeing about the schema stops the machine rather
// than applying a subset.
var knownGroups = groupTags()

func groupTags() []string {
	tags := make([]string, 0, len(protocol.Chains)+1)
	for _, c := range protocol.Chains {
		tags = append(tags, c.Name)
	}
	return append(tags, hexCashGroup)
}

// parseEndpoints decodes the operator's JSON into the typed group list. Unknown
// fields are refused: a misspelt key (secretRef for secret, urlhash for
// urlHash) would otherwise be dropped without a word. It judges the JSON's
// shape only; validateEndpoints applies the machine's rules.
func parseEndpoints(raw []byte) ([]configpayload.Group, error) {
	var e map[string]groupConfig
	dec := json.NewDecoder(bytes.NewReader(raw))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, errors.Errorf("payload is not a known configuration: %s", err)
	}
	if _, err := dec.Token(); err != io.EOF {
		return nil, errors.New("payload is not a known configuration: trailing data after the object")
	}

	groups := make([]configpayload.Group, 0, len(e))
	for _, name := range sortedKeys(e) {
		group, err := buildGroup(name, e[name])
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// buildGroup turns one config entry into a typed EndpointGroup.
func buildGroup(name string, gc groupConfig) (configpayload.Group, error) {
	tag, err := configpayload.Tag(name)
	if err != nil {
		return configpayload.Group{}, errors.Errorf("group %q: %s", name, err)
	}
	threshold, err := configpayload.Threshold(gc.Threshold)
	if err != nil {
		return configpayload.Group{}, errors.Errorf("group %q: %s", name, err)
	}

	endpoints := make([]configpayload.Endpoint, 0, len(gc.Endpoints))
	for i, ec := range gc.Endpoints {
		endpoint, err := buildEndpoint(name, i, ec)
		if err != nil {
			return configpayload.Group{}, err
		}
		endpoints = append(endpoints, endpoint)
	}

	return configpayload.Group{Group: tag, Threshold: threshold, Endpoints: endpoints}, nil
}

// buildEndpoint classifies one endpoint as PUBLIC or PRIVATE by which fields it
// carries. The secret ref is trimmed, as the machine trims it, so the ref
// published is the one a SET_SECRET must name.
func buildEndpoint(group string, i int, ec endpointConfig) (configpayload.Endpoint, error) {
	hasURL := ec.URL != ""
	hasHash := ec.URLHash != ""
	secret := strings.TrimSpace(ec.Secret)

	switch {
	case hasURL && hasHash:
		return configpayload.Endpoint{}, errors.Errorf(
			"group %q endpoint %d: url and urlHash are mutually exclusive — pick PUBLIC (url) or PRIVATE (urlHash)",
			group, i+1)
	case hasURL:
		return configpayload.Endpoint{Kind: configpayload.KindPublic, Url: ec.URL, SecretRef: secret}, nil
	case hasHash:
		hash, ok := parseHash32(ec.URLHash)
		if !ok || hash == (common.Hash{}) {
			return configpayload.Endpoint{}, errors.Errorf(
				"group %q endpoint %d: urlHash %q is not a non-zero 32-byte hash", group, i+1, ec.URLHash)
		}
		return configpayload.Endpoint{Kind: configpayload.KindPrivate, UrlHash: hash, SecretRef: secret}, nil
	default:
		return configpayload.Endpoint{}, errors.Errorf(
			"group %q endpoint %d: needs either url (PUBLIC) or urlHash (PRIVATE)", group, i+1)
	}
}

// parseHash32 reads exactly 32 bytes of hex, with or without 0x.
// common.HexToHash silently truncates and pads anything else, which would turn a
// typo into a commitment no provisioned URL can ever match.
func parseHash32(raw string) (common.Hash, bool) {
	text := strings.TrimPrefix(strings.TrimPrefix(strings.TrimSpace(raw), "0x"), "0X")
	b, err := hex.DecodeString(text)
	if err != nil || len(b) != common.HashLength {
		return common.Hash{}, false
	}
	return common.BytesToHash(b), true
}

// validateEndpoints applies, rule for rule, what a machine checks before it
// installs a SET_ENDPOINTS payload (validate and its helpers in
// internal/provisioning/store), so a payload that passes here is one every
// machine of this build accepts on its content. The chain set and the backing
// refs come from pkg/protocol, which the machine is built with.
//
// What the machine also checks, and no publisher can: that the version is
// newer than the one it last applied, which the sender's per-feed counter
// gives by construction; and that every credential it already holds fits the
// new payload. The second refuses, for example, a hex_cash commitment that
// differs from the one a provisioned cash URL was committed under.
func validateEndpoints(feedID [21]byte, groups []configpayload.Group) error {
	// The machine serves exactly one feed and refuses configuration for any
	// other, however well-formed.
	if feedID != protocol.FeedID {
		return errors.Errorf("feed %#x is not the one this extension serves (%#x): every machine would refuse the payload",
			feedID, protocol.FeedID)
	}
	if len(groups) == 0 {
		return errors.New("no groups configured")
	}

	// Duplicates first: a map cannot hold one, and the machine refuses one as it
	// decodes, before any other rule.
	byName := make(map[string]configpayload.Group, len(groups))
	for _, g := range groups {
		name, ok := configpayload.TagString(g.Group)
		if !ok {
			return errors.New("a group tag is not a printable name")
		}
		if _, dup := byName[name]; dup {
			return errors.Errorf("group %q appears more than once", name)
		}
		byName[name] = g
	}

	names := make([]string, 0, len(byName))
	for name := range byName {
		names = append(names, name)
	}
	sort.Strings(names)

	for _, name := range names {
		g := byName[name]
		_, isChain := protocol.LookupChain(name)
		if !isChain && name != hexCashGroup {
			return errors.Errorf("unknown group %q (expected one of %s)",
				name, strings.Join(knownGroups, ", "))
		}
		if len(g.Endpoints) == 0 {
			return errors.Errorf("group %q has no endpoints", name)
		}
		if g.Threshold < 1 || g.Threshold > uint64(len(g.Endpoints)) {
			return errors.Errorf("group %q threshold %d is not between 1 and %d",
				name, g.Threshold, len(g.Endpoints))
		}
		if name == hexCashGroup {
			if err := checkHexCash(g); err != nil {
				return err
			}
		}
		for i, ep := range g.Endpoints {
			if err := checkEndpoint(name, i, isChain, ep); err != nil {
				return err
			}
		}
	}

	for _, name := range knownGroups {
		if _, ok := byName[name]; !ok {
			return errors.Errorf("group %q is missing — a missing chain undercounts supply, "+
				"which overstates the price", name)
		}
	}

	// The version is fixed-width in the encoding, so any non-zero one measures
	// the payload the sender will dispatch.
	payload, err := configpayload.EncodeEndpoints(1, feedID, groups)
	if err != nil {
		return errors.Errorf("encoding the payload: %s", err)
	}
	if len(payload) > maxPayloadBytes {
		return errors.Errorf("the encoded payload is %d bytes; a machine decodes at most %d",
			len(payload), maxPayloadBytes)
	}
	return nil
}

// checkHexCash pins the restricted reserve endpoint: exactly one endpoint at
// threshold 1, PRIVATE, committed under the pinned secret ref. A PUBLIC entry
// would put the restricted URL on-chain and let whoever publishes configuration
// point the numerator of the price wherever they liked.
func checkHexCash(g configpayload.Group) error {
	if len(g.Endpoints) != 1 {
		return errors.Errorf("hex_cash must have exactly one endpoint, got %d", len(g.Endpoints))
	}
	if g.Threshold != 1 {
		return errors.Errorf("hex_cash threshold must be 1, got %d", g.Threshold)
	}
	ep := g.Endpoints[0]
	if ep.Kind != configpayload.KindPrivate {
		return errors.New("hex_cash must be a PRIVATE endpoint (urlHash), not PUBLIC (url)")
	}
	if ep.SecretRef != protocol.HexCashURLRef {
		return errors.Errorf("hex_cash must name secret %q, got %q", protocol.HexCashURLRef, ep.SecretRef)
	}
	return nil
}

// checkEndpoint is one endpoint's rules: a PRIVATE endpoint carries only a
// commitment and names the ref that delivers its URL; a PUBLIC one carries only
// a URL of a safe shape; and no chain group may name a backing ref, PUBLIC or
// PRIVATE, or a payload author could have the backing set's credential
// substituted into a host of their choosing.
func checkEndpoint(group string, i int, isChain bool, ep configpayload.Endpoint) error {
	switch ep.Kind {
	case configpayload.KindPrivate:
		if ep.Url != "" || ep.UrlHash == ([32]byte{}) {
			return errors.Errorf("group %q endpoint %d: a PRIVATE endpoint carries only a non-zero urlHash", group, i+1)
		}
		if ep.SecretRef == "" {
			return errors.Errorf(
				"group %q endpoint %d: a PRIVATE endpoint must name the secret ref that delivers the URL off-chain",
				group, i+1)
		}
	case configpayload.KindPublic:
		if ep.Url == "" || ep.UrlHash != ([32]byte{}) {
			return errors.Errorf("group %q endpoint %d: a PUBLIC endpoint carries only a url", group, i+1)
		}
		if err := checkURL(ep.Url, ep.SecretRef); err != nil {
			return errors.Errorf("group %q endpoint %d: %s", group, i+1, err)
		}
	default:
		return errors.Errorf("group %q endpoint %d: unknown endpoint kind %d", group, i+1, ep.Kind)
	}

	if isChain && protocol.IsBackingRef(ep.SecretRef) {
		return errors.Errorf(
			"group %q endpoint %d: %q is a backing ref and may not be used on a chain group, "+
				"public or private (the backing refs are %s)",
			group, i+1, ep.SecretRef, strings.Join(protocol.BackingRefs(), ", "))
	}
	return nil
}

// checkURL mirrors the shape half of the machine's resolveURL: that the URL and
// secret ref agree, and that a credential could not move the request to another
// host. Whether the credential has arrived is SET_SECRET's business.
func checkURL(raw, secretRef string) error {
	hasPlaceholder := strings.Contains(raw, secretPlaceholder)
	if hasPlaceholder != (secretRef != "") {
		return errors.New("{secret} and secret must be used together")
	}
	if !strings.HasPrefix(raw, "https://") {
		return errors.New("url must use https")
	}
	if !hasPlaceholder {
		return nil
	}

	authorityEnd := strings.Index(raw[len("https://"):], "/")
	if authorityEnd < 0 {
		return errors.New("{secret} may not appear in the host")
	}
	if strings.Index(raw, secretPlaceholder) <= len("https://")+authorityEnd {
		return errors.New("{secret} may not appear in the scheme or host")
	}
	return nil
}

// summarise describes what is about to be published: group names, thresholds,
// endpoint kinds and credential refs. Never a URL or urlHash value beyond a
// short prefix — this reaches terminals and logs.
func summarise(groups []configpayload.Group) string {
	var b strings.Builder
	for _, g := range groups {
		name := configpayload.TagName(g.Group)
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(strconv.FormatUint(g.Threshold, 10))
		b.WriteString("-of-")
		b.WriteString(strconv.Itoa(len(g.Endpoints)))

		var kinds, refs []string
		for _, ep := range g.Endpoints {
			if ep.Kind == configpayload.KindPrivate {
				kinds = append(kinds, "private")
			} else {
				kinds = append(kinds, "public")
			}
			if ep.SecretRef != "" {
				refs = append(refs, ep.SecretRef)
			}
		}
		b.WriteString(" [")
		b.WriteString(strings.Join(kinds, ", "))
		b.WriteString("]")
		if len(refs) > 0 {
			b.WriteString(", credentials: ")
			b.WriteString(strings.Join(refs, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func sortedKeys(e map[string]groupConfig) []string {
	names := make([]string, 0, len(e))
	for name := range e {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
