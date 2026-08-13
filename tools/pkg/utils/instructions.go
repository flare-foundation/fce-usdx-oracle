package utils

import (
	"context"
	"math/big"
	"strings"
	"time"

	"fce-usdx-oracle/tools/pkg/contracts/usdxoracle"
	"fce-usdx-oracle/tools/pkg/fccutils"
	"fce-usdx-oracle/tools/pkg/support"

	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/pkg/errors"
)

func DeployInstructionSender(s *support.Support) (common.Address, *usdxoracle.UsdxOracleInstructionSender, error) {
	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("failed to create transactor: %s", err)
	}

	// Both registry args are the FlareTeeManager diamond proxy: the diamond
	// routes ExtensionManager and MachineManager calls to the right facets.
	address, tx, contract, err := usdxoracle.DeployUsdxOracleInstructionSender(
		opts, s.ChainClient, s.Addresses.FlareTeeManager, s.Addresses.FlareTeeManager,
	)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("failed to deploy contract: %s", err)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	receipt, err := bind.WaitMined(ctx, s.ChainClient, tx)
	if err != nil {
		return common.Address{}, nil, errors.Errorf("deployment tx not mined within 2 minutes (tx: %s): %s", tx.Hash().Hex(), err)
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		return common.Address{}, nil, errors.New("contract deployment failed")
	}

	return address, contract, nil
}

func SetExtensionId(s *support.Support, instructionSenderAddress common.Address) error {
	sender, err := usdxoracle.NewUsdxOracleInstructionSender(instructionSenderAddress, s.ChainClient)
	if err != nil {
		return errors.Errorf("failed to bind contract: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return errors.Errorf("failed to create transactor: %s", err)
	}

	tx, err := sender.SetExtensionId(opts)
	if err != nil {
		reason := fccutils.DecodeRevertReason(err)
		if reason == "" {
			parsed, _ := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
			if parsed != nil {
				callData, packErr := parsed.Pack("setExtensionId")
				if packErr == nil {
					from := crypto.PubkeyToAddress(s.Prv.PublicKey)
					reason = fccutils.SimulateAndDecodeRevert(
						s.ChainClient, from, instructionSenderAddress, nil, callData,
					)
				}
			}
		}
		if reason != "" {
			return errors.Errorf("failed to call setExtensionId: %s (revert reason: %s)", err, reason)
		}
		return errors.Errorf("failed to call setExtensionId: %s", err)
	}

	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return errors.Errorf("failed waiting for transaction: %s", err)
	}

	if receipt.Status != types.ReceiptStatusSuccessful {
		parsed, _ := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
		if parsed != nil {
			callData, packErr := parsed.Pack("setExtensionId")
			if packErr == nil {
				from := crypto.PubkeyToAddress(s.Prv.PublicKey)
				reason := fccutils.SimulateAndDecodeRevert(
					s.ChainClient, from, instructionSenderAddress, nil, callData,
				)
				if reason != "" {
					return errors.Errorf("setExtensionId transaction failed (revert reason: %s)", reason)
				}
			}
		}
		return errors.New("setExtensionId transaction failed")
	}

	return nil
}

// priceUpdateFee returns the registry's current fee for one GET_PRICE
// instruction. Read, never hard-coded: the fee is governance-settable and a
// stale constant would revert every request with nothing in this repo changed
// to explain it. The op type and command come from the deployed contract's own
// constants, so the lookup cannot drift from the instruction actually sent.
func priceUpdateFee(s *support.Support, senderAddress common.Address) (*big.Int, error) {
	return instructionFee(s, senderAddress, opGetPrice)
}

// endpointsFee is the same lookup for SET_ENDPOINTS, charged under a different
// op type and command.
func endpointsFee(s *support.Support, senderAddress common.Address) (*big.Int, error) {
	return instructionFee(s, senderAddress, opSetEndpoints)
}

// GetPriceRequest is everything needed to raise a GET_PRICE action off-chain,
// read from the deployed sender rather than restated here.
//
// The direct channel does not touch the sender contract at all, so nothing would
// stop a hand-written constant here from drifting away from the one
// requestPriceUpdate() actually emits — and the failure would be a rejected
// request envelope with no hint that the two channels disagree. Reading them
// from the same source makes that impossible.
type GetPriceRequest struct {
	OPType    common.Hash
	OPCommand common.Hash
	Message   []byte
}

// ReadGetPriceRequest fetches the op pair and request envelope for GET_PRICE.
func ReadGetPriceRequest(s *support.Support, senderAddress common.Address) (GetPriceRequest, error) {
	caller, err := usdxoracle.NewUsdxOracleInstructionSenderCaller(senderAddress, s.ChainClient)
	if err != nil {
		return GetPriceRequest{}, errors.Errorf("binding instruction sender: %s", err)
	}

	opts := &bind.CallOpts{Context: context.Background()}
	opType, opCommand, err := opGetPrice(caller, opts)
	if err != nil {
		return GetPriceRequest{}, err
	}

	message, err := caller.GETPRICEREQUESTV1(opts)
	if err != nil {
		return GetPriceRequest{}, errors.Errorf("reading GET_PRICE_REQUEST_V1: %s", err)
	}
	if len(message) == 0 {
		// The registry rejects an empty message with MessageEmpty(), and the
		// extension requires exactly the pinned envelope. Either way an empty
		// one cannot succeed, so stop here rather than send it.
		return GetPriceRequest{}, errors.New("the sender reports an empty GET_PRICE request envelope")
	}

	return GetPriceRequest{
		OPType:    common.BytesToHash(opType[:]),
		OPCommand: common.BytesToHash(opCommand[:]),
		Message:   message,
	}, nil
}

// opSelector reads one (opType, opCommand) pair off the deployed sender.
type opSelector func(*usdxoracle.UsdxOracleInstructionSenderCaller, *bind.CallOpts) ([32]byte, [32]byte, error)

func opGetPrice(c *usdxoracle.UsdxOracleInstructionSenderCaller, o *bind.CallOpts) ([32]byte, [32]byte, error) {
	t, err := c.OPTYPEUSDXORACLE(o)
	if err != nil {
		return [32]byte{}, [32]byte{}, errors.Errorf("reading OP_TYPE_USDX_ORACLE: %s", err)
	}
	cmd, err := c.OPCOMMANDGETPRICE(o)
	if err != nil {
		return [32]byte{}, [32]byte{}, errors.Errorf("reading OP_COMMAND_GET_PRICE: %s", err)
	}
	return t, cmd, nil
}

func opSetEndpoints(c *usdxoracle.UsdxOracleInstructionSenderCaller, o *bind.CallOpts) ([32]byte, [32]byte, error) {
	t, err := c.OPTYPEUSDXCONFIG(o)
	if err != nil {
		return [32]byte{}, [32]byte{}, errors.Errorf("reading OP_TYPE_USDX_CONFIG: %s", err)
	}
	cmd, err := c.OPCOMMANDSETENDPOINTS(o)
	if err != nil {
		return [32]byte{}, [32]byte{}, errors.Errorf("reading OP_COMMAND_SET_ENDPOINTS: %s", err)
	}
	return t, cmd, nil
}

func instructionFee(s *support.Support, senderAddress common.Address, sel opSelector) (*big.Int, error) {
	caller, err := usdxoracle.NewUsdxOracleInstructionSenderCaller(senderAddress, s.ChainClient)
	if err != nil {
		return nil, errors.Errorf("binding instruction sender: %s", err)
	}

	opts := &bind.CallOpts{Context: context.Background()}
	opType, opCommand, err := sel(caller, opts)
	if err != nil {
		return nil, err
	}

	fee, err := s.TeeOperationFees.GetOperationFee(opts, opType, opCommand)
	if err != nil {
		return nil, errors.Errorf("reading operation fee: %s", err)
	}
	if fee != nil && fee.Sign() > 0 {
		return fee, nil
	}

	def, err := s.TeeOperationFees.GetDefaultFee(opts)
	if err != nil {
		return nil, errors.Errorf("reading default fee: %s", err)
	}
	return def, nil
}

// RequestPriceUpdate calls requestPriceUpdate() and returns the instruction ID
// and transaction hash. It takes no arguments: the observation needs nothing
// from the caller, and acceptance is settled by the store's registered-machine
// check rather than by the payload.
func RequestPriceUpdate(s *support.Support, instructionSenderAddress common.Address) (common.Hash, common.Hash, error) {
	sender, err := usdxoracle.NewUsdxOracleInstructionSender(instructionSenderAddress, s.ChainClient)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to bind contract: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to create transactor: %s", err)
	}
	fee, err := priceUpdateFee(s, instructionSenderAddress)
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	opts.Value = fee

	// simulateRevert re-runs the call with eth_call to recover a revert reason,
	// which the raw send/receipt path does not surface.
	simulateRevert := func() string {
		parsed, abiErr := usdxoracle.UsdxOracleInstructionSenderMetaData.GetAbi()
		if abiErr != nil || parsed == nil {
			return ""
		}
		callData, packErr := parsed.Pack("requestPriceUpdate")
		if packErr != nil {
			return ""
		}
		from := crypto.PubkeyToAddress(s.Prv.PublicKey)
		return fccutils.SimulateAndDecodeRevert(
			s.ChainClient, from, instructionSenderAddress,
			fee, callData,
		)
	}

	tx, err := sender.RequestPriceUpdate(opts)
	if err != nil {
		reason := fccutils.DecodeRevertReason(err)
		if reason == "" {
			reason = simulateRevert()
		}
		if reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s (revert reason: %s)", err, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s", err)
	}

	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed waiting for transaction: %s", err)
	}

	if receipt.Status != 1 {
		if reason := simulateRevert(); reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status %d (revert reason: %s)", receipt.Status, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status: %d", receipt.Status)
	}

	// Read the id off our own PriceUpdateRequested event rather than the
	// registry's, which would couple every caller to the registry's event ABI.
	// Scanned, not indexed by position: the receipt also holds the registry's
	// logs and the emit order can change.
	for _, l := range receipt.Logs {
		if l == nil {
			continue
		}
		requested, parseErr := sender.ParsePriceUpdateRequested(*l)
		if parseErr == nil {
			return requested.InstructionId, receipt.TxHash, nil
		}
	}

	return common.Hash{}, common.Hash{}, errors.Errorf(
		"no PriceUpdateRequested event in receipt (%d logs) for tx %s", len(receipt.Logs), receipt.TxHash.Hex())
}

// endpointsSetTopic is keccak256("EndpointsSet(address,bytes32)").
var endpointsSetTopic = crypto.Keccak256Hash([]byte("EndpointsSet(address,bytes32)"))

// setEndpointsABI is the minimal ABI of setEndpoints: the generated usdxoracle
// binding predates the provisioning surface and has no SetEndpoints method.
const setEndpointsABI = `[{"name":"setEndpoints","type":"function","stateMutability":"payable","inputs":[{"name":"teeId","type":"address"},{"name":"config","type":"bytes"}],"outputs":[]}]`

// SetEndpoints publishes an endpoint configuration through the instruction
// sender. The config bytes go into calldata and are publicly readable, so
// credentials must never be part of them.
func SetEndpoints(s *support.Support, instructionSenderAddress, teeID common.Address, config []byte) (common.Hash, common.Hash, error) {
	parsed, err := abi.JSON(strings.NewReader(setEndpointsABI))
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to parse setEndpoints ABI: %s", err)
	}

	opts, err := bind.NewKeyedTransactorWithChainID(s.Prv, s.ChainID)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to create transactor: %s", err)
	}
	fee, err := endpointsFee(s, instructionSenderAddress)
	if err != nil {
		return common.Hash{}, common.Hash{}, err
	}
	opts.Value = fee

	simulateRevert := func() string {
		callData, packErr := parsed.Pack("setEndpoints", teeID, config)
		if packErr != nil {
			return ""
		}
		from := crypto.PubkeyToAddress(s.Prv.PublicKey)
		return fccutils.SimulateAndDecodeRevert(
			s.ChainClient, from, instructionSenderAddress,
			fee, callData,
		)
	}

	sender := bind.NewBoundContract(instructionSenderAddress, parsed, s.ChainClient, s.ChainClient, s.ChainClient)
	tx, err := sender.Transact(opts, "setEndpoints", teeID, config)
	if err != nil {
		reason := fccutils.DecodeRevertReason(err)
		if reason == "" {
			reason = simulateRevert()
		}
		if reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s (revert reason: %s)", err, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("failed to send instruction: %s", err)
	}

	receipt, err := bind.WaitMined(context.Background(), s.ChainClient, tx)
	if err != nil {
		return common.Hash{}, common.Hash{}, errors.Errorf("failed waiting for transaction: %s", err)
	}
	if receipt.Status != 1 {
		if reason := simulateRevert(); reason != "" {
			return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status %d (revert reason: %s)", receipt.Status, reason)
		}
		return common.Hash{}, common.Hash{}, errors.Errorf("transaction failed with status: %d", receipt.Status)
	}

	// EndpointsSet proves the exact bytes landed on-chain: its configHash topic
	// must be the keccak256 of the configuration that was sent.
	configHash := crypto.Keccak256Hash(config)
	teeIDTopic := common.BytesToHash(teeID.Bytes())
	var (
		instructionID common.Hash
		endpointsSet  bool
	)
	for _, l := range receipt.Logs {
		if l == nil {
			continue
		}
		if len(l.Topics) == 3 && l.Topics[0] == endpointsSetTopic &&
			l.Topics[1] == teeIDTopic && l.Topics[2] == configHash {
			endpointsSet = true
			continue
		}
		if instructionSent, parseErr := s.TeeVerification.ParseTeeInstructionsSent(*l); parseErr == nil {
			instructionID = instructionSent.InstructionId
		}
	}
	if !endpointsSet {
		return common.Hash{}, common.Hash{}, errors.Errorf(
			"no EndpointsSet event for teeId %s with configHash %s in receipt for tx %s",
			teeID.Hex(), configHash.Hex(), receipt.TxHash.Hex())
	}
	if instructionID == (common.Hash{}) {
		return common.Hash{}, common.Hash{}, errors.Errorf(
			"no TeeInstructionsSent event in receipt (%d logs) for tx %s", len(receipt.Logs), receipt.TxHash.Hex())
	}

	return instructionID, receipt.TxHash, nil
}
