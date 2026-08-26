// Code generated - DO NOT EDIT.
// This file is a generated binding and any manual changes will be lost.

package teeoraclestructs

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

// ITeeOracleInstructionsSenderAdminRole is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderAdminRole struct {
	Role      [32]byte
	Threshold uint64
	Admins    []common.Address
}

// ITeeOracleInstructionsSenderAdmins is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderAdmins struct {
	Version uint64
	FeedId  [21]byte
	Roles   []ITeeOracleInstructionsSenderAdminRole
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

// ITeeOracleInstructionsSenderEndpoints is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderEndpoints struct {
	Version uint64
	FeedId  [21]byte
	Groups  []ITeeOracleInstructionsSenderEndpointGroup
}

// ITeeOracleInstructionsSenderFeedUpdateRequest is an auto generated low-level Go binding around an user-defined struct.
type ITeeOracleInstructionsSenderFeedUpdateRequest struct {
	FeedId [21]byte
}

// TeeOracleStructsMetaData contains all meta data concerning the TeeOracleStructs contract.
var TeeOracleStructsMetaData = &bind.MetaData{
	ABI: "[{\"type\":\"function\",\"name\":\"adminRoleStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.AdminRole\",\"components\":[{\"name\":\"role\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"admins\",\"type\":\"address[]\",\"internalType\":\"address[]\"}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"adminsStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.Admins\",\"components\":[{\"name\":\"version\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"roles\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.AdminRole[]\",\"components\":[{\"name\":\"role\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"admins\",\"type\":\"address[]\",\"internalType\":\"address[]\"}]}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"endpointGroupStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.EndpointGroup\",\"components\":[{\"name\":\"group\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpoints\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint[]\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"endpointStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"endpointsStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoints\",\"components\":[{\"name\":\"version\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"groups\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.EndpointGroup[]\",\"components\":[{\"name\":\"group\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"threshold\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpoints\",\"type\":\"tuple[]\",\"internalType\":\"structITeeOracleInstructionsSender.Endpoint[]\",\"components\":[{\"name\":\"kind\",\"type\":\"uint8\",\"internalType\":\"enumITeeOracleInstructionsSender.EndpointKind\"},{\"name\":\"url\",\"type\":\"string\",\"internalType\":\"string\"},{\"name\":\"urlHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"secretRef\",\"type\":\"string\",\"internalType\":\"string\"}]}]}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"feedUpdateRequestStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleInstructionsSender.FeedUpdateRequest\",\"components\":[{\"name\":\"feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"},{\"type\":\"function\",\"name\":\"feedUpdateStruct\",\"inputs\":[{\"name\":\"\",\"type\":\"tuple\",\"internalType\":\"structITeeOracleFeedStore.FeedUpdate\",\"components\":[{\"name\":\"extensionId\",\"type\":\"uint256\",\"internalType\":\"uint256\"},{\"name\":\"feedId\",\"type\":\"bytes21\",\"internalType\":\"bytes21\"},{\"name\":\"value\",\"type\":\"int32\",\"internalType\":\"int32\"},{\"name\":\"decimals\",\"type\":\"int8\",\"internalType\":\"int8\"},{\"name\":\"observedAt\",\"type\":\"uint64\",\"internalType\":\"uint64\"},{\"name\":\"endpointsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"},{\"name\":\"adminsHash\",\"type\":\"bytes32\",\"internalType\":\"bytes32\"}]}],\"outputs\":[],\"stateMutability\":\"nonpayable\"}]",
}

// TeeOracleStructsABI is the input ABI used to generate the binding from.
// Deprecated: Use TeeOracleStructsMetaData.ABI instead.
var TeeOracleStructsABI = TeeOracleStructsMetaData.ABI

// TeeOracleStructs is an auto generated Go binding around an Ethereum contract.
type TeeOracleStructs struct {
	TeeOracleStructsCaller     // Read-only binding to the contract
	TeeOracleStructsTransactor // Write-only binding to the contract
	TeeOracleStructsFilterer   // Log filterer for contract events
}

// TeeOracleStructsCaller is an auto generated read-only Go binding around an Ethereum contract.
type TeeOracleStructsCaller struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// TeeOracleStructsTransactor is an auto generated write-only Go binding around an Ethereum contract.
type TeeOracleStructsTransactor struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// TeeOracleStructsFilterer is an auto generated log filtering Go binding around an Ethereum contract events.
type TeeOracleStructsFilterer struct {
	contract *bind.BoundContract // Generic contract wrapper for the low level calls
}

// TeeOracleStructsSession is an auto generated Go binding around an Ethereum contract,
// with pre-set call and transact options.
type TeeOracleStructsSession struct {
	Contract     *TeeOracleStructs // Generic contract binding to set the session for
	CallOpts     bind.CallOpts     // Call options to use throughout this session
	TransactOpts bind.TransactOpts // Transaction auth options to use throughout this session
}

// TeeOracleStructsCallerSession is an auto generated read-only Go binding around an Ethereum contract,
// with pre-set call options.
type TeeOracleStructsCallerSession struct {
	Contract *TeeOracleStructsCaller // Generic contract caller binding to set the session for
	CallOpts bind.CallOpts           // Call options to use throughout this session
}

// TeeOracleStructsTransactorSession is an auto generated write-only Go binding around an Ethereum contract,
// with pre-set transact options.
type TeeOracleStructsTransactorSession struct {
	Contract     *TeeOracleStructsTransactor // Generic contract transactor binding to set the session for
	TransactOpts bind.TransactOpts           // Transaction auth options to use throughout this session
}

// TeeOracleStructsRaw is an auto generated low-level Go binding around an Ethereum contract.
type TeeOracleStructsRaw struct {
	Contract *TeeOracleStructs // Generic contract binding to access the raw methods on
}

// TeeOracleStructsCallerRaw is an auto generated low-level read-only Go binding around an Ethereum contract.
type TeeOracleStructsCallerRaw struct {
	Contract *TeeOracleStructsCaller // Generic read-only contract binding to access the raw methods on
}

// TeeOracleStructsTransactorRaw is an auto generated low-level write-only Go binding around an Ethereum contract.
type TeeOracleStructsTransactorRaw struct {
	Contract *TeeOracleStructsTransactor // Generic write-only contract binding to access the raw methods on
}

// NewTeeOracleStructs creates a new instance of TeeOracleStructs, bound to a specific deployed contract.
func NewTeeOracleStructs(address common.Address, backend bind.ContractBackend) (*TeeOracleStructs, error) {
	contract, err := bindTeeOracleStructs(address, backend, backend, backend)
	if err != nil {
		return nil, err
	}
	return &TeeOracleStructs{TeeOracleStructsCaller: TeeOracleStructsCaller{contract: contract}, TeeOracleStructsTransactor: TeeOracleStructsTransactor{contract: contract}, TeeOracleStructsFilterer: TeeOracleStructsFilterer{contract: contract}}, nil
}

// NewTeeOracleStructsCaller creates a new read-only instance of TeeOracleStructs, bound to a specific deployed contract.
func NewTeeOracleStructsCaller(address common.Address, caller bind.ContractCaller) (*TeeOracleStructsCaller, error) {
	contract, err := bindTeeOracleStructs(address, caller, nil, nil)
	if err != nil {
		return nil, err
	}
	return &TeeOracleStructsCaller{contract: contract}, nil
}

// NewTeeOracleStructsTransactor creates a new write-only instance of TeeOracleStructs, bound to a specific deployed contract.
func NewTeeOracleStructsTransactor(address common.Address, transactor bind.ContractTransactor) (*TeeOracleStructsTransactor, error) {
	contract, err := bindTeeOracleStructs(address, nil, transactor, nil)
	if err != nil {
		return nil, err
	}
	return &TeeOracleStructsTransactor{contract: contract}, nil
}

// NewTeeOracleStructsFilterer creates a new log filterer instance of TeeOracleStructs, bound to a specific deployed contract.
func NewTeeOracleStructsFilterer(address common.Address, filterer bind.ContractFilterer) (*TeeOracleStructsFilterer, error) {
	contract, err := bindTeeOracleStructs(address, nil, nil, filterer)
	if err != nil {
		return nil, err
	}
	return &TeeOracleStructsFilterer{contract: contract}, nil
}

// bindTeeOracleStructs binds a generic wrapper to an already deployed contract.
func bindTeeOracleStructs(address common.Address, caller bind.ContractCaller, transactor bind.ContractTransactor, filterer bind.ContractFilterer) (*bind.BoundContract, error) {
	parsed, err := TeeOracleStructsMetaData.GetAbi()
	if err != nil {
		return nil, err
	}
	return bind.NewBoundContract(address, *parsed, caller, transactor, filterer), nil
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_TeeOracleStructs *TeeOracleStructsRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _TeeOracleStructs.Contract.TeeOracleStructsCaller.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_TeeOracleStructs *TeeOracleStructsRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.TeeOracleStructsTransactor.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_TeeOracleStructs *TeeOracleStructsRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.TeeOracleStructsTransactor.contract.Transact(opts, method, params...)
}

// Call invokes the (constant) contract method with params as input values and
// sets the output to result. The result type might be a single field for simple
// returns, a slice of interfaces for anonymous returns and a struct for named
// returns.
func (_TeeOracleStructs *TeeOracleStructsCallerRaw) Call(opts *bind.CallOpts, result *[]interface{}, method string, params ...interface{}) error {
	return _TeeOracleStructs.Contract.contract.Call(opts, result, method, params...)
}

// Transfer initiates a plain transaction to move funds to the contract, calling
// its default method if one is available.
func (_TeeOracleStructs *TeeOracleStructsTransactorRaw) Transfer(opts *bind.TransactOpts) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.contract.Transfer(opts)
}

// Transact invokes the (paid) contract method with params as input values.
func (_TeeOracleStructs *TeeOracleStructsTransactorRaw) Transact(opts *bind.TransactOpts, method string, params ...interface{}) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.contract.Transact(opts, method, params...)
}

// AdminRoleStruct is a paid mutator transaction binding the contract method 0x59473501.
//
// Solidity: function adminRoleStruct((bytes32,uint64,address[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) AdminRoleStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "adminRoleStruct", arg0)
}

// AdminRoleStruct is a paid mutator transaction binding the contract method 0x59473501.
//
// Solidity: function adminRoleStruct((bytes32,uint64,address[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) AdminRoleStruct(arg0 ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.AdminRoleStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// AdminRoleStruct is a paid mutator transaction binding the contract method 0x59473501.
//
// Solidity: function adminRoleStruct((bytes32,uint64,address[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) AdminRoleStruct(arg0 ITeeOracleInstructionsSenderAdminRole) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.AdminRoleStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// AdminsStruct is a paid mutator transaction binding the contract method 0x975009e1.
//
// Solidity: function adminsStruct((uint64,bytes21,(bytes32,uint64,address[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) AdminsStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderAdmins) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "adminsStruct", arg0)
}

// AdminsStruct is a paid mutator transaction binding the contract method 0x975009e1.
//
// Solidity: function adminsStruct((uint64,bytes21,(bytes32,uint64,address[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) AdminsStruct(arg0 ITeeOracleInstructionsSenderAdmins) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.AdminsStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// AdminsStruct is a paid mutator transaction binding the contract method 0x975009e1.
//
// Solidity: function adminsStruct((uint64,bytes21,(bytes32,uint64,address[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) AdminsStruct(arg0 ITeeOracleInstructionsSenderAdmins) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.AdminsStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointGroupStruct is a paid mutator transaction binding the contract method 0xa1cac938.
//
// Solidity: function endpointGroupStruct((bytes32,uint64,(uint8,string,bytes32,string)[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) EndpointGroupStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "endpointGroupStruct", arg0)
}

// EndpointGroupStruct is a paid mutator transaction binding the contract method 0xa1cac938.
//
// Solidity: function endpointGroupStruct((bytes32,uint64,(uint8,string,bytes32,string)[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) EndpointGroupStruct(arg0 ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointGroupStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointGroupStruct is a paid mutator transaction binding the contract method 0xa1cac938.
//
// Solidity: function endpointGroupStruct((bytes32,uint64,(uint8,string,bytes32,string)[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) EndpointGroupStruct(arg0 ITeeOracleInstructionsSenderEndpointGroup) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointGroupStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointStruct is a paid mutator transaction binding the contract method 0xd5d7aadc.
//
// Solidity: function endpointStruct((uint8,string,bytes32,string) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) EndpointStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderEndpoint) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "endpointStruct", arg0)
}

// EndpointStruct is a paid mutator transaction binding the contract method 0xd5d7aadc.
//
// Solidity: function endpointStruct((uint8,string,bytes32,string) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) EndpointStruct(arg0 ITeeOracleInstructionsSenderEndpoint) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointStruct is a paid mutator transaction binding the contract method 0xd5d7aadc.
//
// Solidity: function endpointStruct((uint8,string,bytes32,string) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) EndpointStruct(arg0 ITeeOracleInstructionsSenderEndpoint) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointsStruct is a paid mutator transaction binding the contract method 0x3d15562d.
//
// Solidity: function endpointsStruct((uint64,bytes21,(bytes32,uint64,(uint8,string,bytes32,string)[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) EndpointsStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderEndpoints) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "endpointsStruct", arg0)
}

// EndpointsStruct is a paid mutator transaction binding the contract method 0x3d15562d.
//
// Solidity: function endpointsStruct((uint64,bytes21,(bytes32,uint64,(uint8,string,bytes32,string)[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) EndpointsStruct(arg0 ITeeOracleInstructionsSenderEndpoints) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointsStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// EndpointsStruct is a paid mutator transaction binding the contract method 0x3d15562d.
//
// Solidity: function endpointsStruct((uint64,bytes21,(bytes32,uint64,(uint8,string,bytes32,string)[])[]) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) EndpointsStruct(arg0 ITeeOracleInstructionsSenderEndpoints) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.EndpointsStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// FeedUpdateRequestStruct is a paid mutator transaction binding the contract method 0x5e79f61a.
//
// Solidity: function feedUpdateRequestStruct((bytes21) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) FeedUpdateRequestStruct(opts *bind.TransactOpts, arg0 ITeeOracleInstructionsSenderFeedUpdateRequest) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "feedUpdateRequestStruct", arg0)
}

// FeedUpdateRequestStruct is a paid mutator transaction binding the contract method 0x5e79f61a.
//
// Solidity: function feedUpdateRequestStruct((bytes21) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) FeedUpdateRequestStruct(arg0 ITeeOracleInstructionsSenderFeedUpdateRequest) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.FeedUpdateRequestStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// FeedUpdateRequestStruct is a paid mutator transaction binding the contract method 0x5e79f61a.
//
// Solidity: function feedUpdateRequestStruct((bytes21) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) FeedUpdateRequestStruct(arg0 ITeeOracleInstructionsSenderFeedUpdateRequest) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.FeedUpdateRequestStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// FeedUpdateStruct is a paid mutator transaction binding the contract method 0xa3a5b913.
//
// Solidity: function feedUpdateStruct((uint256,bytes21,int32,int8,uint64,bytes32,bytes32) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactor) FeedUpdateStruct(opts *bind.TransactOpts, arg0 ITeeOracleFeedStoreFeedUpdate) (*types.Transaction, error) {
	return _TeeOracleStructs.contract.Transact(opts, "feedUpdateStruct", arg0)
}

// FeedUpdateStruct is a paid mutator transaction binding the contract method 0xa3a5b913.
//
// Solidity: function feedUpdateStruct((uint256,bytes21,int32,int8,uint64,bytes32,bytes32) ) returns()
func (_TeeOracleStructs *TeeOracleStructsSession) FeedUpdateStruct(arg0 ITeeOracleFeedStoreFeedUpdate) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.FeedUpdateStruct(&_TeeOracleStructs.TransactOpts, arg0)
}

// FeedUpdateStruct is a paid mutator transaction binding the contract method 0xa3a5b913.
//
// Solidity: function feedUpdateStruct((uint256,bytes21,int32,int8,uint64,bytes32,bytes32) ) returns()
func (_TeeOracleStructs *TeeOracleStructsTransactorSession) FeedUpdateStruct(arg0 ITeeOracleFeedStoreFeedUpdate) (*types.Transaction, error) {
	return _TeeOracleStructs.Contract.FeedUpdateStruct(&_TeeOracleStructs.TransactOpts, arg0)
}
