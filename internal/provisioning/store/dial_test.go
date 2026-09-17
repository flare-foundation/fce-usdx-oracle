package store

import (
	"bytes"
	"context"
	"math/big"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"

	"fce-usdx-oracle/internal/config"

	"github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/common"
)

// ceiling stops the test server rather than the code under test: without the cap
// the client drains everything offered, so the handler needs its own end.
const ceiling = 64 << 20

// A provider answering with a result far larger than a single ABI word must be cut
// off at the transport. Asserted on what the server manages to write, because the
// defect is how much reaches the enclave's memory, not whether the call fails —
// uncapped it fails too, after buffering the lot.
func TestDialRPC_CapsAnOversizedResponse(t *testing.T) {
	var written atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		// A well-formed JSON-RPC reply whose result is never terminated.
		_, _ = w.Write([]byte(`{"jsonrpc":"2.0","id":1,"result":"0x`))
		chunk := bytes.Repeat([]byte("ab"), 32<<10)
		for written.Load() < ceiling {
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client, err := dialRPC(srv.URL)
	if err != nil {
		t.Fatalf("dialRPC: %v", err)
	}
	defer client.Close()

	call := ethereum.CallMsg{To: &common.Address{}, Data: []byte{0x18, 0x16, 0x0d, 0xdd}}
	if _, err := client.CallContract(context.Background(), call, big.NewInt(1)); err == nil {
		t.Fatal("expected the capped body to fail rather than buffer without bound")
	}

	if got, limit := written.Load(), int64(8*config.MaxRPCResponseBytes); got > limit {
		t.Errorf("provider pushed %d bytes into the enclave, want at most %d", got, limit)
	}
}

// The non-2xx path buffers the body separately, so the cap has to cover it too.
func TestDialRPC_CapsAnOversizedErrorBody(t *testing.T) {
	var written atomic.Int64

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		chunk := bytes.Repeat([]byte("x"), 32<<10)
		for written.Load() < ceiling {
			n, err := w.Write(chunk)
			written.Add(int64(n))
			if err != nil {
				return
			}
		}
	}))
	defer srv.Close()

	client, err := dialRPC(srv.URL)
	if err != nil {
		t.Fatalf("dialRPC: %v", err)
	}
	defer client.Close()

	call := ethereum.CallMsg{To: &common.Address{}, Data: []byte{0x18, 0x16, 0x0d, 0xdd}}
	if _, err := client.CallContract(context.Background(), call, big.NewInt(1)); err == nil {
		t.Fatal("expected an error from the 500 response")
	}

	if got, limit := written.Load(), int64(8*config.MaxRPCResponseBytes); got > limit {
		t.Errorf("provider pushed %d bytes into the enclave, want at most %d", got, limit)
	}
}
