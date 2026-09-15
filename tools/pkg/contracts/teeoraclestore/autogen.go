// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package teeoraclestore

import (
	"errors"
	"math/big"
	"strings"

	ethereum "github.com/ethereum/go-ethereum"
	"github.com/ethereum/go-ethereum/accounts/abi"
	"github.com/ethereum/go-ethereum/accounts/abi/bind"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/event"
)

// Reference imports to suppress errors if they are not otherwise used.
var (
	_ = errors.New
	_ = big.NewInt
	_ = strings.NewReader
	_ = ethereum.NotFound
	_ = bind.Bind
	_ = common.Big1
	_ = types.BloomLookup
	_ = event.NewSubscription
	_ = abi.ConvertType
)

// ITeeOracleFeedStoreFeedAggregation is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleFeedStoreFeedAggregation struct {
	Median            *big.Int
	MinValue          *big.Int
	MaxValue          *big.Int
	Decimals          int8
	Spread            *big.Int
	AllowedDeviation  *big.Int
	OutlierTeeIds     []common.Address
	OutlierDeviations []*big.Int
}

// ITeeOracleFeedStoreFeedUpdate is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleFeedStoreFeedUpdate struct {
	ExtensionId   *big.Int
	FeedId        [21]byte
	Value         int32
	Decimals      int8
	ObservedAt    uint64
	EndpointsHash [32]byte
	AdminsHash    [32]byte
}

// ITeeOracleFeedStoreSignedFeedUpdate is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleFeedStoreSignedFeedUpdate struct {
	FeedUpdate ITeeOracleFeedStoreFeedUpdate
	Signature  Signature
}

// ITeeOracleFeedStoreSubmissionPolicy is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleFeedStoreSubmissionPolicy struct {
	RequiredSignatures uint8
	MaxSpreadBIPS      uint16
	MaxSpreadAbsolute  uint64
}

// Signature is an auto generated low-level Go binding around an user-defined struct.
type Signature struct {
	V uint8
	R [32]byte
	S [32]byte
}

// IITeeOracleFeedStoreMetaData contains all meta data concerning the IITeeOracleFeedStore contract.
var IITeeOracleFeedStoreMetaData = &bind.MetaData{
	ABI: "[{\"type\":\"function\",\"name\":\"extensionId\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint256\",\"internalType\":\"uint256\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"feeDestination\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"address\",\"internalType\":\"address\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getSubmissionPolicy\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleFeedStore.SubmissionPolicy\",\"components\":[{\"name\":\"requiredSignatures\",\"type\":\"uint8\",\"internalType\":\"uint8\"},{\"name\":\"maxSpreadBIPS\",\"type\":\"uint16\",\"internalType\":\"uint16\"},{\"name\":\"maxSpreadAbsolute\",\"type\":\"uint64\",\"internalType\":\"uint64\"}]}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"instructionsSender\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"address\",\"internalType\":\"contractITeeOracleInstructionsSender\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"maxSpreadAbsolute\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"maxSpreadBIPS\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint16\",\"internalType\":\"uint16\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"observedAt\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"requiredSignatures\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint8\",\"internalType\":\"uint8\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"setFeeDestination\",\"inputs\":[{\"name\":\"_feeDestination\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"setSubmissionPolicy\",\"inputs\":[{\"name\":\"_submissionPolicy\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleFeedStore.SubmissionPolicy\",\"components\":[{\"name\":\"requiredSignatures\",\"type\":\"uint8\",\"internalType\":\"uint8\"},{\"name\":\"maxSpreadBIPS\",\"type\":\"uint16\",\"internalType\":\"uint16\"},{\"name\":\"maxSpreadAbsolute\",\"type\":\"uint64\",\"internalType\":\"uint64\"}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"submitFeedUpdates\",\"inputs\":[{\"name\":\"_signedFeedUpdates\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleFeedStore.SignedFeedUpdate[]\",\"components\":[{\"name\":\"feedUpdate\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleFeedStore.FeedUpdate\",\"components\":[{\"name\":\"extensionId\",\"type\":\"uint256\",\"internalType\":\"uint256\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"value\",\"type\":\"int32\",\"internalType\":\"int32\"},{\"name\":\"decimals\",\"type\":\"int8\",\"internalType\":\"int8\"},{\"name\":\"observedAt\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpointsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"adminsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}]},{\"name\":\"signature\",\"type\":\"tuple\",\"internalType\":\"structSignature\",\"components\":[{\"name\":\"v\",\"type\":\"uint8\",\"internalType\":\"uint8\"},{\"name\":\"r\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"s\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}]}]}],\"outputs\":[{\"name\":\"_feedAggregation\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleFeedStore.FeedAggregation\",\"components\":[{\"name\":\"median\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"minValue\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"maxValue\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"decimals\",\"type\":\"int8\",\"internalType\":\"int8\"},{\"name\":\"spread\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"allowedDeviation\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"outlierTeeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"},{\"name\":\"outlierDeviations\",\"type\":\"int256[]\",\"internalType\":\"int256[]\"}]}],\"stateMutability\":\"nonpayable\"},{\"type\":\"event\",\"name\":\"FeeDestinationSet\",\"inputs\":[{\"name\":\"feeDestination\",\"type\":\"address\",\"indexed\":false,\"internalType\":\"address\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"FeedOutliers\",\"inputs\":[{\"name\":\"observedAt\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"median\",\"type\":\"int256\",\"indexed\":false,\"internalType\":\"int256\"},{\"name\":\"decimals\",\"type\":\"int8\",\"indexed\":false,\"internalType\":\"int8\"},{\"name\":\"teeIds\",\"type\":\"address[]\",\"indexed\":false,\"internalType\":\"address[]\"},{\"name\":\"deviations\",\"type\":\"int256[]\",\"indexed\":false,\"internalType\":\"int256[]\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"FeedStoreInitialised\",\"inputs\":[{\"name\":\"instructionsSender\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"},{\"name\":\"extensionId\",\"type\":\"uint256\",\"indexed\":true,\"internalType\":\"uint256\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"FeedUpdated\",\"inputs\":[{\"name\":\"value\",\"type\":\"int32\",\"indexed\":true,\"internalType\":\"int32\"},{\"name\":\"decimals\",\"type\":\"int8\",\"indexed\":false,\"internalType\":\"int8\"},{\"name\":\"observedAt\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"teeIds\",\"type\":\"address[]\",\"indexed\":false,\"internalType\":\"address[]\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"SubmissionPolicySet\",\"inputs\":[{\"name\":\"requiredSignatures\",\"type\":\"uint8\",\"indexed\":false,\"internalType\":\"uint8\"},{\"name\":\"maxSpreadBIPS\",\"type\":\"uint16\",\"indexed\":false,\"internalType\":\"uint16\"},{\"name\":\"maxSpreadAbsolute\",\"type\":\"uint64\",\"indexed\":false,\"internalType\":\"uint64\"}],\"anonymous\":false},{\"type\":\"error\",\"name\":\"DecimalsSpreadTooBig\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"DuplicateTeeId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"FeeTooLow\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"FeeTransferFailed\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidFeedCategory\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidSubmissionPolicy\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoAdminsPublished\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoEndpointsPublished\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoValuePublished\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NotEnoughSignatures\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NotNewer\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ObservedAtMismatch\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"SpreadTooBig\",\"inputs\":[{\"name\":\"lowerNeighbour\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"upperNeighbour\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"median\",\"type\":\"int256\",\"internalType\":\"int256\"},{\"name\":\"decimals\",\"type\":\"int8\",\"internalType\":\"int8\"}]},{\"type\":\"error\",\"name\":\"StaleAdmins\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"StaleEndpoints\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"TooFarAhead\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"TooManySignatures\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ValueOutOfRange\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"WrongExtensionId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"WrongFeedId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroAddress\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroExtensionId\",\"inputs\":[]}]",
}

// IITeeOracleFeedStoreABI is the input ABI used to generate the binding from.
// Deprecated: Use IITeeOracleFeedStoreMetaData.ABI instead.
var IITeeOracleFeedStoreABI = IITeeOracleFeedStoreMetaData.ABI

// IITeeOracleFeedStore is an auto generated Go binding around an Ethereum contract.
type IITeeOracleFeedStore struct {
	IITeeOracleFeedStoreCaller     // Read-only binding to the contract
	IITeeOracleFeedStoreTransactor // Write-only binding to the contract
	IITeeOracleFeedStoreFilterer   // Log filterer for contract events
}

// IITeeOracleFeedStoreCaller is an auto generated read-only Go binding around an Ethereum contract.
type IITeeOracleFeedStoreCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleFeedStoreTransactor is an auto generated write-only Go binding around an Ethereum contract.
type IITeeOracleFeedStoreTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleFeedStoreFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type IITeeOracleFeedStoreFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleFeedStoreSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type IITeeOracleFeedStoreSession struct {
	Contract     *IITeeOracleFeedStore // Generic contract binding to set the session for
	CallOpts     bind.CallOpts         // Call options to use throughout this session
	TransactOpts bind.TransactOpts     // Transaction auth options to use throughout this session
}

// IITeeOracleFeedStoreCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type IITeeOracleFeedStoreCallerSession struct {
	Contract *IITeeOracleFeedStoreCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts               // Call options to use throughout this session
}

// IITeeOracleFeedStoreTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type IITeeOracleFeedStoreTransactorSession struct {
	Contract     *IITeeOracleFeedStoreTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts               // Transaction auth options to use throughout this session
}

// IITeeOracleFeedStoreRaw is an auto generated low-level Go binding around an Ethereum contract.
type IITeeOracleFeedStoreRaw struct {
	Contract *IITeeOracleFeedStore // Generic contract binding to access the raw methods on
}

// IITeeOracleFeedStoreCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type IITeeOracleFeedStoreCallerRaw struct {
	Contract *IITeeOracleFeedStoreCaller // Generic read-only contract binding to access the raw methods on
}

// IITeeOracleFeedStoreTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type IITeeOracleFeedStoreTransactorRaw struct {
	Contract *IITeeOracleFeedStoreTransactor // Generic write-only contract binding to access the raw methods on
}

// NewIITeeOracleFeedStore creates a new instance of IITeeOracleFeedStore, bound to a specific deployed contract.
func NewIITeeOracleFeedStore(address common.Address, backend bind.ContractBackend) (*IITeeOracleFeedStore, error) {
	contract, err := bindIITeeOracleFeedStore(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStore{IITeeOracleFeedStoreCaller: IITeeOracleFeedStoreCaller{contract: contract}, IITeeOracleFeedStoreTransactor: IITeeOracleFeedStoreTransactor{contract: contract}, IITeeOracleFeedStoreFilterer: IITeeOracleFeedStoreFilterer{contract: contract}}, nil
}

// NewIITeeOracleFeedStoreCaller creates a new read-only instance of IITeeOracleFeedStore, bound to a specific deployed contract.
func NewIITeeOracleFeedStoreCaller(address common.Address, caller bind.ContractCaller) (*IITeeOracleFeedStoreCaller, error) {
	contract, err := bindIITeeOracleFeedStore(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreCaller{contract: contract}, nil
}

// NewIITeeOracleFeedStoreTransactor creates a new write-only instance of IITeeOracleFeedStore, bound to a specific deployed contract.
func NewIITeeOracleFeedStoreTransactor(address common.Address, transactor bind.ContractTransactor) (*IITeeOracleFeedStoreTransactor, error) {
	contract, err := bindIITeeOracleFeedStore(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreTransactor{contract: contract}, nil
}

// NewIITeeOracleFeedStoreFilterer creates a new log filterer instance of IITeeOracleFeedStore, bound to a specific deployed contract.
func NewIITeeOracleFeedStoreFilterer(address common.Address, filterer bind.ContractFilterer) (*IITeeOracleFeedStoreFilterer, error) {
	contract, err := bindIITeeOracleFeedStore(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreFilterer{contract: contract}, nil
}

// bindIITeeOracleFeedStore binds a generic wrapper to an already deployed contract.
func bindIITeeOracleFeedStore(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := IITeeOracleFeedStoreMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IITeeOracleFeedStore.Contract.IITeeOracleFeedStoreCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.IITeeOracleFeedStoreTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.IITeeOracleFeedStoreTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IITeeOracleFeedStore.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.contract.Transact(opts, method, params...)
}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) ExtensionId(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "extensionId")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) ExtensionId() (*big.Int, error) {
	return _IITeeOracleFeedStore.Contract.ExtensionId(&_IITeeOracleFeedStore.CallOpts)
}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) ExtensionId() (*big.Int, error) {
	return _IITeeOracleFeedStore.Contract.ExtensionId(&_IITeeOracleFeedStore.CallOpts)
}

// FeeDestination is a free data retrieval call binding the contract method 0xde53fcd5.
//
// Solidity: function feeDestination() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) FeeDestination(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "feeDestination")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// FeeDestination is a free data retrieval call binding the contract method 0xde53fcd5.
//
// Solidity: function feeDestination() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) FeeDestination() (common.Address, error) {
	return _IITeeOracleFeedStore.Contract.FeeDestination(&_IITeeOracleFeedStore.CallOpts)
}

// FeeDestination is a free data retrieval call binding the contract method 0xde53fcd5.
//
// Solidity: function feeDestination() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) FeeDestination() (common.Address, error) {
	return _IITeeOracleFeedStore.Contract.FeeDestination(&_IITeeOracleFeedStore.CallOpts)
}

// GetSubmissionPolicy is a free data retrieval call binding the contract method 0xb7fd2ede.
//
// Solidity: function getSubmissionPolicy() view returns((uint8,uint16,uint64))
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) GetSubmissionPolicy(opts *bind.CallOpts) (ITeeOracleFeedStoreSubmissionPolicy, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "getSubmissionPolicy")

	if err != nil {
		return *new(ITeeOracleFeedStoreSubmissionPolicy), err
	}

	out0 := *abi.ConvertType(out[0], new(ITeeOracleFeedStoreSubmissionPolicy)).(*ITeeOracleFeedStoreSubmissionPolicy)

	return out0, err

}

// GetSubmissionPolicy is a free data retrieval call binding the contract method 0xb7fd2ede.
//
// Solidity: function getSubmissionPolicy() view returns((uint8,uint16,uint64))
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) GetSubmissionPolicy() (ITeeOracleFeedStoreSubmissionPolicy, error) {
	return _IITeeOracleFeedStore.Contract.GetSubmissionPolicy(&_IITeeOracleFeedStore.CallOpts)
}

// GetSubmissionPolicy is a free data retrieval call binding the contract method 0xb7fd2ede.
//
// Solidity: function getSubmissionPolicy() view returns((uint8,uint16,uint64))
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) GetSubmissionPolicy() (ITeeOracleFeedStoreSubmissionPolicy, error) {
	return _IITeeOracleFeedStore.Contract.GetSubmissionPolicy(&_IITeeOracleFeedStore.CallOpts)
}

// InstructionsSender is a free data retrieval call binding the contract method 0xef2fe0a4.
//
// Solidity: function instructionsSender() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) InstructionsSender(opts *bind.CallOpts) (common.Address, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "instructionsSender")

	if err != nil {
		return *new(common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new(common.Address)).(*common.Address)

	return out0, err

}

// InstructionsSender is a free data retrieval call binding the contract method 0xef2fe0a4.
//
// Solidity: function instructionsSender() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) InstructionsSender() (common.Address, error) {
	return _IITeeOracleFeedStore.Contract.InstructionsSender(&_IITeeOracleFeedStore.CallOpts)
}

// InstructionsSender is a free data retrieval call binding the contract method 0xef2fe0a4.
//
// Solidity: function instructionsSender() view returns(address)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) InstructionsSender() (common.Address, error) {
	return _IITeeOracleFeedStore.Contract.InstructionsSender(&_IITeeOracleFeedStore.CallOpts)
}

// MaxSpreadAbsolute is a free data retrieval call binding the contract method 0x068afac0.
//
// Solidity: function maxSpreadAbsolute() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) MaxSpreadAbsolute(opts *bind.CallOpts) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "maxSpreadAbsolute")

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// MaxSpreadAbsolute is a free data retrieval call binding the contract method 0x068afac0.
//
// Solidity: function maxSpreadAbsolute() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) MaxSpreadAbsolute() (uint64, error) {
	return _IITeeOracleFeedStore.Contract.MaxSpreadAbsolute(&_IITeeOracleFeedStore.CallOpts)
}

// MaxSpreadAbsolute is a free data retrieval call binding the contract method 0x068afac0.
//
// Solidity: function maxSpreadAbsolute() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) MaxSpreadAbsolute() (uint64, error) {
	return _IITeeOracleFeedStore.Contract.MaxSpreadAbsolute(&_IITeeOracleFeedStore.CallOpts)
}

// MaxSpreadBIPS is a free data retrieval call binding the contract method 0xb44cd1ba.
//
// Solidity: function maxSpreadBIPS() view returns(uint16)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) MaxSpreadBIPS(opts *bind.CallOpts) (uint16, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "maxSpreadBIPS")

	if err != nil {
		return *new(uint16), err
	}

	out0 := *abi.ConvertType(out[0], new(uint16)).(*uint16)

	return out0, err

}

// MaxSpreadBIPS is a free data retrieval call binding the contract method 0xb44cd1ba.
//
// Solidity: function maxSpreadBIPS() view returns(uint16)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) MaxSpreadBIPS() (uint16, error) {
	return _IITeeOracleFeedStore.Contract.MaxSpreadBIPS(&_IITeeOracleFeedStore.CallOpts)
}

// MaxSpreadBIPS is a free data retrieval call binding the contract method 0xb44cd1ba.
//
// Solidity: function maxSpreadBIPS() view returns(uint16)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) MaxSpreadBIPS() (uint16, error) {
	return _IITeeOracleFeedStore.Contract.MaxSpreadBIPS(&_IITeeOracleFeedStore.CallOpts)
}

// ObservedAt is a free data retrieval call binding the contract method 0x9be7dadb.
//
// Solidity: function observedAt() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) ObservedAt(opts *bind.CallOpts) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "observedAt")

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// ObservedAt is a free data retrieval call binding the contract method 0x9be7dadb.
//
// Solidity: function observedAt() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) ObservedAt() (uint64, error) {
	return _IITeeOracleFeedStore.Contract.ObservedAt(&_IITeeOracleFeedStore.CallOpts)
}

// ObservedAt is a free data retrieval call binding the contract method 0x9be7dadb.
//
// Solidity: function observedAt() view returns(uint64)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) ObservedAt() (uint64, error) {
	return _IITeeOracleFeedStore.Contract.ObservedAt(&_IITeeOracleFeedStore.CallOpts)
}

// RequiredSignatures is a free data retrieval call binding the contract method 0x8d068043.
//
// Solidity: function requiredSignatures() view returns(uint8)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCaller) RequiredSignatures(opts *bind.CallOpts) (uint8, error) {
	var out []interface{}
	err := _IITeeOracleFeedStore.contract.Call(opts, &out, "requiredSignatures")

	if err != nil {
		return *new(uint8), err
	}

	out0 := *abi.ConvertType(out[0], new(uint8)).(*uint8)

	return out0, err

}

// RequiredSignatures is a free data retrieval call binding the contract method 0x8d068043.
//
// Solidity: function requiredSignatures() view returns(uint8)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) RequiredSignatures() (uint8, error) {
	return _IITeeOracleFeedStore.Contract.RequiredSignatures(&_IITeeOracleFeedStore.CallOpts)
}

// RequiredSignatures is a free data retrieval call binding the contract method 0x8d068043.
//
// Solidity: function requiredSignatures() view returns(uint8)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreCallerSession) RequiredSignatures() (uint8, error) {
	return _IITeeOracleFeedStore.Contract.RequiredSignatures(&_IITeeOracleFeedStore.CallOpts)
}

// SetFeeDestination is a paid mutator transaction binding the contract method 0xfbe53234.
//
// Solidity: function setFeeDestination(address _feeDestination) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactor) SetFeeDestination(opts *bind.TransactOpts, _feeDestination common.Address) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.contract.Transact(opts, "setFeeDestination", _feeDestination)
}

// SetFeeDestination is a paid mutator transaction binding the contract method 0xfbe53234.
//
// Solidity: function setFeeDestination(address _feeDestination) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) SetFeeDestination(_feeDestination common.Address) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SetFeeDestination(&_IITeeOracleFeedStore.TransactOpts, _feeDestination)
}

// SetFeeDestination is a paid mutator transaction binding the contract method 0xfbe53234.
//
// Solidity: function setFeeDestination(address _feeDestination) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactorSession) SetFeeDestination(_feeDestination common.Address) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SetFeeDestination(&_IITeeOracleFeedStore.TransactOpts, _feeDestination)
}

// SetSubmissionPolicy is a paid mutator transaction binding the contract method 0x6add936d.
//
// Solidity: function setSubmissionPolicy((uint8,uint16,uint64) _submissionPolicy) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactor) SetSubmissionPolicy(opts *bind.TransactOpts, _submissionPolicy ITeeOracleFeedStoreSubmissionPolicy) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.contract.Transact(opts, "setSubmissionPolicy", _submissionPolicy)
}

// SetSubmissionPolicy is a paid mutator transaction binding the contract method 0x6add936d.
//
// Solidity: function setSubmissionPolicy((uint8,uint16,uint64) _submissionPolicy) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) SetSubmissionPolicy(_submissionPolicy ITeeOracleFeedStoreSubmissionPolicy) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SetSubmissionPolicy(&_IITeeOracleFeedStore.TransactOpts, _submissionPolicy)
}

// SetSubmissionPolicy is a paid mutator transaction binding the contract method 0x6add936d.
//
// Solidity: function setSubmissionPolicy((uint8,uint16,uint64) _submissionPolicy) returns()
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactorSession) SetSubmissionPolicy(_submissionPolicy ITeeOracleFeedStoreSubmissionPolicy) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SetSubmissionPolicy(&_IITeeOracleFeedStore.TransactOpts, _submissionPolicy)
}

// SubmitFeedUpdates is a paid mutator transaction binding the contract method 0x6c2252e1.
//
// Solidity: function submitFeedUpdates(((uint256,bytes21,int32,int8,uint64,bytes32,bytes32),(uint8,bytes32,bytes32))[] _signedFeedUpdates) returns((int256,int256,int256,int8,int256,int256,address[],int256[]) _feedAggregation)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactor) SubmitFeedUpdates(opts *bind.TransactOpts, _signedFeedUpdates []ITeeOracleFeedStoreSignedFeedUpdate) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.contract.Transact(opts, "submitFeedUpdates", _signedFeedUpdates)
}

// SubmitFeedUpdates is a paid mutator transaction binding the contract method 0x6c2252e1.
//
// Solidity: function submitFeedUpdates(((uint256,bytes21,int32,int8,uint64,bytes32,bytes32),(uint8,bytes32,bytes32))[] _signedFeedUpdates) returns((int256,int256,int256,int8,int256,int256,address[],int256[]) _feedAggregation)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreSession) SubmitFeedUpdates(_signedFeedUpdates []ITeeOracleFeedStoreSignedFeedUpdate) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SubmitFeedUpdates(&_IITeeOracleFeedStore.TransactOpts, _signedFeedUpdates)
}

// SubmitFeedUpdates is a paid mutator transaction binding the contract method 0x6c2252e1.
//
// Solidity: function submitFeedUpdates(((uint256,bytes21,int32,int8,uint64,bytes32,bytes32),(uint8,bytes32,bytes32))[] _signedFeedUpdates) returns((int256,int256,int256,int8,int256,int256,address[],int256[]) _feedAggregation)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreTransactorSession) SubmitFeedUpdates(_signedFeedUpdates []ITeeOracleFeedStoreSignedFeedUpdate) (*types.Transaction, error) {
	return _IITeeOracleFeedStore.Contract.SubmitFeedUpdates(&_IITeeOracleFeedStore.TransactOpts, _signedFeedUpdates)
}

// IITeeOracleFeedStoreFeeDestinationSetIterator is returned from FilterFeeDestinationSet and is used to iterate over the raw logs and unpacked data for FeeDestinationSet events raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeeDestinationSetIterator struct {
	Event *IITeeOracleFeedStoreFeeDestinationSet // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *IITeeOracleFeedStoreFeeDestinationSetIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleFeedStoreFeeDestinationSet)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(IITeeOracleFeedStoreFeeDestinationSet)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *IITeeOracleFeedStoreFeeDestinationSetIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleFeedStoreFeeDestinationSetIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleFeedStoreFeeDestinationSet represents a FeeDestinationSet event raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeeDestinationSet struct {
	FeeDestination common.Address
	Raw            types.Log // Blockchain specific contextual infos
}

// FilterFeeDestinationSet is a free log retrieval operation binding the contract event 0x9ffcc7872e184f6db78bf00d137d6a7d52aa1546937286c01382063602bbcad6.
//
// Solidity: event FeeDestinationSet(address feeDestination)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) FilterFeeDestinationSet(opts *bind.FilterOpts) (*IITeeOracleFeedStoreFeeDestinationSetIterator, error) {

	logs, sub, err := _IITeeOracleFeedStore.contract.FilterLogs(opts, "FeeDestinationSet")
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreFeeDestinationSetIterator{contract: _IITeeOracleFeedStore.contract, event: "FeeDestinationSet", logs: logs, sub: sub}, nil
}

// WatchFeeDestinationSet is a free log subscription operation binding the contract event 0x9ffcc7872e184f6db78bf00d137d6a7d52aa1546937286c01382063602bbcad6.
//
// Solidity: event FeeDestinationSet(address feeDestination)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) WatchFeeDestinationSet(opts *bind.WatchOpts, sink chan<- *IITeeOracleFeedStoreFeeDestinationSet) (event.Subscription, error) {

	logs, sub, err := _IITeeOracleFeedStore.contract.WatchLogs(opts, "FeeDestinationSet")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleFeedStoreFeeDestinationSet)
				if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeeDestinationSet", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseFeeDestinationSet is a log parse operation binding the contract event 0x9ffcc7872e184f6db78bf00d137d6a7d52aa1546937286c01382063602bbcad6.
//
// Solidity: event FeeDestinationSet(address feeDestination)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) ParseFeeDestinationSet(log types.Log) (*IITeeOracleFeedStoreFeeDestinationSet, error) {
	event := new(IITeeOracleFeedStoreFeeDestinationSet)
	if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeeDestinationSet", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleFeedStoreFeedOutliersIterator is returned from FilterFeedOutliers and is used to iterate over the raw logs and unpacked data for FeedOutliers events raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedOutliersIterator struct {
	Event *IITeeOracleFeedStoreFeedOutliers // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *IITeeOracleFeedStoreFeedOutliersIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleFeedStoreFeedOutliers)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(IITeeOracleFeedStoreFeedOutliers)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *IITeeOracleFeedStoreFeedOutliersIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleFeedStoreFeedOutliersIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleFeedStoreFeedOutliers represents a FeedOutliers event raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedOutliers struct {
	ObservedAt uint64
	Median     *big.Int
	Decimals   int8
	TeeIds     []common.Address
	Deviations []*big.Int
	Raw        types.Log // Blockchain specific contextual infos
}

// FilterFeedOutliers is a free log retrieval operation binding the contract event 0x89a976ab5d1d92659f899c66d35a3185f9bb1dab8b1b75eefba93edef9cdfdbe.
//
// Solidity: event FeedOutliers(uint64 indexed observedAt, int256 median, int8 decimals, address[] teeIds, int256[] deviations)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) FilterFeedOutliers(opts *bind.FilterOpts, observedAt []uint64) (*IITeeOracleFeedStoreFeedOutliersIterator, error) {

	var observedAtRule []interface{}
	for _, observedAtItem := range observedAt {
		observedAtRule = append(observedAtRule, observedAtItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.FilterLogs(opts, "FeedOutliers", observedAtRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreFeedOutliersIterator{contract: _IITeeOracleFeedStore.contract, event: "FeedOutliers", logs: logs, sub: sub}, nil
}

// WatchFeedOutliers is a free log subscription operation binding the contract event 0x89a976ab5d1d92659f899c66d35a3185f9bb1dab8b1b75eefba93edef9cdfdbe.
//
// Solidity: event FeedOutliers(uint64 indexed observedAt, int256 median, int8 decimals, address[] teeIds, int256[] deviations)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) WatchFeedOutliers(opts *bind.WatchOpts, sink chan<- *IITeeOracleFeedStoreFeedOutliers, observedAt []uint64) (event.Subscription, error) {

	var observedAtRule []interface{}
	for _, observedAtItem := range observedAt {
		observedAtRule = append(observedAtRule, observedAtItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.WatchLogs(opts, "FeedOutliers", observedAtRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleFeedStoreFeedOutliers)
				if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedOutliers", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseFeedOutliers is a log parse operation binding the contract event 0x89a976ab5d1d92659f899c66d35a3185f9bb1dab8b1b75eefba93edef9cdfdbe.
//
// Solidity: event FeedOutliers(uint64 indexed observedAt, int256 median, int8 decimals, address[] teeIds, int256[] deviations)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) ParseFeedOutliers(log types.Log) (*IITeeOracleFeedStoreFeedOutliers, error) {
	event := new(IITeeOracleFeedStoreFeedOutliers)
	if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedOutliers", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleFeedStoreFeedStoreInitialisedIterator is returned from FilterFeedStoreInitialised and is used to iterate over the raw logs and unpacked data for FeedStoreInitialised events raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedStoreInitialisedIterator struct {
	Event *IITeeOracleFeedStoreFeedStoreInitialised // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *IITeeOracleFeedStoreFeedStoreInitialisedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleFeedStoreFeedStoreInitialised)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(IITeeOracleFeedStoreFeedStoreInitialised)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *IITeeOracleFeedStoreFeedStoreInitialisedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleFeedStoreFeedStoreInitialisedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleFeedStoreFeedStoreInitialised represents a FeedStoreInitialised event raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedStoreInitialised struct {
	InstructionsSender common.Address
	ExtensionId        *big.Int
	FeedId             [21]byte
	Raw                types.Log // Blockchain specific contextual infos
}

// FilterFeedStoreInitialised is a free log retrieval operation binding the contract event 0x206cc7b50c6343342c801cd5bedcfd05841c8968dd89745abbf2066a86509117.
//
// Solidity: event FeedStoreInitialised(address indexed instructionsSender, uint256 indexed extensionId, bytes21 indexed feedId)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) FilterFeedStoreInitialised(opts *bind.FilterOpts, instructionsSender []common.Address, extensionId []*big.Int, feedId [][21]byte) (*IITeeOracleFeedStoreFeedStoreInitialisedIterator, error) {

	var instructionsSenderRule []interface{}
	for _, instructionsSenderItem := range instructionsSender {
		instructionsSenderRule = append(instructionsSenderRule, instructionsSenderItem)
	}
	var extensionIdRule []interface{}
	for _, extensionIdItem := range extensionId {
		extensionIdRule = append(extensionIdRule, extensionIdItem)
	}
	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.FilterLogs(opts, "FeedStoreInitialised", instructionsSenderRule, extensionIdRule, feedIdRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreFeedStoreInitialisedIterator{contract: _IITeeOracleFeedStore.contract, event: "FeedStoreInitialised", logs: logs, sub: sub}, nil
}

// WatchFeedStoreInitialised is a free log subscription operation binding the contract event 0x206cc7b50c6343342c801cd5bedcfd05841c8968dd89745abbf2066a86509117.
//
// Solidity: event FeedStoreInitialised(address indexed instructionsSender, uint256 indexed extensionId, bytes21 indexed feedId)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) WatchFeedStoreInitialised(opts *bind.WatchOpts, sink chan<- *IITeeOracleFeedStoreFeedStoreInitialised, instructionsSender []common.Address, extensionId []*big.Int, feedId [][21]byte) (event.Subscription, error) {

	var instructionsSenderRule []interface{}
	for _, instructionsSenderItem := range instructionsSender {
		instructionsSenderRule = append(instructionsSenderRule, instructionsSenderItem)
	}
	var extensionIdRule []interface{}
	for _, extensionIdItem := range extensionId {
		extensionIdRule = append(extensionIdRule, extensionIdItem)
	}
	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.WatchLogs(opts, "FeedStoreInitialised", instructionsSenderRule, extensionIdRule, feedIdRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleFeedStoreFeedStoreInitialised)
				if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedStoreInitialised", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseFeedStoreInitialised is a log parse operation binding the contract event 0x206cc7b50c6343342c801cd5bedcfd05841c8968dd89745abbf2066a86509117.
//
// Solidity: event FeedStoreInitialised(address indexed instructionsSender, uint256 indexed extensionId, bytes21 indexed feedId)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) ParseFeedStoreInitialised(log types.Log) (*IITeeOracleFeedStoreFeedStoreInitialised, error) {
	event := new(IITeeOracleFeedStoreFeedStoreInitialised)
	if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedStoreInitialised", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleFeedStoreFeedUpdatedIterator is returned from FilterFeedUpdated and is used to iterate over the raw logs and unpacked data for FeedUpdated events raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedUpdatedIterator struct {
	Event *IITeeOracleFeedStoreFeedUpdated // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *IITeeOracleFeedStoreFeedUpdatedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleFeedStoreFeedUpdated)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(IITeeOracleFeedStoreFeedUpdated)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *IITeeOracleFeedStoreFeedUpdatedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleFeedStoreFeedUpdatedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleFeedStoreFeedUpdated represents a FeedUpdated event raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreFeedUpdated struct {
	Value      int32
	Decimals   int8
	ObservedAt uint64
	TeeIds     []common.Address
	Raw        types.Log // Blockchain specific contextual infos
}

// FilterFeedUpdated is a free log retrieval operation binding the contract event 0x0102fd4bf40a18c2a6f0eccc4830c074f59ccda0015b6f40fc0f3915906a9b75.
//
// Solidity: event FeedUpdated(int32 indexed value, int8 decimals, uint64 indexed observedAt, address[] teeIds)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) FilterFeedUpdated(opts *bind.FilterOpts, value []int32, observedAt []uint64) (*IITeeOracleFeedStoreFeedUpdatedIterator, error) {

	var valueRule []interface{}
	for _, valueItem := range value {
		valueRule = append(valueRule, valueItem)
	}

	var observedAtRule []interface{}
	for _, observedAtItem := range observedAt {
		observedAtRule = append(observedAtRule, observedAtItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.FilterLogs(opts, "FeedUpdated", valueRule, observedAtRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreFeedUpdatedIterator{contract: _IITeeOracleFeedStore.contract, event: "FeedUpdated", logs: logs, sub: sub}, nil
}

// WatchFeedUpdated is a free log subscription operation binding the contract event 0x0102fd4bf40a18c2a6f0eccc4830c074f59ccda0015b6f40fc0f3915906a9b75.
//
// Solidity: event FeedUpdated(int32 indexed value, int8 decimals, uint64 indexed observedAt, address[] teeIds)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) WatchFeedUpdated(opts *bind.WatchOpts, sink chan<- *IITeeOracleFeedStoreFeedUpdated, value []int32, observedAt []uint64) (event.Subscription, error) {

	var valueRule []interface{}
	for _, valueItem := range value {
		valueRule = append(valueRule, valueItem)
	}

	var observedAtRule []interface{}
	for _, observedAtItem := range observedAt {
		observedAtRule = append(observedAtRule, observedAtItem)
	}

	logs, sub, err := _IITeeOracleFeedStore.contract.WatchLogs(opts, "FeedUpdated", valueRule, observedAtRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleFeedStoreFeedUpdated)
				if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedUpdated", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseFeedUpdated is a log parse operation binding the contract event 0x0102fd4bf40a18c2a6f0eccc4830c074f59ccda0015b6f40fc0f3915906a9b75.
//
// Solidity: event FeedUpdated(int32 indexed value, int8 decimals, uint64 indexed observedAt, address[] teeIds)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) ParseFeedUpdated(log types.Log) (*IITeeOracleFeedStoreFeedUpdated, error) {
	event := new(IITeeOracleFeedStoreFeedUpdated)
	if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "FeedUpdated", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleFeedStoreSubmissionPolicySetIterator is returned from FilterSubmissionPolicySet and is used to iterate over the raw logs and unpacked data for SubmissionPolicySet events raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreSubmissionPolicySetIterator struct {
	Event *IITeeOracleFeedStoreSubmissionPolicySet // Event containing the contract specifics and raw log

	contract *bind.BoundContract // Generic contract to use for unpacking event data
	event    string              // Event name to use for unpacking event data

	logs chan types.Log        // Log channel receiving the found contract events
	sub  ethereum.Subscription // Subscription for errors, completion and termination
	done bool                  // Whether the subscription completed delivering logs
	fail error                 // Occurred error to stop iteration
}

// Next advances the iterator to the subsequent event, returning whether there
// are any more events found. In case of a retrieval or parsing error, false is
// returned and Error() can be queried for the exact failure.
func (it *IITeeOracleFeedStoreSubmissionPolicySetIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleFeedStoreSubmissionPolicySet)
			if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
				it.fail = err
				return false
			}
			it.Event.Raw = log
			return true

		default:
			return false
		}
	}
	// Iterator still in progress, wait for either a data or an error event
	select {
	case log := <-it.logs:
		it.Event = new(IITeeOracleFeedStoreSubmissionPolicySet)
		if err := it.contract.UnpackLog(it.Event, it.event, log); err != nil {
			it.fail = err
			return false
		}
		it.Event.Raw = log
		return true

	case err := <-it.sub.Err():
		it.done = true
		it.fail = err
		return it.Next()
	}
}

// Error returns any retrieval or parsing error occurred during filtering.
func (it *IITeeOracleFeedStoreSubmissionPolicySetIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleFeedStoreSubmissionPolicySetIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleFeedStoreSubmissionPolicySet represents a SubmissionPolicySet event raised by the IITeeOracleFeedStore contract.
type IITeeOracleFeedStoreSubmissionPolicySet struct {
	RequiredSignatures uint8
	MaxSpreadBIPS      uint16
	MaxSpreadAbsolute  uint64
	Raw                types.Log // Blockchain specific contextual infos
}

// FilterSubmissionPolicySet is a free log retrieval operation binding the contract event 0x5a391d20e9c89d17d85f5b686c5c18afb3ba0ce1891c5e3207fc1b1edb655dcd.
//
// Solidity: event SubmissionPolicySet(uint8 requiredSignatures, uint16 maxSpreadBIPS, uint64 maxSpreadAbsolute)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) FilterSubmissionPolicySet(opts *bind.FilterOpts) (*IITeeOracleFeedStoreSubmissionPolicySetIterator, error) {

	logs, sub, err := _IITeeOracleFeedStore.contract.FilterLogs(opts, "SubmissionPolicySet")
	if err != nil {
		return nil, err
	}
	return &IITeeOracleFeedStoreSubmissionPolicySetIterator{contract: _IITeeOracleFeedStore.contract, event: "SubmissionPolicySet", logs: logs, sub: sub}, nil
}

// WatchSubmissionPolicySet is a free log subscription operation binding the contract event 0x5a391d20e9c89d17d85f5b686c5c18afb3ba0ce1891c5e3207fc1b1edb655dcd.
//
// Solidity: event SubmissionPolicySet(uint8 requiredSignatures, uint16 maxSpreadBIPS, uint64 maxSpreadAbsolute)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) WatchSubmissionPolicySet(opts *bind.WatchOpts, sink chan<- *IITeeOracleFeedStoreSubmissionPolicySet) (event.Subscription, error) {

	logs, sub, err := _IITeeOracleFeedStore.contract.WatchLogs(opts, "SubmissionPolicySet")
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleFeedStoreSubmissionPolicySet)
				if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "SubmissionPolicySet", log); err != nil {
					return err
				}
				event.Raw = log

				select {
				case sink <- event:
				case err := <-sub.Err():
					return err
				case <-quit:
					return nil
				}
			case err := <-sub.Err():
				return err
			case <-quit:
				return nil
			}
		}
	}), nil
}

// ParseSubmissionPolicySet is a log parse operation binding the contract event 0x5a391d20e9c89d17d85f5b686c5c18afb3ba0ce1891c5e3207fc1b1edb655dcd.
//
// Solidity: event SubmissionPolicySet(uint8 requiredSignatures, uint16 maxSpreadBIPS, uint64 maxSpreadAbsolute)
func (_IITeeOracleFeedStore *IITeeOracleFeedStoreFilterer) ParseSubmissionPolicySet(log types.Log) (*IITeeOracleFeedStoreSubmissionPolicySet, error) {
	event := new(IITeeOracleFeedStoreSubmissionPolicySet)
	if err := _IITeeOracleFeedStore.contract.UnpackLog(event, "SubmissionPolicySet", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
