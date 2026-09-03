// Package utils drives the on-chain half of the TEE oracle extension: it
// requests feed updates and publishes configuration through
// TeeOracleInstructionsSender.
//
// Nothing here deploys anything. The sender and the feed stores come from
// flare-smart-contracts-v2's own deploy script; this repo only ever calls them,
// through the upstream interfaces compiled via contracts/.
package utils

import (
	"context"
	"math/big"

	"fce-usdx-oracle/tools/pkg/contracts/teeoraclesender"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/oraclepayload"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

// The operation identifiers, PINNED rather than read off the chain.
//
// They used to be public getters on the sender, so the fee lookup could not
// drift from what was actually dispatched. Upstream made them file-level
// `bytes32 constant`s in ITeeOracleInstructionsSender — shared by every deployed
// instance — so there is nothing left to read and this copy is now load-bearing.
// It must match internal/config; a mismatch prices the wrong row and the
// instruction is rejected for an underpaid fee.
const (
	OpTypeTeeOracle       = "TEE_ORACLE"
	OpCommandGetFeed      = "GET_FEED"
	OpCommandSetEndpoints = "SET_ENDPOINTS"
	OpCommandSetAdmins    = "SET_ADMINS"
)

func b32(s string) [32]byte {
	var out [32]byte
	copy(out[:], s)
	return out
}

func senderABI() (*abi.ABI, error) {
	parsed, err := teeoraclesender.IITeeOracleInstructionsSenderMetaData.GetAbi()
	if err != nil {
		return nil, errors.Errorf("parsing sender ABI: %s", err)
	}
	return parsed, nil
}

// --- fees ---

// FeedUpdateFee is the registry's current fee for one GET_FEED instruction.
// Read, never hard-coded: it is governance-settable, and the default applies
// until the TEE_ORACLE rows are set.
func FeedUpdateFee(s *support.Support, teeIDs int) (*big.Int, error) {
	return operationFee(s, OpCommandGetFeed, teeIDs)
}

// EndpointsFee is the same lookup for SET_ENDPOINTS. One publication dispatches
// one instruction PER TARGET MACHINE, so the fee scales with the fleet — a
// single-machine fee would underpay a fleet-wide publish and revert.
func EndpointsFee(s *support.Support, teeIDs int) (*big.Int, error) {
	return operationFee(s, OpCommandSetEndpoints, teeIDs)
}

// AdminsFee is the same, for SET_ADMINS.
func AdminsFee(s *support.Support, teeIDs int) (*big.Int, error) {
	return operationFee(s, OpCommandSetAdmins, teeIDs)
}

func operationFee(s *support.Support, command string, teeIDs int) (*big.Int, error) {
	if teeIDs < 1 {
		return nil, errors.New("at least one target machine is required")
	}

	opts := &bind.CallOpts{Context: context.Background()}
	fee, err := s.TeeOperationFees.GetOperationFee(opts, b32(OpTypeTeeOracle), b32(command))
	if err != nil {
		return nil, errors.Errorf("reading operation fee: %s", err)
	}
	if fee == nil || fee.Sign() == 0 {
		// No row set for this pair yet; the diamond charges the default. Upstream's
		// deploy script leaves the TEE_ORACLE rows to a governance follow-up, so
		// this is the normal state on a fresh deployment, not an error.
		fee, err = s.TeeOperationFees.GetDefaultFee(opts)
		if err != nil {
			return nil, errors.Errorf("reading default fee: %s", err)
		}
	}
	return new(big.Int).Mul(fee, big.NewInt(int64(teeIDs))), nil
}

// --- requesting a feed update ---

// RequestFeedUpdate asks the given machines for a fresh observation of one feed
// and returns the dispatched instruction id.
//
// It is permissionless — the fee is what bounds spam — but every target must
// already have endpoints AND admins published for the feed, or the call reverts
// TeeIdNotConfigured. Use IsTeeIdConfigured to check before spending gas.
func RequestFeedUpdate(
	s *support.Support, senderAddress common.Address, feedID [21]byte, teeIDs []common.Address,
) (common.Hash, common.Hash, error) {
	if len(teeIDs) == 0 {
		return common.Hash{}, common.Hash{}, errors.New("at least one target machine is required")
	}

	sender, err := teeoraclesender.NewIITeeOracleInstructionsSender(senderAddress, s.ChainClient)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("binding sender: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("creating transactor: %s", err)
	}
	fee, err := FeedUpdateFee(s, len(teeIDs))
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	opts.Value = fee

	tx, err := sender.RequestFeedUpdate(opts, feedID, teeIDs)
	if err != nil {
		return common.Hash{}, common.Hash{}, decorateRevert(s, senderAddress, fee, err, func() []byte {
			parsed, perr := senderABI()
			if perr != nil {
				return nil
			}
			data, perr := parsed.Pack("requestFeedUpdate", feedID, teeIDs)
			if perr != nil {
				return nil
			}
			return data
		})
	}

	receipt, err := waitMined(s, tx)
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}

	instructionID, err := feedUpdateInstructionID(s, senderAddress, receipt)
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	return instructionID, receipt.TxHash, nil
}

// feedUpdateInstructionID pulls the id out of the receipt. The sender's own
// FeedUpdateRequested carries it, and so does the diamond's TeeInstructionsSent;
// the former is preferred because it also proves the feed it was requested for.
func feedUpdateInstructionID(
	s *support.Support, senderAddress common.Address, receipt *types.Receipt,
) (common.Hash, error) {
	filterer, err := teeoraclesender.NewIITeeOracleInstructionsSenderFilterer(senderAddress, s.ChainClient)
	if err != nil {
		return common.Hash{}, errors.Errorf("binding sender filterer: %s", err)
	}

	for _, l := range receipt.Logs {
		if l == nil || l.Address != senderAddress {
			continue
		}
		if requested, perr := filterer.ParseFeedUpdateRequested(*l); perr == nil {
			return requested.InstructionId, nil
		}
	}
	return common.Hash{}, errors.Errorf(
		"no FeedUpdateRequested event in receipt (%d logs) for tx %s",
		len(receipt.Logs), receipt.TxHash.Hex())
}

// --- publishing configuration ---

// Publication is one setEndpoints or setAdmins call, ready to send or to hand a
// governance executor.
type Publication struct {
	// Calldata is the exact encoded call. Under the Flare governance timelock the
	// executor supplies these bytes verbatim and the recorded hash must match
	// byte for byte, so this is the artefact to store — not a description of it.
	Calldata []byte

	// Fee is operationFee × len(TeeIDs). In production mode the EXECUTOR attaches
	// it to executeGovernanceCall; recording the call rejects value.
	Fee *big.Int

	// PayloadHash is what the sender will record as latestEndpointsHash /
	// latestAdminsHash, and what every later feed update must carry. Predicted
	// here so a publisher can verify the machine picked up this exact payload.
	PayloadHash common.Hash

	Version uint64
	FeedID  [21]byte
	TeeIDs  []common.Address
}

// EncodeSetEndpoints builds the governance call that publishes an endpoint
// configuration for one feed to a set of machines.
//
// version is the version the sender WILL assign — endpointsVersion(feedId) + 1
// — and is needed only to predict PayloadHash. The contract assigns the real
// one; if another publication for this feed lands first, the prediction is stale
// and the hash will not match.
func EncodeSetEndpoints(
	feedID [21]byte, teeIDs []common.Address, groups []oraclepayload.Group,
	claimBack common.Address, version uint64,
) (*Publication, error) {
	if len(teeIDs) == 0 {
		return nil, errors.New("at least one target machine is required")
	}

	hash, err := oraclepayload.EndpointsHash(version, feedID, groups)
	if err != nil {
		return nil, err
	}

	parsed, err := senderABI()
	if err != nil {
		return nil, err
	}
	calldata, err := parsed.Pack("setEndpoints", feedID, teeIDs, senderGroups(groups), claimBack)
	if err != nil {
		return nil, errors.Errorf("packing setEndpoints: %s", err)
	}

	return &Publication{
		Calldata: calldata, PayloadHash: hash,
		Version: version, FeedID: feedID, TeeIDs: teeIDs,
	}, nil
}

// EncodeSetAdmins is the same for the role-tagged admin signer sets.
func EncodeSetAdmins(
	feedID [21]byte, teeIDs []common.Address, roles []oraclepayload.Role,
	claimBack common.Address, version uint64,
) (*Publication, error) {
	if len(teeIDs) == 0 {
		return nil, errors.New("at least one target machine is required")
	}

	hash, err := oraclepayload.AdminsHash(version, feedID, roles)
	if err != nil {
		return nil, err
	}

	parsed, err := senderABI()
	if err != nil {
		return nil, err
	}
	calldata, err := parsed.Pack("setAdmins", feedID, teeIDs, senderRoles(roles), claimBack)
	if err != nil {
		return nil, errors.Errorf("packing setAdmins: %s", err)
	}

	return &Publication{
		Calldata: calldata, PayloadHash: hash,
		Version: version, FeedID: feedID, TeeIDs: teeIDs,
	}, nil
}

// Send submits a publication directly, for a caller that IS governance.
//
// That is only the case before switchToProductionMode — upstream's deploy script
// switches the proxies over, so on any real deployment this reverts and the
// calldata has to go through the timelock instead. Kept for local and test
// deployments, where the deployer holds governance and the round trip is noise.
func (p *Publication) Send(s *support.Support, senderAddress common.Address) (common.Hash, error) {
	fee := p.Fee
	if fee == nil {
		return common.Hash{}, errors.New("publication has no fee; call ResolveFee first")
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Hash{}, errors.Errorf("creating transactor: %s", err)
	}
	opts.Value = fee

	contract := bind.NewBoundContract(senderAddress, abi.ABI{}, s.ChainClient, s.ChainClient, s.ChainClient)
	tx, err := contract.RawTransact(opts, p.Calldata)
	if err != nil {
		return common.Hash{}, decorateRevert(s, senderAddress, fee, err, func() []byte { return p.Calldata })
	}

	receipt, err := waitMined(s, tx)
	if err != nil {
		return common.Hash{}, err
	}
	return receipt.TxHash, nil
}

// ResolveFee reads the current operation fee for a publication and fills it in.
func (p *Publication) ResolveFee(s *support.Support, command string) error {
	fee, err := operationFee(s, command, len(p.TeeIDs))
	if err != nil {
		return err
	}
	p.Fee = fee
	return nil
}

// Handoff is the export form of a Publication for a governance executor.
//
// Once the sender is in production mode, setEndpoints / setAdmins are
// governance-only, and in this deployment Flare governance runs the timelock —
// so this repo does not execute the call at all. It hands over exactly what the
// executor needs: target + calldata to pass to executeGovernanceCall, with the
// fee attached as msg.value AT EXECUTION (recording the timelocked call rejects
// value). The calldata is the artefact — the recorded hash must match it byte
// for byte, so hand the file over as written, never a re-typed description.
type Handoff struct {
	Command     string   `json:"command"`
	Target      string   `json:"target"`
	Calldata    string   `json:"calldata"`
	ValueWei    string   `json:"valueWei"`
	PayloadHash string   `json:"payloadHash"`
	Version     uint64   `json:"predictedVersion"`
	FeedID      string   `json:"feedId"`
	TeeIDs      []string `json:"teeIds"`
	Note        string   `json:"note"`
}

// Handoff renders the publication for the executor. ResolveFee must have run.
func (p *Publication) Handoff(target common.Address, command string) (*Handoff, error) {
	if p.Fee == nil {
		return nil, errors.New("publication has no fee; call ResolveFee first")
	}
	teeIDs := make([]string, len(p.TeeIDs))
	for i, id := range p.TeeIDs {
		teeIDs[i] = id.Hex()
	}
	return &Handoff{
		Command:     command,
		Target:      target.Hex(),
		Calldata:    hexutil.Encode(p.Calldata),
		ValueWei:    p.Fee.String(),
		PayloadHash: p.PayloadHash.Hex(),
		Version:     p.Version,
		FeedID:      hexutil.Encode(p.FeedID[:]),
		TeeIDs:      teeIDs,
		Note: "Execute through the Flare governance timelock: executeGovernanceCall with " +
			"this target and calldata, attaching valueWei as msg.value at execution time " +
			"(recording the call rejects value). After execution, the sender's " +
			"latestEndpointsHash/latestAdminsHash for the feed must equal payloadHash.",
	}, nil
}

// senderGroups and senderRoles retype the payload structs for the generated
// binding. abigen emits its own copy of each nested struct into every package
// that mentions one, so the identical EndpointGroup exists in both
// teeoraclestructs (where oraclepayload takes it from) and teeoraclesender,
// as distinct named types Go will not convert between in a slice.
func senderGroups(in []oraclepayload.Group) []teeoraclesender.ITeeOracleInstructionsSenderEndpointGroup {
	out := make([]teeoraclesender.ITeeOracleInstructionsSenderEndpointGroup, 0, len(in))
	for _, g := range in {
		endpoints := make([]teeoraclesender.ITeeOracleInstructionsSenderEndpoint, 0, len(g.Endpoints))
		for _, ep := range g.Endpoints {
			endpoints = append(endpoints, teeoraclesender.ITeeOracleInstructionsSenderEndpoint{
				Kind: ep.Kind, Url: ep.Url, UrlHash: ep.UrlHash, SecretRef: ep.SecretRef,
			})
		}
		out = append(out, teeoraclesender.ITeeOracleInstructionsSenderEndpointGroup{
			Group: g.Group, Threshold: g.Threshold, Endpoints: endpoints,
		})
	}
	return out
}

func senderRoles(in []oraclepayload.Role) []teeoraclesender.ITeeOracleInstructionsSenderAdminRole {
	out := make([]teeoraclesender.ITeeOracleInstructionsSenderAdminRole, 0, len(in))
	for _, r := range in {
		out = append(out, teeoraclesender.ITeeOracleInstructionsSenderAdminRole{
			Role: r.Role, Threshold: r.Threshold, Admins: r.Admins,
		})
	}
	return out
}

// --- reading published state ---

// Sender binds the instructions sender for the read-only views.
func Sender(s *support.Support, senderAddress common.Address) (*teeoraclesender.IITeeOracleInstructionsSenderCaller, error) {
	caller, err := teeoraclesender.NewIITeeOracleInstructionsSenderCaller(senderAddress, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding sender: %s", err)
	}
	return caller, nil
}

// --- shared plumbing ---

func waitMined(s *support.Support, tx *types.Transaction) (*types.Receipt, error) {
	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return nil, errors.Errorf("waiting for transaction: %s", err)
	}
	if receipt.Status != 1 {
		return nil, errors.Errorf("transaction %s failed with status %d", tx.Hash().Hex(), receipt.Status)
	}
	return receipt, nil
}

// decorateRevert turns a bare "execution reverted" into the typed error the
// contract actually raised. The sender's custom errors (TeeIdNotConfigured,
// DuplicateGroup, InvalidThreshold, ...) are the whole diagnostic value of a
// failed publish, and geth drops them unless the call is simulated.
func decorateRevert(
	s *support.Support, to common.Address, value *big.Int, cause error, calldata func() []byte,
) error {
	reason := fccutils.DecodeRevertReason(cause)
	if reason == "" {
		if data := calldata(); data != nil {
			from := crypto.PubkeyToAddress(s.Prv.PublicKey)
			reason = fccutils.SimulateAndDecodeRevert(s.ChainClient, from, to, value, data)
		}
	}
	if reason != "" {
		return errors.Errorf("%s (revert reason: %s)", cause, reason)
	}
	return errors.Errorf("%s", cause)
}
