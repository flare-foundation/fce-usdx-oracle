package support

import (
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"fce-usdx-oracle/tools/pkg/configs"
)

// chainStub answers eth_chainId, which is all building a Support asks of a node.
func chainStub(t *testing.T, chainID uint64) *httptest.Server {
	t.Helper()
	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		_ = json.NewDecoder(r.Body).Decode(&req)
		if req.Method != "eth_chainId" {
			t.Errorf("unexpected rpc %s", req.Method)
		}
		_, _ = fmt.Fprintf(w, `{"jsonrpc":"2.0","id":%s,"result":"0x%x"}`, req.ID, chainID)
	}))
}

// Verifying a machine or listing a fleet must work for someone who holds no
// key at all — an independent admin, or a coordinator who is not the
// extension's owner — on a live chain where there is no dev key to fall back
// to. The signing bootstrap refuses in exactly that environment, which is
// what makes the read-only one necessary.
func TestReadOnlySupportNeedsNoKeyOnALiveChain(t *testing.T) {
	t.Setenv("EXTENSION_OWNER_KEY", "")
	t.Setenv("DEPLOYMENT_PRIVATE_KEY", "")
	chain := configs.Chain
	t.Cleanup(func() { configs.Chain = chain })
	configs.Chain = "coston"

	node := chainStub(t, 16)
	defer node.Close()

	addresses := filepath.Join(t.TempDir(), "addresses.json")
	if err := os.WriteFile(addresses, []byte(`{
		"FlareSystemsManager": "0x0000000000000000000000000000000000000001",
		"Fdc2Hub":             "0x0000000000000000000000000000000000000002",
		"FlareTeeManager":     "0x0000000000000000000000000000000000000003"
	}`), 0o600); err != nil {
		t.Fatal(err)
	}

	s, err := ReadOnlySupport(addresses, node.URL)
	if err != nil {
		t.Fatalf("a read-only command was refused for want of a key: %v", err)
	}
	if s.Prv != nil {
		t.Error("the read-only support holds a signing key")
	}
	if s.ChainID.Uint64() != 16 {
		t.Errorf("chain id = %s, want the 16 the node reported", s.ChainID)
	}

	if _, err := DefaultSupport(addresses, node.URL); err == nil {
		t.Error("the signing bootstrap accepted a live chain with no key configured")
	}
}
