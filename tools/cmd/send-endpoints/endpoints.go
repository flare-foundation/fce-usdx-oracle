package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"fce-usdx-oracle/tools/pkg/oraclepayload"

	"github.com/ethereum/go-ethereum/common"
	"github.com/pkg/errors"
)

// The SET_ENDPOINTS payload schema, restated rather than imported so tools/ stays
// independent of any one implementation. Worth checking because a bad payload fails
// SILENTLY: expectedEndpointsHash is written, then every submitFeedUpdate reverts
// StaleEndpoints.
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

// hexAPIKeyRef authenticates the pinned backing source by header, and may not be
// named as a provider secret: substituting it into a provider URL would deliver
// the backing credential to a host of the URL author's choosing.
const hexAPIKeyRef = "hex_api_key"

// hexCashURLRef names the off-chain-provisioned URL for the PRIVATE hex_cash
// endpoint. Pinned so the role governing the cash URL is not the publisher's
// choice.
const hexCashURLRef = "hex_cash_url"

// knownGroups must match the tags the extension build pins (config.Chains plus
// hex_cash). A payload missing one undercounts supply, which OVERSTATES the price
// — so a missing group is rejected rather than treated as "that chain is off".
// An unknown group is likewise rejected: publisher and build disagreeing about
// the schema stops the machine rather than applying a subset.
var knownGroups = []string{"flare", "ethereum", "songbird", "hex_cash"}

// parseEndpoints decodes the operator's JSON into the typed group list, rejecting
// unknown fields: they mean publisher and build disagree about the schema.
func parseEndpoints(raw []byte) ([]oraclepayload.Group, error) {
	var e map[string]groupConfig
	if err := json.Unmarshal(raw, &e); err != nil {
		return nil, errors.Errorf("payload is not a known configuration: %s", err)
	}

	groups := make([]oraclepayload.Group, 0, len(e))
	for _, name := range sortedKeys(e) {
		group, err := buildGroup(name, e[name])
		if err != nil {
			return nil, err
		}
		groups = append(groups, group)
	}
	return groups, nil
}

// buildGroup turns one config entry into a typed EndpointGroup, validating the
// shape along the way.
func buildGroup(name string, gc groupConfig) (oraclepayload.Group, error) {
	tag, err := oraclepayload.Tag(name)
	if err != nil {
		return oraclepayload.Group{}, errors.Errorf("group %q: %s", name, err)
	}
	threshold, err := oraclepayload.Threshold(gc.Threshold)
	if err != nil {
		return oraclepayload.Group{}, errors.Errorf("group %q: %s", name, err)
	}

	endpoints := make([]oraclepayload.Endpoint, 0, len(gc.Endpoints))
	for i, ec := range gc.Endpoints {
		endpoint, err := buildEndpoint(name, i, ec)
		if err != nil {
			return oraclepayload.Group{}, err
		}
		endpoints = append(endpoints, endpoint)
	}

	return oraclepayload.Group{Group: tag, Threshold: threshold, Endpoints: endpoints}, nil
}

// buildEndpoint classifies one endpoint as PUBLIC or PRIVATE by which fields it
// carries, and validates it.
func buildEndpoint(group string, i int, ec endpointConfig) (oraclepayload.Endpoint, error) {
	hasURL := ec.URL != ""
	hasHash := ec.URLHash != ""

	switch {
	case hasURL && hasHash:
		return oraclepayload.Endpoint{}, errors.Errorf(
			"group %q endpoint %d: url and urlHash are mutually exclusive — pick PUBLIC (url) or PRIVATE (urlHash)",
			group, i+1)
	case hasURL:
		if err := checkURL(ec.URL, ec.Secret); err != nil {
			return oraclepayload.Endpoint{}, errors.Errorf("group %q endpoint %d: %s", group, i+1, err)
		}
		if ec.Secret == hexAPIKeyRef {
			return oraclepayload.Endpoint{}, errors.Errorf(
				"group %q endpoint %d: %q authenticates the backing source and may not be used as a provider secret",
				group, i+1, hexAPIKeyRef)
		}
		return oraclepayload.Endpoint{
			Kind:      oraclepayload.KindPublic,
			Url:       ec.URL,
			SecretRef: ec.Secret,
		}, nil
	case hasHash:
		hash := common.HexToHash(ec.URLHash)
		if hash == (common.Hash{}) {
			return oraclepayload.Endpoint{}, errors.Errorf(
				"group %q endpoint %d: urlHash %q is not a non-zero 32-byte hash", group, i+1, ec.URLHash)
		}
		if ec.Secret == "" {
			return oraclepayload.Endpoint{}, errors.Errorf(
				"group %q endpoint %d: a PRIVATE endpoint must name the secret ref that delivers the URL off-chain",
				group, i+1)
		}
		if strings.Contains(ec.Secret, secretPlaceholder) {
			return oraclepayload.Endpoint{}, errors.Errorf(
				"group %q endpoint %d: secretRef may not contain %s", group, i+1, secretPlaceholder)
		}
		return oraclepayload.Endpoint{
			Kind:      oraclepayload.KindPrivate,
			UrlHash:   hash,
			SecretRef: ec.Secret,
		}, nil
	default:
		return oraclepayload.Endpoint{}, errors.Errorf(
			"group %q endpoint %d: needs either url (PUBLIC) or urlHash (PRIVATE)", group, i+1)
	}
}

// validateEndpoints checks the group set against the rules the extension build
// pins. Kept independent of the extension so a divergence surfaces here rather
// than as a silently-applied subset.
func validateEndpoints(groups []oraclepayload.Group) error {
	if len(groups) == 0 {
		return errors.New("no groups configured")
	}

	seen := make(map[string]bool, len(groups))
	for _, g := range groups {
		name := oraclepayload.TagName(g.Group)
		if seen[name] {
			return errors.Errorf("group %q appears more than once", name)
		}
		seen[name] = true

		if !isKnownGroup(name) {
			return errors.Errorf("unknown group %q (expected one of %s)",
				name, strings.Join(knownGroups, ", "))
		}
		if len(g.Endpoints) == 0 {
			return errors.Errorf("group %q has no endpoints", name)
		}
		if g.Threshold < 1 || int(g.Threshold) > len(g.Endpoints) {
			return errors.Errorf("group %q threshold %d is not between 1 and %d",
				name, g.Threshold, len(g.Endpoints))
		}

		if name == "hex_cash" {
			if err := checkHexCash(g); err != nil {
				return err
			}
		}
	}

	for _, name := range knownGroups {
		if !seen[name] {
			return errors.Errorf("group %q is missing — a missing chain undercounts supply, "+
				"which overstates the price", name)
		}
	}
	return nil
}

// checkHexCash pins the restricted reserve endpoint: exactly one endpoint, PRIVATE,
// committed under the pinned secret ref. A PUBLIC entry would put the restricted
// URL on-chain and let whoever publishes configuration point the numerator of the
// price wherever they liked.
func checkHexCash(g oraclepayload.Group) error {
	if len(g.Endpoints) != 1 {
		return errors.Errorf("hex_cash must have exactly one endpoint, got %d", len(g.Endpoints))
	}
	if g.Threshold != 1 {
		return errors.Errorf("hex_cash threshold must be 1, got %d", g.Threshold)
	}
	ep := g.Endpoints[0]
	if ep.Kind != oraclepayload.KindPrivate {
		return errors.New("hex_cash must be a PRIVATE endpoint (urlHash), not PUBLIC (url)")
	}
	if ep.SecretRef != hexCashURLRef {
		return errors.Errorf("hex_cash must name secretRef %q, got %q", hexCashURLRef, ep.SecretRef)
	}
	return nil
}

// checkURL mirrors the shape half of runtimecfg.resolveURL: that the URL and
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
func summarise(groups []oraclepayload.Group) string {
	var b strings.Builder
	for _, g := range groups {
		name := oraclepayload.TagName(g.Group)
		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(strconv.FormatUint(g.Threshold, 10))
		b.WriteString("-of-")
		b.WriteString(strconv.Itoa(len(g.Endpoints)))

		var kinds, refs []string
		for _, ep := range g.Endpoints {
			if ep.Kind == oraclepayload.KindPrivate {
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

func isKnownGroup(name string) bool {
	for _, c := range knownGroups {
		if c == name {
			return true
		}
	}
	return false
}

func sortedKeys(e map[string]groupConfig) []string {
	names := make([]string, 0, len(e))
	for name := range e {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
