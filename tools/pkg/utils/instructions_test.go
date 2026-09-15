package utils

import (
	"bytes"
	"testing"

	"fce-usdx-oracle/tools/pkg/oraclepayload"

	"github.com/ethereum/go-ethereum/common"
)

// tag is a bytes32 group or role name, as the payload structs carry them.
func tag(name string) [32]byte {
	var out [32]byte
	copy(out[:], name)
	return out
}

// The calldata has to match the deployed sender's ABI. The binding is
// regenerated from the upstream contracts, so a signature change there must
// surface here, in the encoder, rather than as a revert at execution.
func TestPublicationCalldataMatchesSenderABI(t *testing.T) {
	parsed, err := senderABI()
	if err != nil {
		t.Fatal(err)
	}
	feedID := [21]byte{0x20, 'U', 'S', 'D', 'X'}
	claimBack := common.HexToAddress("0x000000000000000000000000000000000000dEaD")
	groups := []oraclepayload.Group{{Group: tag("flare"), Threshold: 1, Endpoints: []oraclepayload.Endpoint{
		{Kind: oraclepayload.KindPublic, Url: "https://flare.example/rpc"},
	}}}
	roles := []oraclepayload.Role{{Role: tag("backing"), Threshold: 1, Admins: []common.Address{
		common.HexToAddress("0x1111111111111111111111111111111111111111"),
	}}}

	cases := []struct {
		name string
		pub  func() (*Publication, error)
	}{
		{"setEndpoints", func() (*Publication, error) { return EncodeSetEndpoints(feedID, groups, claimBack, 7) }},
		{"setAdmins", func() (*Publication, error) { return EncodeSetAdmins(feedID, roles, claimBack, 7) }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			pub, err := tc.pub()
			if err != nil {
				t.Fatal(err)
			}
			method, ok := parsed.Methods[tc.name]
			if !ok {
				t.Fatalf("the sender ABI has no %s", tc.name)
			}
			if !bytes.Equal(pub.Calldata[:4], method.ID) {
				t.Fatalf("selector %x, want %x", pub.Calldata[:4], method.ID)
			}
			args, err := method.Inputs.Unpack(pub.Calldata[4:])
			if err != nil {
				t.Fatalf("calldata does not unpack against the ABI: %v", err)
			}
			if len(args) != len(method.Inputs) {
				t.Fatalf("%d arguments, the ABI declares %d", len(args), len(method.Inputs))
			}
			got, ok := args[0].([21]byte)
			if !ok || got != feedID {
				t.Fatalf("feed id %x, want %x", got, feedID)
			}
			claim, ok := args[len(args)-1].(common.Address)
			if !ok || claim != claimBack {
				t.Fatalf("claim-back %s, want %s", claim.Hex(), claimBack.Hex())
			}
			if pub.Version != 7 || pub.PayloadHash == (common.Hash{}) {
				t.Fatalf("version %d, payload hash %s", pub.Version, pub.PayloadHash.Hex())
			}
		})
	}
}

func TestPublicationRejectsZeroClaimBack(t *testing.T) {
	if _, err := EncodeSetEndpoints([21]byte{1}, nil, common.Address{}, 1); err == nil {
		t.Fatal("setEndpoints accepted a zero claim-back address; the sender reverts it")
	}
	if _, err := EncodeSetAdmins([21]byte{1}, nil, common.Address{}, 1); err == nil {
		t.Fatal("setAdmins accepted a zero claim-back address; the sender reverts it")
	}
}
