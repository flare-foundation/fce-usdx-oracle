package main

import (
	"encoding/json"
	"sort"
	"strconv"
	"strings"

	"github.com/pkg/errors"
)

// The SET_ENDPOINTS payload schema, restated rather than imported so tools/ stays
// independent of any one implementation. Worth checking because a bad payload fails
// SILENTLY: expectedConfigHash is written, then every submitPrice reverts StaleConfig.
type endpoints map[string]chainEndpoints

type chainEndpoints struct {
	// How many providers must return identical results at the agreed block.
	Threshold int `json:"threshold"`

	Providers []provider `json:"providers"`
}

type provider struct {
	URL string `json:"url"`

	// Names the credential substituted for {secret} in URL. A name, never a value.
	Secret string `json:"secret,omitempty"`
}

// secretPlaceholder is the token the enclave replaces at dial time.
const secretPlaceholder = "{secret}"

// hexAPIKeyRef authenticates the pinned backing source by header, and may not be
// named as a provider secret: substituting it into a provider URL would deliver
// the backing credential to a host of the URL author's choosing.
const hexAPIKeyRef = "hex_api_key"

// pinnedChains must match config.Chains in the extension. A payload missing one
// undercounts supply, which OVERSTATES the price — so a missing chain is
// rejected rather than treated as "that chain is off".
var pinnedChains = []string{"flare", "ethereum", "songbird"}

// parseEndpoints decodes the payload the way the extension does, rejecting unknown
// fields: they mean publisher and build disagree about the schema.
func parseEndpoints(raw []byte) (endpoints, error) {
	var e endpoints

	dec := json.NewDecoder(strings.NewReader(string(raw)))
	dec.DisallowUnknownFields()
	if err := dec.Decode(&e); err != nil {
		return nil, errors.Errorf("payload is not a known configuration: %s", err)
	}
	return e, nil
}

// validateEndpoints mirrors runtimecfg.validate. Kept in the same order so the
// two can be diffed by eye.
func validateEndpoints(e endpoints) error {
	if len(e) == 0 {
		return errors.New("no chains configured")
	}

	for _, name := range sortedKeys(e) {
		ce := e[name]

		if !isPinnedChain(name) {
			return errors.Errorf("unknown chain %q (expected one of %s)",
				name, strings.Join(pinnedChains, ", "))
		}
		if len(ce.Providers) == 0 {
			return errors.Errorf("chain %q has no providers", name)
		}
		if ce.Threshold < 1 || ce.Threshold > len(ce.Providers) {
			return errors.Errorf("chain %q threshold %d is not between 1 and %d",
				name, ce.Threshold, len(ce.Providers))
		}
		for i, p := range ce.Providers {
			if p.Secret == hexAPIKeyRef {
				return errors.Errorf(
					"chain %q provider %d: %q authenticates the backing source and may not be used as a provider secret",
					name, i+1, hexAPIKeyRef)
			}
			if err := checkURL(p.URL, p.Secret); err != nil {
				return errors.Errorf("chain %q provider %d: %s", name, i+1, err)
			}
		}
	}

	for _, name := range pinnedChains {
		if _, ok := e[name]; !ok {
			return errors.Errorf("chain %q is missing — a missing chain undercounts supply, "+
				"which overstates the price", name)
		}
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

// summarise describes what is about to be published: chain names, thresholds and
// credential refs. Never a URL — this reaches terminals and logs.
func summarise(e endpoints) string {
	var b strings.Builder
	for _, name := range sortedKeys(e) {
		ce := e[name]
		refs := make([]string, 0, len(ce.Providers))
		for _, p := range ce.Providers {
			if p.Secret != "" {
				refs = append(refs, p.Secret)
			}
		}

		b.WriteString("  ")
		b.WriteString(name)
		b.WriteString(": ")
		b.WriteString(strconv.Itoa(ce.Threshold))
		b.WriteString("-of-")
		b.WriteString(strconv.Itoa(len(ce.Providers)))
		if len(refs) > 0 {
			b.WriteString(", credentials: ")
			b.WriteString(strings.Join(refs, ", "))
		}
		b.WriteString("\n")
	}
	return b.String()
}

func isPinnedChain(name string) bool {
	for _, c := range pinnedChains {
		if c == name {
			return true
		}
	}
	return false
}

func sortedKeys(e endpoints) []string {
	names := make([]string, 0, len(e))
	for name := range e {
		names = append(names, name)
	}
	sort.Strings(names)
	return names
}
