package store

import (
	"context"
	"net/http"

	"fce-usdx-oracle/internal/config"

	"github.com/ethereum/go-ethereum/ethclient"
	"github.com/ethereum/go-ethereum/rpc"
)

// boundedBody caps every response body an RPC provider returns. go-ethereum reads
// the success path straight into a JSON decoder and the non-2xx path into a byte
// buffer, neither bounded, so the cap has to sit under both — which is what
// wrapping the transport, rather than any one call site, achieves.
type boundedBody struct {
	base  http.RoundTripper
	limit int64
}

func (t boundedBody) RoundTrip(req *http.Request) (*http.Response, error) {
	resp, err := t.base.RoundTrip(req)
	if err != nil {
		return nil, err
	}
	// A nil ResponseWriter is the client-side use of MaxBytesReader: the
	// too-large callback it would otherwise invoke is server-only.
	resp.Body = http.MaxBytesReader(nil, resp.Body, t.limit)
	return resp, nil
}

// dialRPC opens one supply provider with its response bodies capped and its
// requests bounded in time. Every endpoint URL is https — resolveURL rejects
// anything else — so the HTTP transport is always the one in use and the cap
// always applies; a ws:// endpoint would bypass both.
//
// The context bounds nothing here: an HTTP rpc.Client connects lazily, and each
// call carries its own deadline from supply.ChainSource.ProviderTimeout. The
// client timeout repeats that bound so a future call path that forgets the
// per-call context is still not unbounded.
func dialRPC(url string) (*ethclient.Client, error) {
	client := &http.Client{
		Timeout: config.ProviderTimeout,
		Transport: boundedBody{
			base:  http.DefaultTransport,
			limit: config.MaxRPCResponseBytes,
		},
	}

	c, err := rpc.DialOptions(context.Background(), url, rpc.WithHTTPClient(client))
	if err != nil {
		return nil, err
	}
	return ethclient.NewClient(c), nil
}
