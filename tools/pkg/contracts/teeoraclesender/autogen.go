// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package teeoraclesender

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

// ITeeOracleInstructionsSenderAdminRole is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderAdminRole struct {
	Role      [32]byte
	Threshold uint64
	Admins    []common.Address
}

// ITeeOracleInstructionsSenderEndpoint is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderEndpoint struct {
	Kind      uint8
	Url       string
	UrlHash   [32]byte
	SecretRef string
}

// ITeeOracleInstructionsSenderEndpointGroup is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderEndpointGroup struct {
	Group     [32]byte
	Threshold uint64
	Endpoints []ITeeOracleInstructionsSenderEndpoint
}

// ITeeOracleInstructionsSenderFeedConfig is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderFeedConfig struct {
	EndpointsHash        [32]byte
	AdminsHash           [32]byte
	EndpointsVersion     uint64
	AdminsVersion        uint64
	EndpointsPublishedAt uint64
	AdminsPublishedAt    uint64
}

// ITeeOracleInstructionsSenderMachineVersions is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderMachineVersions struct {
	EndpointsVersion uint64
	AdminsVersion    uint64
}

// IITeeOracleInstructionsSenderMetaData contains all meta data concerning the IITeeOracleInstructionsSender contract.
var IITeeOracleInstructionsSenderMetaData = &bind.MetaData{
	ABI: "[{\"type\":\"function\",\"name\":\"adminsPublishedAt\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"adminsVersion\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"endpointsPublishedAt\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"endpointsVersion\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"expectedAdminsVersion\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeId\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"expectedEndpointsVersion\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeId\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"uint64\",\"internalType\":\"uint64\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"extensionId\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"uint256\",\"internalType\":\"uint256\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getAdminsPublicationFee\",\"inputs\":[],\"outputs\":[{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"},{\"name\":\"_fee\",\"type\":\"uint256\",\"internalType\":\"uint256\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getAdminsPushTargets\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getEndpointsPublicationFee\",\"inputs\":[],\"outputs\":[{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"},{\"name\":\"_fee\",\"type\":\"uint256\",\"internalType\":\"uint256\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getEndpointsPushTargets\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getFeedConfig\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.FeedConfig\",\"components\":[{\"name\":\"endpointsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"adminsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"endpointsVersion\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"adminsVersion\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpointsPublishedAt\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"adminsPublishedAt\",\"type\":\"uint64\",\"internalType\":\"uint64\"}]}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getFeedIds\",\"inputs\":[],\"outputs\":[{\"name\":\"\",\"type\":\"bytes21[]\",\"internalType\":\"bytes21[]\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"getMachineVersions\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeId\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.MachineVersions\",\"components\":[{\"name\":\"endpointsVersion\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"adminsVersion\",\"type\":\"uint64\",\"internalType\":\"uint64\"}]}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"isTeeIdConfigured\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeId\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[{\"name\":\"\",\"type\":\"bool\",\"internalType\":\"bool\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"latestAdminsHash\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"latestEndpointsHash\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}],\"outputs\":[{\"name\":\"\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}],\"stateMutability\":\"view\"},{\"type\":\"function\",\"name\":\"pushAdmins\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"},{\"name\":\"_roles\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.AdminRole[]\",\"components\":[{\"name\":\"role\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"admins\",\"type\":\"address[]\",\"internalType\":\"address[]\"}]}],\"outputs\":[{\"name\":\"_instructionId\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}],\"stateMutability\":\"payable\"},{\"type\":\"function\",\"name\":\"pushEndpoints\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"},{\"name\":\"_groups\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.EndpointGroup[]\",\"components\":[{\"name\":\"group\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpoints\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint[]\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}]}],\"outputs\":[{\"name\":\"_instructionId\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}],\"stateMutability\":\"payable\"},{\"type\":\"function\",\"name\":\"requestFeedUpdate\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_teeIds\",\"type\":\"address[]\",\"internalType\":\"address[]\"}],\"outputs\":[{\"name\":\"_instructionId\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}],\"stateMutability\":\"payable\"},{\"type\":\"function\",\"name\":\"setAdmins\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_roles\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.AdminRole[]\",\"components\":[{\"name\":\"role\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"admins\",\"type\":\"address[]\",\"internalType\":\"address[]\"}]},{\"name\":\"_claimBackAddress\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[],\"stateMutability\":\"payable\"},{\"type\":\"function\",\"name\":\"setEndpoints\",\"inputs\":[{\"name\":\"_feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"_groups\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.EndpointGroup[]\",\"components\":[{\"name\":\"group\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpoints\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint[]\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}]},{\"name\":\"_claimBackAddress\",\"type\":\"address\",\"internalType\":\"address\"}],\"outputs\":[],\"stateMutability\":\"payable\"},{\"type\":\"event\",\"name\":\"AdminsPublished\",\"inputs\":[{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"},{\"name\":\"version\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"adminsHash\",\"type\":\"bytes32\",\"indexed\":false,\"internalType\":\"bytes32\"},{\"name\":\"roles\",\"type\":\"tuple[]\",\"indexed\":false,\"internalType\":\"structITeeOracleInstructionsSender.AdminRole[]\",\"components\":[{\"name\":\"role\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"admins\",\"type\":\"address[]\",\"internalType\":\"address[]\"}]}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"AdminsSet\",\"inputs\":[{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"},{\"name\":\"teeId\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"},{\"name\":\"version\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"adminsHash\",\"type\":\"bytes32\",\"indexed\":false,\"internalType\":\"bytes32\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"EndpointsPublished\",\"inputs\":[{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"},{\"name\":\"version\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"endpointsHash\",\"type\":\"bytes32\",\"indexed\":false,\"internalType\":\"bytes32\"},{\"name\":\"groups\",\"type\":\"tuple[]\",\"indexed\":false,\"internalType\":\"structITeeOracleInstructionsSender.EndpointGroup[]\",\"components\":[{\"name\":\"group\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpoints\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint[]\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}]}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"EndpointsSet\",\"inputs\":[{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"},{\"name\":\"teeId\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"},{\"name\":\"version\",\"type\":\"uint64\",\"indexed\":true,\"internalType\":\"uint64\"},{\"name\":\"endpointsHash\",\"type\":\"bytes32\",\"indexed\":false,\"internalType\":\"bytes32\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"FeedUpdateRequested\",\"inputs\":[{\"name\":\"requester\",\"type\":\"address\",\"indexed\":true,\"internalType\":\"address\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"indexed\":true,\"internalType\":\"bytes21\"},{\"name\":\"instructionId\",\"type\":\"bytes32\",\"indexed\":true,\"internalType\":\"bytes32\"}],\"anonymous\":false},{\"type\":\"event\",\"name\":\"InstructionsSenderInitialised\",\"inputs\":[{\"name\":\"extensionId\",\"type\":\"uint256\",\"indexed\":true,\"internalType\":\"uint256\"}],\"anonymous\":false},{\"type\":\"error\",\"name\":\"DuplicateAdmin\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"DuplicateGroup\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"DuplicateRole\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"DuplicateTeeId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"EmptyGroup\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"EmptyRole\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"FeedNotConfigured\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidEndpoint\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidExtensionId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidFeedId\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"InvalidThreshold\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoAdmins\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoConfigPublished\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoEndpoints\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoGroups\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoRoles\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"NoTeeIds\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"TeeIdNotConfigured\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"TeeIdNotInExtension\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ValueNotNeeded\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"WrongConfigPayload\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroAddressUpdater\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroAdmin\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroClaimBackAddress\",\"inputs\":[]},{\"type\":\"error\",\"name\":\"ZeroTeeId\",\"inputs\":[]}]",
}

// IITeeOracleInstructionsSenderABI is the input ABI used to generate the binding from.
// Deprecated: Use IITeeOracleInstructionsSenderMetaData.ABI instead.
var IITeeOracleInstructionsSenderABI = IITeeOracleInstructionsSenderMetaData.ABI

// IITeeOracleInstructionsSender is an auto generated Go binding around an Ethereum contract.
type IITeeOracleInstructionsSender struct {
	IITeeOracleInstructionsSenderCaller     // Read-only binding to the contract
	IITeeOracleInstructionsSenderTransactor // Write-only binding to the contract
	IITeeOracleInstructionsSenderFilterer   // Log filterer for contract events
}

// IITeeOracleInstructionsSenderCaller is an auto generated read-only Go binding around an Ethereum contract.
type IITeeOracleInstructionsSenderCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleInstructionsSenderTransactor is an auto generated write-only Go binding around an Ethereum contract.
type IITeeOracleInstructionsSenderTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleInstructionsSenderFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type IITeeOracleInstructionsSenderFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// IITeeOracleInstructionsSenderSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type IITeeOracleInstructionsSenderSession struct {
	Contract     *IITeeOracleInstructionsSender // Generic contract binding to set the session for
	CallOpts     bind.CallOpts                  // Call options to use throughout this session
	TransactOpts bind.TransactOpts              // Transaction auth options to use throughout this session
}

// IITeeOracleInstructionsSenderCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type IITeeOracleInstructionsSenderCallerSession struct {
	Contract *IITeeOracleInstructionsSenderCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts                        // Call options to use throughout this session
}

// IITeeOracleInstructionsSenderTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type IITeeOracleInstructionsSenderTransactorSession struct {
	Contract     *IITeeOracleInstructionsSenderTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts                        // Transaction auth options to use throughout this session
}

// IITeeOracleInstructionsSenderRaw is an auto generated low-level Go binding around an Ethereum contract.
type IITeeOracleInstructionsSenderRaw struct {
	Contract *IITeeOracleInstructionsSender // Generic contract binding to access the raw methods on
}

// IITeeOracleInstructionsSenderCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type IITeeOracleInstructionsSenderCallerRaw struct {
	Contract *IITeeOracleInstructionsSenderCaller // Generic read-only contract binding to access the raw methods on
}

// IITeeOracleInstructionsSenderTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type IITeeOracleInstructionsSenderTransactorRaw struct {
	Contract *IITeeOracleInstructionsSenderTransactor // Generic write-only contract binding to access the raw methods on
}

// NewIITeeOracleInstructionsSender creates a new instance of IITeeOracleInstructionsSender, bound to a specific deployed contract.
func NewIITeeOracleInstructionsSender(address common.Address, backend bind.ContractBackend) (*IITeeOracleInstructionsSender, error) {
	contract, err := bindIITeeOracleInstructionsSender(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSender{IITeeOracleInstructionsSenderCaller: IITeeOracleInstructionsSenderCaller{contract: contract}, IITeeOracleInstructionsSenderTransactor: IITeeOracleInstructionsSenderTransactor{contract: contract}, IITeeOracleInstructionsSenderFilterer: IITeeOracleInstructionsSenderFilterer{contract: contract}}, nil
}

// NewIITeeOracleInstructionsSenderCaller creates a new read-only instance of IITeeOracleInstructionsSender, bound to a specific deployed contract.
func NewIITeeOracleInstructionsSenderCaller(address common.Address, caller bind.ContractCaller) (*IITeeOracleInstructionsSenderCaller, error) {
	contract, err := bindIITeeOracleInstructionsSender(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderCaller{contract: contract}, nil
}

// NewIITeeOracleInstructionsSenderTransactor creates a new write-only instance of IITeeOracleInstructionsSender, bound to a specific deployed contract.
func NewIITeeOracleInstructionsSenderTransactor(address common.Address, transactor bind.ContractTransactor) (*IITeeOracleInstructionsSenderTransactor, error) {
	contract, err := bindIITeeOracleInstructionsSender(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderTransactor{contract: contract}, nil
}

// NewIITeeOracleInstructionsSenderFilterer creates a new log filterer instance of IITeeOracleInstructionsSender, bound to a specific deployed contract.
func NewIITeeOracleInstructionsSenderFilterer(address common.Address, filterer bind.ContractFilterer) (*IITeeOracleInstructionsSenderFilterer, error) {
	contract, err := bindIITeeOracleInstructionsSender(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderFilterer{contract: contract}, nil
}

// bindIITeeOracleInstructionsSender binds a generic wrapper to an already deployed contract.
func bindIITeeOracleInstructionsSender(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := IITeeOracleInstructionsSenderMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IITeeOracleInstructionsSender.Contract.IITeeOracleInstructionsSenderCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.IITeeOracleInstructionsSenderTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.IITeeOracleInstructionsSenderTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _IITeeOracleInstructionsSender.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.contract.Transact(opts, method, params...)
}

// AdminsPublishedAt is a free data retrieval call binding the contract method 0xfa743df6.
//
// Solidity: function adminsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) AdminsPublishedAt(opts *bind.CallOpts, _feedId [21]byte) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "adminsPublishedAt", _feedId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// AdminsPublishedAt is a free data retrieval call binding the contract method 0xfa743df6.
//
// Solidity: function adminsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) AdminsPublishedAt(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.AdminsPublishedAt(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// AdminsPublishedAt is a free data retrieval call binding the contract method 0xfa743df6.
//
// Solidity: function adminsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) AdminsPublishedAt(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.AdminsPublishedAt(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// AdminsVersion is a free data retrieval call binding the contract method 0x6f8f616f.
//
// Solidity: function adminsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) AdminsVersion(opts *bind.CallOpts, _feedId [21]byte) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "adminsVersion", _feedId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// AdminsVersion is a free data retrieval call binding the contract method 0x6f8f616f.
//
// Solidity: function adminsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) AdminsVersion(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.AdminsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// AdminsVersion is a free data retrieval call binding the contract method 0x6f8f616f.
//
// Solidity: function adminsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) AdminsVersion(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.AdminsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// EndpointsPublishedAt is a free data retrieval call binding the contract method 0x7452738e.
//
// Solidity: function endpointsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) EndpointsPublishedAt(opts *bind.CallOpts, _feedId [21]byte) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "endpointsPublishedAt", _feedId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// EndpointsPublishedAt is a free data retrieval call binding the contract method 0x7452738e.
//
// Solidity: function endpointsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) EndpointsPublishedAt(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.EndpointsPublishedAt(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// EndpointsPublishedAt is a free data retrieval call binding the contract method 0x7452738e.
//
// Solidity: function endpointsPublishedAt(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) EndpointsPublishedAt(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.EndpointsPublishedAt(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// EndpointsVersion is a free data retrieval call binding the contract method 0x2a65c586.
//
// Solidity: function endpointsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) EndpointsVersion(opts *bind.CallOpts, _feedId [21]byte) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "endpointsVersion", _feedId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// EndpointsVersion is a free data retrieval call binding the contract method 0x2a65c586.
//
// Solidity: function endpointsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) EndpointsVersion(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.EndpointsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// EndpointsVersion is a free data retrieval call binding the contract method 0x2a65c586.
//
// Solidity: function endpointsVersion(bytes21 _feedId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) EndpointsVersion(_feedId [21]byte) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.EndpointsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// ExpectedAdminsVersion is a free data retrieval call binding the contract method 0x555144b8.
//
// Solidity: function expectedAdminsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) ExpectedAdminsVersion(opts *bind.CallOpts, _feedId [21]byte, _teeId common.Address) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "expectedAdminsVersion", _feedId, _teeId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// ExpectedAdminsVersion is a free data retrieval call binding the contract method 0x555144b8.
//
// Solidity: function expectedAdminsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) ExpectedAdminsVersion(_feedId [21]byte, _teeId common.Address) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.ExpectedAdminsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// ExpectedAdminsVersion is a free data retrieval call binding the contract method 0x555144b8.
//
// Solidity: function expectedAdminsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) ExpectedAdminsVersion(_feedId [21]byte, _teeId common.Address) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.ExpectedAdminsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// ExpectedEndpointsVersion is a free data retrieval call binding the contract method 0x0a174388.
//
// Solidity: function expectedEndpointsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) ExpectedEndpointsVersion(opts *bind.CallOpts, _feedId [21]byte, _teeId common.Address) (uint64, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "expectedEndpointsVersion", _feedId, _teeId)

	if err != nil {
		return *new(uint64), err
	}

	out0 := *abi.ConvertType(out[0], new(uint64)).(*uint64)

	return out0, err

}

// ExpectedEndpointsVersion is a free data retrieval call binding the contract method 0x0a174388.
//
// Solidity: function expectedEndpointsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) ExpectedEndpointsVersion(_feedId [21]byte, _teeId common.Address) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.ExpectedEndpointsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// ExpectedEndpointsVersion is a free data retrieval call binding the contract method 0x0a174388.
//
// Solidity: function expectedEndpointsVersion(bytes21 _feedId, address _teeId) view returns(uint64)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) ExpectedEndpointsVersion(_feedId [21]byte, _teeId common.Address) (uint64, error) {
	return _IITeeOracleInstructionsSender.Contract.ExpectedEndpointsVersion(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) ExtensionId(opts *bind.CallOpts) (*big.Int, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "extensionId")

	if err != nil {
		return *new(*big.Int), err
	}

	out0 := *abi.ConvertType(out[0], new(*big.Int)).(**big.Int)

	return out0, err

}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) ExtensionId() (*big.Int, error) {
	return _IITeeOracleInstructionsSender.Contract.ExtensionId(&_IITeeOracleInstructionsSender.CallOpts)
}

// ExtensionId is a free data retrieval call binding the contract method 0x62d7076e.
//
// Solidity: function extensionId() view returns(uint256)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) ExtensionId() (*big.Int, error) {
	return _IITeeOracleInstructionsSender.Contract.ExtensionId(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetAdminsPublicationFee is a free data retrieval call binding the contract method 0x38f05d87.
//
// Solidity: function getAdminsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetAdminsPublicationFee(opts *bind.CallOpts) (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getAdminsPublicationFee")

	outstruct := new(struct {
		TeeIds []common.Address
		Fee    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.TeeIds = *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
	outstruct.Fee = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetAdminsPublicationFee is a free data retrieval call binding the contract method 0x38f05d87.
//
// Solidity: function getAdminsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetAdminsPublicationFee() (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	return _IITeeOracleInstructionsSender.Contract.GetAdminsPublicationFee(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetAdminsPublicationFee is a free data retrieval call binding the contract method 0x38f05d87.
//
// Solidity: function getAdminsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetAdminsPublicationFee() (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	return _IITeeOracleInstructionsSender.Contract.GetAdminsPublicationFee(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetAdminsPushTargets is a free data retrieval call binding the contract method 0xb08371ac.
//
// Solidity: function getAdminsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetAdminsPushTargets(opts *bind.CallOpts, _feedId [21]byte) ([]common.Address, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getAdminsPushTargets", _feedId)

	if err != nil {
		return *new([]common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)

	return out0, err

}

// GetAdminsPushTargets is a free data retrieval call binding the contract method 0xb08371ac.
//
// Solidity: function getAdminsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetAdminsPushTargets(_feedId [21]byte) ([]common.Address, error) {
	return _IITeeOracleInstructionsSender.Contract.GetAdminsPushTargets(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetAdminsPushTargets is a free data retrieval call binding the contract method 0xb08371ac.
//
// Solidity: function getAdminsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetAdminsPushTargets(_feedId [21]byte) ([]common.Address, error) {
	return _IITeeOracleInstructionsSender.Contract.GetAdminsPushTargets(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetEndpointsPublicationFee is a free data retrieval call binding the contract method 0x7e2489c5.
//
// Solidity: function getEndpointsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetEndpointsPublicationFee(opts *bind.CallOpts) (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getEndpointsPublicationFee")

	outstruct := new(struct {
		TeeIds []common.Address
		Fee    *big.Int
	})
	if err != nil {
		return *outstruct, err
	}

	outstruct.TeeIds = *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)
	outstruct.Fee = *abi.ConvertType(out[1], new(*big.Int)).(**big.Int)

	return *outstruct, err

}

// GetEndpointsPublicationFee is a free data retrieval call binding the contract method 0x7e2489c5.
//
// Solidity: function getEndpointsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetEndpointsPublicationFee() (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	return _IITeeOracleInstructionsSender.Contract.GetEndpointsPublicationFee(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetEndpointsPublicationFee is a free data retrieval call binding the contract method 0x7e2489c5.
//
// Solidity: function getEndpointsPublicationFee() view returns(address[] _teeIds, uint256 _fee)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetEndpointsPublicationFee() (struct {
	TeeIds []common.Address
	Fee    *big.Int
}, error) {
	return _IITeeOracleInstructionsSender.Contract.GetEndpointsPublicationFee(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetEndpointsPushTargets is a free data retrieval call binding the contract method 0xb93d173d.
//
// Solidity: function getEndpointsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetEndpointsPushTargets(opts *bind.CallOpts, _feedId [21]byte) ([]common.Address, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getEndpointsPushTargets", _feedId)

	if err != nil {
		return *new([]common.Address), err
	}

	out0 := *abi.ConvertType(out[0], new([]common.Address)).(*[]common.Address)

	return out0, err

}

// GetEndpointsPushTargets is a free data retrieval call binding the contract method 0xb93d173d.
//
// Solidity: function getEndpointsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetEndpointsPushTargets(_feedId [21]byte) ([]common.Address, error) {
	return _IITeeOracleInstructionsSender.Contract.GetEndpointsPushTargets(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetEndpointsPushTargets is a free data retrieval call binding the contract method 0xb93d173d.
//
// Solidity: function getEndpointsPushTargets(bytes21 _feedId) view returns(address[] _teeIds)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetEndpointsPushTargets(_feedId [21]byte) ([]common.Address, error) {
	return _IITeeOracleInstructionsSender.Contract.GetEndpointsPushTargets(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetFeedConfig is a free data retrieval call binding the contract method 0x32f702cf.
//
// Solidity: function getFeedConfig(bytes21 _feedId) view returns((bytes32,bytes32,uint64,uint64,uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetFeedConfig(opts *bind.CallOpts, _feedId [21]byte) (ITeeOracleInstructionsSenderFeedConfig, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getFeedConfig", _feedId)

	if err != nil {
		return *new(ITeeOracleInstructionsSenderFeedConfig), err
	}

	out0 := *abi.ConvertType(out[0], new(ITeeOracleInstructionsSenderFeedConfig)).(*ITeeOracleInstructionsSenderFeedConfig)

	return out0, err

}

// GetFeedConfig is a free data retrieval call binding the contract method 0x32f702cf.
//
// Solidity: function getFeedConfig(bytes21 _feedId) view returns((bytes32,bytes32,uint64,uint64,uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetFeedConfig(_feedId [21]byte) (ITeeOracleInstructionsSenderFeedConfig, error) {
	return _IITeeOracleInstructionsSender.Contract.GetFeedConfig(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetFeedConfig is a free data retrieval call binding the contract method 0x32f702cf.
//
// Solidity: function getFeedConfig(bytes21 _feedId) view returns((bytes32,bytes32,uint64,uint64,uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetFeedConfig(_feedId [21]byte) (ITeeOracleInstructionsSenderFeedConfig, error) {
	return _IITeeOracleInstructionsSender.Contract.GetFeedConfig(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// GetFeedIds is a free data retrieval call binding the contract method 0x0c518dce.
//
// Solidity: function getFeedIds() view returns(bytes21[])
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetFeedIds(opts *bind.CallOpts) ([][21]byte, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getFeedIds")

	if err != nil {
		return *new([][21]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([][21]byte)).(*[][21]byte)

	return out0, err

}

// GetFeedIds is a free data retrieval call binding the contract method 0x0c518dce.
//
// Solidity: function getFeedIds() view returns(bytes21[])
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetFeedIds() ([][21]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.GetFeedIds(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetFeedIds is a free data retrieval call binding the contract method 0x0c518dce.
//
// Solidity: function getFeedIds() view returns(bytes21[])
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetFeedIds() ([][21]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.GetFeedIds(&_IITeeOracleInstructionsSender.CallOpts)
}

// GetMachineVersions is a free data retrieval call binding the contract method 0xc838fbac.
//
// Solidity: function getMachineVersions(bytes21 _feedId, address _teeId) view returns((uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) GetMachineVersions(opts *bind.CallOpts, _feedId [21]byte, _teeId common.Address) (ITeeOracleInstructionsSenderMachineVersions, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "getMachineVersions", _feedId, _teeId)

	if err != nil {
		return *new(ITeeOracleInstructionsSenderMachineVersions), err
	}

	out0 := *abi.ConvertType(out[0], new(ITeeOracleInstructionsSenderMachineVersions)).(*ITeeOracleInstructionsSenderMachineVersions)

	return out0, err

}

// GetMachineVersions is a free data retrieval call binding the contract method 0xc838fbac.
//
// Solidity: function getMachineVersions(bytes21 _feedId, address _teeId) view returns((uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) GetMachineVersions(_feedId [21]byte, _teeId common.Address) (ITeeOracleInstructionsSenderMachineVersions, error) {
	return _IITeeOracleInstructionsSender.Contract.GetMachineVersions(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// GetMachineVersions is a free data retrieval call binding the contract method 0xc838fbac.
//
// Solidity: function getMachineVersions(bytes21 _feedId, address _teeId) view returns((uint64,uint64))
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) GetMachineVersions(_feedId [21]byte, _teeId common.Address) (ITeeOracleInstructionsSenderMachineVersions, error) {
	return _IITeeOracleInstructionsSender.Contract.GetMachineVersions(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// IsTeeIdConfigured is a free data retrieval call binding the contract method 0x8c9c62f9.
//
// Solidity: function isTeeIdConfigured(bytes21 _feedId, address _teeId) view returns(bool)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) IsTeeIdConfigured(opts *bind.CallOpts, _feedId [21]byte, _teeId common.Address) (bool, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "isTeeIdConfigured", _feedId, _teeId)

	if err != nil {
		return *new(bool), err
	}

	out0 := *abi.ConvertType(out[0], new(bool)).(*bool)

	return out0, err

}

// IsTeeIdConfigured is a free data retrieval call binding the contract method 0x8c9c62f9.
//
// Solidity: function isTeeIdConfigured(bytes21 _feedId, address _teeId) view returns(bool)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) IsTeeIdConfigured(_feedId [21]byte, _teeId common.Address) (bool, error) {
	return _IITeeOracleInstructionsSender.Contract.IsTeeIdConfigured(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// IsTeeIdConfigured is a free data retrieval call binding the contract method 0x8c9c62f9.
//
// Solidity: function isTeeIdConfigured(bytes21 _feedId, address _teeId) view returns(bool)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) IsTeeIdConfigured(_feedId [21]byte, _teeId common.Address) (bool, error) {
	return _IITeeOracleInstructionsSender.Contract.IsTeeIdConfigured(&_IITeeOracleInstructionsSender.CallOpts, _feedId, _teeId)
}

// LatestAdminsHash is a free data retrieval call binding the contract method 0xfc9c3a22.
//
// Solidity: function latestAdminsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) LatestAdminsHash(opts *bind.CallOpts, _feedId [21]byte) ([32]byte, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "latestAdminsHash", _feedId)

	if err != nil {
		return *new([32]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([32]byte)).(*[32]byte)

	return out0, err

}

// LatestAdminsHash is a free data retrieval call binding the contract method 0xfc9c3a22.
//
// Solidity: function latestAdminsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) LatestAdminsHash(_feedId [21]byte) ([32]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.LatestAdminsHash(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// LatestAdminsHash is a free data retrieval call binding the contract method 0xfc9c3a22.
//
// Solidity: function latestAdminsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) LatestAdminsHash(_feedId [21]byte) ([32]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.LatestAdminsHash(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// LatestEndpointsHash is a free data retrieval call binding the contract method 0x7713ee15.
//
// Solidity: function latestEndpointsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCaller) LatestEndpointsHash(opts *bind.CallOpts, _feedId [21]byte) ([32]byte, error) {
	var out []interface{}
	err := _IITeeOracleInstructionsSender.contract.Call(opts, &out, "latestEndpointsHash", _feedId)

	if err != nil {
		return *new([32]byte), err
	}

	out0 := *abi.ConvertType(out[0], new([32]byte)).(*[32]byte)

	return out0, err

}

// LatestEndpointsHash is a free data retrieval call binding the contract method 0x7713ee15.
//
// Solidity: function latestEndpointsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) LatestEndpointsHash(_feedId [21]byte) ([32]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.LatestEndpointsHash(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// LatestEndpointsHash is a free data retrieval call binding the contract method 0x7713ee15.
//
// Solidity: function latestEndpointsHash(bytes21 _feedId) view returns(bytes32)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderCallerSession) LatestEndpointsHash(_feedId [21]byte) ([32]byte, error) {
	return _IITeeOracleInstructionsSender.Contract.LatestEndpointsHash(&_IITeeOracleInstructionsSender.CallOpts, _feedId)
}

// PushAdmins is a paid mutator transaction binding the contract method 0xf6dfdb95.
//
// Solidity: function pushAdmins(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,address[])[] _roles) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactor) PushAdmins(opts *bind.TransactOpts, _feedId [21]byte, _teeIds []common.Address, _roles []ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.contract.Transact(opts, "pushAdmins", _feedId, _teeIds, _roles)
}

// PushAdmins is a paid mutator transaction binding the contract method 0xf6dfdb95.
//
// Solidity: function pushAdmins(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,address[])[] _roles) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) PushAdmins(_feedId [21]byte, _teeIds []common.Address, _roles []ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.PushAdmins(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds, _roles)
}

// PushAdmins is a paid mutator transaction binding the contract method 0xf6dfdb95.
//
// Solidity: function pushAdmins(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,address[])[] _roles) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorSession) PushAdmins(_feedId [21]byte, _teeIds []common.Address, _roles []ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.PushAdmins(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds, _roles)
}

// PushEndpoints is a paid mutator transaction binding the contract method 0xd63baf54.
//
// Solidity: function pushEndpoints(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactor) PushEndpoints(opts *bind.TransactOpts, _feedId [21]byte, _teeIds []common.Address, _groups []ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.contract.Transact(opts, "pushEndpoints", _feedId, _teeIds, _groups)
}

// PushEndpoints is a paid mutator transaction binding the contract method 0xd63baf54.
//
// Solidity: function pushEndpoints(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) PushEndpoints(_feedId [21]byte, _teeIds []common.Address, _groups []ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.PushEndpoints(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds, _groups)
}

// PushEndpoints is a paid mutator transaction binding the contract method 0xd63baf54.
//
// Solidity: function pushEndpoints(bytes21 _feedId, address[] _teeIds, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorSession) PushEndpoints(_feedId [21]byte, _teeIds []common.Address, _groups []ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.PushEndpoints(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds, _groups)
}

// RequestFeedUpdate is a paid mutator transaction binding the contract method 0x7d4e3923.
//
// Solidity: function requestFeedUpdate(bytes21 _feedId, address[] _teeIds) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactor) RequestFeedUpdate(opts *bind.TransactOpts, _feedId [21]byte, _teeIds []common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.contract.Transact(opts, "requestFeedUpdate", _feedId, _teeIds)
}

// RequestFeedUpdate is a paid mutator transaction binding the contract method 0x7d4e3923.
//
// Solidity: function requestFeedUpdate(bytes21 _feedId, address[] _teeIds) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) RequestFeedUpdate(_feedId [21]byte, _teeIds []common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.RequestFeedUpdate(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds)
}

// RequestFeedUpdate is a paid mutator transaction binding the contract method 0x7d4e3923.
//
// Solidity: function requestFeedUpdate(bytes21 _feedId, address[] _teeIds) payable returns(bytes32 _instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorSession) RequestFeedUpdate(_feedId [21]byte, _teeIds []common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.RequestFeedUpdate(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _teeIds)
}

// SetAdmins is a paid mutator transaction binding the contract method 0xa87d3458.
//
// Solidity: function setAdmins(bytes21 _feedId, (bytes32,uint64,address[])[] _roles, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactor) SetAdmins(opts *bind.TransactOpts, _feedId [21]byte, _roles []ITeeOracleInstructionsSenderAdminRole, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.contract.Transact(opts, "setAdmins", _feedId, _roles, _claimBackAddress)
}

// SetAdmins is a paid mutator transaction binding the contract method 0xa87d3458.
//
// Solidity: function setAdmins(bytes21 _feedId, (bytes32,uint64,address[])[] _roles, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) SetAdmins(_feedId [21]byte, _roles []ITeeOracleInstructionsSenderAdminRole, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.SetAdmins(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _roles, _claimBackAddress)
}

// SetAdmins is a paid mutator transaction binding the contract method 0xa87d3458.
//
// Solidity: function setAdmins(bytes21 _feedId, (bytes32,uint64,address[])[] _roles, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorSession) SetAdmins(_feedId [21]byte, _roles []ITeeOracleInstructionsSenderAdminRole, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.SetAdmins(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _roles, _claimBackAddress)
}

// SetEndpoints is a paid mutator transaction binding the contract method 0xa6049047.
//
// Solidity: function setEndpoints(bytes21 _feedId, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactor) SetEndpoints(opts *bind.TransactOpts, _feedId [21]byte, _groups []ITeeOracleInstructionsSenderEndpointGroup, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.contract.Transact(opts, "setEndpoints", _feedId, _groups, _claimBackAddress)
}

// SetEndpoints is a paid mutator transaction binding the contract method 0xa6049047.
//
// Solidity: function setEndpoints(bytes21 _feedId, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderSession) SetEndpoints(_feedId [21]byte, _groups []ITeeOracleInstructionsSenderEndpointGroup, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.SetEndpoints(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _groups, _claimBackAddress)
}

// SetEndpoints is a paid mutator transaction binding the contract method 0xa6049047.
//
// Solidity: function setEndpoints(bytes21 _feedId, (bytes32,uint64,(uint8,string,bytes32,string)[])[] _groups, address _claimBackAddress) payable returns()
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderTransactorSession) SetEndpoints(_feedId [21]byte, _groups []ITeeOracleInstructionsSenderEndpointGroup, _claimBackAddress common.Address) (*types.Transaction, error) {
	return _IITeeOracleInstructionsSender.Contract.SetEndpoints(&_IITeeOracleInstructionsSender.TransactOpts, _feedId, _groups, _claimBackAddress)
}

// IITeeOracleInstructionsSenderAdminsPublishedIterator is returned from FilterAdminsPublished and is used to iterate over the raw logs and unpacked data for AdminsPublished events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderAdminsPublishedIterator struct {
	Event *IITeeOracleInstructionsSenderAdminsPublished // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderAdminsPublishedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderAdminsPublished)
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
		it.Event = new(IITeeOracleInstructionsSenderAdminsPublished)
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
func (it *IITeeOracleInstructionsSenderAdminsPublishedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderAdminsPublishedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderAdminsPublished represents a AdminsPublished event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderAdminsPublished struct {
	FeedId     [21]byte
	Version    uint64
	AdminsHash [32]byte
	Roles      []ITeeOracleInstructionsSenderAdminRole
	Raw        types.Log // Blockchain specific contextual infos
}

// FilterAdminsPublished is a free log retrieval operation binding the contract event 0x924ca7497497c8c163e5b4e581ac727e6c35563fbb25609176ed998c0321d51d.
//
// Solidity: event AdminsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 adminsHash, (bytes32,uint64,address[])[] roles)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterAdminsPublished(opts *bind.FilterOpts, feedId [][21]byte, version []uint64) (*IITeeOracleInstructionsSenderAdminsPublishedIterator, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "AdminsPublished", feedIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderAdminsPublishedIterator{contract: _IITeeOracleInstructionsSender.contract, event: "AdminsPublished", logs: logs, sub: sub}, nil
}

// WatchAdminsPublished is a free log subscription operation binding the contract event 0x924ca7497497c8c163e5b4e581ac727e6c35563fbb25609176ed998c0321d51d.
//
// Solidity: event AdminsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 adminsHash, (bytes32,uint64,address[])[] roles)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchAdminsPublished(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderAdminsPublished, feedId [][21]byte, version []uint64) (event.Subscription, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "AdminsPublished", feedIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderAdminsPublished)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "AdminsPublished", log); err != nil {
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

// ParseAdminsPublished is a log parse operation binding the contract event 0x924ca7497497c8c163e5b4e581ac727e6c35563fbb25609176ed998c0321d51d.
//
// Solidity: event AdminsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 adminsHash, (bytes32,uint64,address[])[] roles)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseAdminsPublished(log types.Log) (*IITeeOracleInstructionsSenderAdminsPublished, error) {
	event := new(IITeeOracleInstructionsSenderAdminsPublished)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "AdminsPublished", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleInstructionsSenderAdminsSetIterator is returned from FilterAdminsSet and is used to iterate over the raw logs and unpacked data for AdminsSet events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderAdminsSetIterator struct {
	Event *IITeeOracleInstructionsSenderAdminsSet // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderAdminsSetIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderAdminsSet)
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
		it.Event = new(IITeeOracleInstructionsSenderAdminsSet)
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
func (it *IITeeOracleInstructionsSenderAdminsSetIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderAdminsSetIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderAdminsSet represents a AdminsSet event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderAdminsSet struct {
	FeedId     [21]byte
	TeeId      common.Address
	Version    uint64
	AdminsHash [32]byte
	Raw        types.Log // Blockchain specific contextual infos
}

// FilterAdminsSet is a free log retrieval operation binding the contract event 0x27db2c431e0455dd62dc45d449445882c9d8421b070ce500851306e16bd382c7.
//
// Solidity: event AdminsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 adminsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterAdminsSet(opts *bind.FilterOpts, feedId [][21]byte, teeId []common.Address, version []uint64) (*IITeeOracleInstructionsSenderAdminsSetIterator, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var teeIdRule []interface{}
	for _, teeIdItem := range teeId {
		teeIdRule = append(teeIdRule, teeIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "AdminsSet", feedIdRule, teeIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderAdminsSetIterator{contract: _IITeeOracleInstructionsSender.contract, event: "AdminsSet", logs: logs, sub: sub}, nil
}

// WatchAdminsSet is a free log subscription operation binding the contract event 0x27db2c431e0455dd62dc45d449445882c9d8421b070ce500851306e16bd382c7.
//
// Solidity: event AdminsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 adminsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchAdminsSet(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderAdminsSet, feedId [][21]byte, teeId []common.Address, version []uint64) (event.Subscription, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var teeIdRule []interface{}
	for _, teeIdItem := range teeId {
		teeIdRule = append(teeIdRule, teeIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "AdminsSet", feedIdRule, teeIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderAdminsSet)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "AdminsSet", log); err != nil {
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

// ParseAdminsSet is a log parse operation binding the contract event 0x27db2c431e0455dd62dc45d449445882c9d8421b070ce500851306e16bd382c7.
//
// Solidity: event AdminsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 adminsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseAdminsSet(log types.Log) (*IITeeOracleInstructionsSenderAdminsSet, error) {
	event := new(IITeeOracleInstructionsSenderAdminsSet)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "AdminsSet", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleInstructionsSenderEndpointsPublishedIterator is returned from FilterEndpointsPublished and is used to iterate over the raw logs and unpacked data for EndpointsPublished events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderEndpointsPublishedIterator struct {
	Event *IITeeOracleInstructionsSenderEndpointsPublished // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderEndpointsPublishedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderEndpointsPublished)
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
		it.Event = new(IITeeOracleInstructionsSenderEndpointsPublished)
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
func (it *IITeeOracleInstructionsSenderEndpointsPublishedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderEndpointsPublishedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderEndpointsPublished represents a EndpointsPublished event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderEndpointsPublished struct {
	FeedId        [21]byte
	Version       uint64
	EndpointsHash [32]byte
	Groups        []ITeeOracleInstructionsSenderEndpointGroup
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterEndpointsPublished is a free log retrieval operation binding the contract event 0x163a47b8f661ead2a9f06211d2fd0d00b5f458c6d4a98a63cb29b6e7bad91437.
//
// Solidity: event EndpointsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 endpointsHash, (bytes32,uint64,(uint8,string,bytes32,string)[])[] groups)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterEndpointsPublished(opts *bind.FilterOpts, feedId [][21]byte, version []uint64) (*IITeeOracleInstructionsSenderEndpointsPublishedIterator, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "EndpointsPublished", feedIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderEndpointsPublishedIterator{contract: _IITeeOracleInstructionsSender.contract, event: "EndpointsPublished", logs: logs, sub: sub}, nil
}

// WatchEndpointsPublished is a free log subscription operation binding the contract event 0x163a47b8f661ead2a9f06211d2fd0d00b5f458c6d4a98a63cb29b6e7bad91437.
//
// Solidity: event EndpointsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 endpointsHash, (bytes32,uint64,(uint8,string,bytes32,string)[])[] groups)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchEndpointsPublished(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderEndpointsPublished, feedId [][21]byte, version []uint64) (event.Subscription, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "EndpointsPublished", feedIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderEndpointsPublished)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "EndpointsPublished", log); err != nil {
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

// ParseEndpointsPublished is a log parse operation binding the contract event 0x163a47b8f661ead2a9f06211d2fd0d00b5f458c6d4a98a63cb29b6e7bad91437.
//
// Solidity: event EndpointsPublished(bytes21 indexed feedId, uint64 indexed version, bytes32 endpointsHash, (bytes32,uint64,(uint8,string,bytes32,string)[])[] groups)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseEndpointsPublished(log types.Log) (*IITeeOracleInstructionsSenderEndpointsPublished, error) {
	event := new(IITeeOracleInstructionsSenderEndpointsPublished)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "EndpointsPublished", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleInstructionsSenderEndpointsSetIterator is returned from FilterEndpointsSet and is used to iterate over the raw logs and unpacked data for EndpointsSet events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderEndpointsSetIterator struct {
	Event *IITeeOracleInstructionsSenderEndpointsSet // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderEndpointsSetIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderEndpointsSet)
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
		it.Event = new(IITeeOracleInstructionsSenderEndpointsSet)
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
func (it *IITeeOracleInstructionsSenderEndpointsSetIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderEndpointsSetIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderEndpointsSet represents a EndpointsSet event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderEndpointsSet struct {
	FeedId        [21]byte
	TeeId         common.Address
	Version       uint64
	EndpointsHash [32]byte
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterEndpointsSet is a free log retrieval operation binding the contract event 0x98a582feb91e992d17e45996e7576e7f177889b4a514a815574c8d277fc7394f.
//
// Solidity: event EndpointsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 endpointsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterEndpointsSet(opts *bind.FilterOpts, feedId [][21]byte, teeId []common.Address, version []uint64) (*IITeeOracleInstructionsSenderEndpointsSetIterator, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var teeIdRule []interface{}
	for _, teeIdItem := range teeId {
		teeIdRule = append(teeIdRule, teeIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "EndpointsSet", feedIdRule, teeIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderEndpointsSetIterator{contract: _IITeeOracleInstructionsSender.contract, event: "EndpointsSet", logs: logs, sub: sub}, nil
}

// WatchEndpointsSet is a free log subscription operation binding the contract event 0x98a582feb91e992d17e45996e7576e7f177889b4a514a815574c8d277fc7394f.
//
// Solidity: event EndpointsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 endpointsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchEndpointsSet(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderEndpointsSet, feedId [][21]byte, teeId []common.Address, version []uint64) (event.Subscription, error) {

	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var teeIdRule []interface{}
	for _, teeIdItem := range teeId {
		teeIdRule = append(teeIdRule, teeIdItem)
	}
	var versionRule []interface{}
	for _, versionItem := range version {
		versionRule = append(versionRule, versionItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "EndpointsSet", feedIdRule, teeIdRule, versionRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderEndpointsSet)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "EndpointsSet", log); err != nil {
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

// ParseEndpointsSet is a log parse operation binding the contract event 0x98a582feb91e992d17e45996e7576e7f177889b4a514a815574c8d277fc7394f.
//
// Solidity: event EndpointsSet(bytes21 indexed feedId, address indexed teeId, uint64 indexed version, bytes32 endpointsHash)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseEndpointsSet(log types.Log) (*IITeeOracleInstructionsSenderEndpointsSet, error) {
	event := new(IITeeOracleInstructionsSenderEndpointsSet)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "EndpointsSet", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleInstructionsSenderFeedUpdateRequestedIterator is returned from FilterFeedUpdateRequested and is used to iterate over the raw logs and unpacked data for FeedUpdateRequested events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderFeedUpdateRequestedIterator struct {
	Event *IITeeOracleInstructionsSenderFeedUpdateRequested // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderFeedUpdateRequestedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderFeedUpdateRequested)
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
		it.Event = new(IITeeOracleInstructionsSenderFeedUpdateRequested)
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
func (it *IITeeOracleInstructionsSenderFeedUpdateRequestedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderFeedUpdateRequestedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderFeedUpdateRequested represents a FeedUpdateRequested event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderFeedUpdateRequested struct {
	Requester     common.Address
	FeedId        [21]byte
	InstructionId [32]byte
	Raw           types.Log // Blockchain specific contextual infos
}

// FilterFeedUpdateRequested is a free log retrieval operation binding the contract event 0x8ff920ae2d28487643047e9c261bf810d85c01ddddf721cb038baafe9190fe9e.
//
// Solidity: event FeedUpdateRequested(address indexed requester, bytes21 indexed feedId, bytes32 indexed instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterFeedUpdateRequested(opts *bind.FilterOpts, requester []common.Address, feedId [][21]byte, instructionId [][32]byte) (*IITeeOracleInstructionsSenderFeedUpdateRequestedIterator, error) {

	var requesterRule []interface{}
	for _, requesterItem := range requester {
		requesterRule = append(requesterRule, requesterItem)
	}
	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var instructionIdRule []interface{}
	for _, instructionIdItem := range instructionId {
		instructionIdRule = append(instructionIdRule, instructionIdItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "FeedUpdateRequested", requesterRule, feedIdRule, instructionIdRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderFeedUpdateRequestedIterator{contract: _IITeeOracleInstructionsSender.contract, event: "FeedUpdateRequested", logs: logs, sub: sub}, nil
}

// WatchFeedUpdateRequested is a free log subscription operation binding the contract event 0x8ff920ae2d28487643047e9c261bf810d85c01ddddf721cb038baafe9190fe9e.
//
// Solidity: event FeedUpdateRequested(address indexed requester, bytes21 indexed feedId, bytes32 indexed instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchFeedUpdateRequested(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderFeedUpdateRequested, requester []common.Address, feedId [][21]byte, instructionId [][32]byte) (event.Subscription, error) {

	var requesterRule []interface{}
	for _, requesterItem := range requester {
		requesterRule = append(requesterRule, requesterItem)
	}
	var feedIdRule []interface{}
	for _, feedIdItem := range feedId {
		feedIdRule = append(feedIdRule, feedIdItem)
	}
	var instructionIdRule []interface{}
	for _, instructionIdItem := range instructionId {
		instructionIdRule = append(instructionIdRule, instructionIdItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "FeedUpdateRequested", requesterRule, feedIdRule, instructionIdRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderFeedUpdateRequested)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "FeedUpdateRequested", log); err != nil {
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

// ParseFeedUpdateRequested is a log parse operation binding the contract event 0x8ff920ae2d28487643047e9c261bf810d85c01ddddf721cb038baafe9190fe9e.
//
// Solidity: event FeedUpdateRequested(address indexed requester, bytes21 indexed feedId, bytes32 indexed instructionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseFeedUpdateRequested(log types.Log) (*IITeeOracleInstructionsSenderFeedUpdateRequested, error) {
	event := new(IITeeOracleInstructionsSenderFeedUpdateRequested)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "FeedUpdateRequested", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}

// IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator is returned from FilterInstructionsSenderInitialised and is used to iterate over the raw logs and unpacked data for InstructionsSenderInitialised events raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator struct {
	Event *IITeeOracleInstructionsSenderInstructionsSenderInitialised // Event containing the contract specifics and raw log

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
func (it *IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator) Next() bool {
	// If the iterator failed, stop iterating
	if it.fail != nil {
		return false
	}
	// If the iterator completed, deliver directly whatever's available
	if it.done {
		select {
		case log := <-it.logs:
			it.Event = new(IITeeOracleInstructionsSenderInstructionsSenderInitialised)
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
		it.Event = new(IITeeOracleInstructionsSenderInstructionsSenderInitialised)
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
func (it *IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator) Error() error {
	return it.fail
}

// Close terminates the iteration process, releasing any pending underlying
// resources.
func (it *IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator) Close() error {
	it.sub.Unsubscribe()
	return nil
}

// IITeeOracleInstructionsSenderInstructionsSenderInitialised represents a InstructionsSenderInitialised event raised by the IITeeOracleInstructionsSender contract.
type IITeeOracleInstructionsSenderInstructionsSenderInitialised struct {
	ExtensionId *big.Int
	Raw         types.Log // Blockchain specific contextual infos
}

// FilterInstructionsSenderInitialised is a free log retrieval operation binding the contract event 0xaaf37a7a784420c471f47055af098d7623b77adf0fc52da83f842b971a9e5f48.
//
// Solidity: event InstructionsSenderInitialised(uint256 indexed extensionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) FilterInstructionsSenderInitialised(opts *bind.FilterOpts, extensionId []*big.Int) (*IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator, error) {

	var extensionIdRule []interface{}
	for _, extensionIdItem := range extensionId {
		extensionIdRule = append(extensionIdRule, extensionIdItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.FilterLogs(opts, "InstructionsSenderInitialised", extensionIdRule)
	if err != nil {
		return nil, err
	}
	return &IITeeOracleInstructionsSenderInstructionsSenderInitialisedIterator{contract: _IITeeOracleInstructionsSender.contract, event: "InstructionsSenderInitialised", logs: logs, sub: sub}, nil
}

// WatchInstructionsSenderInitialised is a free log subscription operation binding the contract event 0xaaf37a7a784420c471f47055af098d7623b77adf0fc52da83f842b971a9e5f48.
//
// Solidity: event InstructionsSenderInitialised(uint256 indexed extensionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) WatchInstructionsSenderInitialised(opts *bind.WatchOpts, sink chan<- *IITeeOracleInstructionsSenderInstructionsSenderInitialised, extensionId []*big.Int) (event.Subscription, error) {

	var extensionIdRule []interface{}
	for _, extensionIdItem := range extensionId {
		extensionIdRule = append(extensionIdRule, extensionIdItem)
	}

	logs, sub, err := _IITeeOracleInstructionsSender.contract.WatchLogs(opts, "InstructionsSenderInitialised", extensionIdRule)
	if err != nil {
		return nil, err
	}
	return event.NewSubscription(func(quit <-chan struct{}) error {
		defer sub.Unsubscribe()
		for {
			select {
			case log := <-logs:
				// New log arrived, parse the event and forward to the user
				event := new(IITeeOracleInstructionsSenderInstructionsSenderInitialised)
				if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "InstructionsSenderInitialised", log); err != nil {
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

// ParseInstructionsSenderInitialised is a log parse operation binding the contract event 0xaaf37a7a784420c471f47055af098d7623b77adf0fc52da83f842b971a9e5f48.
//
// Solidity: event InstructionsSenderInitialised(uint256 indexed extensionId)
func (_IITeeOracleInstructionsSender *IITeeOracleInstructionsSenderFilterer) ParseInstructionsSenderInitialised(log types.Log) (*IITeeOracleInstructionsSenderInstructionsSenderInitialised, error) {
	event := new(IITeeOracleInstructionsSenderInstructionsSenderInitialised)
	if err := _IITeeOracleInstructionsSender.contract.UnpackLog(event, "InstructionsSenderInitialised", log); err != nil {
		return nil, err
	}
	event.Raw = log
	return event, nil
}
