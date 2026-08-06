// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

// TODO: Replace local interfaces with imports from flare-smart-contracts-v2 once published as a package.
import { ITeeExtensionRegistry } from "./interfaces/ITeeExtensionRegistry.sol";
import { ITeeMachineRegistry } from "./interfaces/ITeeMachineRegistry.sol";

/// @title UsdxOracleInstructionSender
/// @author Flare Foundation
/// @notice On-chain entry point for USDX oracle instructions to the TEE.
/// @dev DO NOT MODIFY: constructor, setExtensionId(), _getExtensionId()
contract UsdxOracleInstructionSender {
    /// @notice Operation type for oracle price requests.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_TYPE_USDX_ORACLE = bytes32("USDX_ORACLE");

    /// @notice Command for GET_PRICE.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_COMMAND_GET_PRICE = bytes32("GET_PRICE");

    /// @notice Operation type for extension configuration.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_TYPE_USDX_CONFIG = bytes32("USDX_CONFIG");

    /// @notice Command for SET_ENDPOINTS.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_COMMAND_SET_ENDPOINTS = bytes32("SET_ENDPOINTS");

    /// @notice Maximum endpoint configuration payload size.
    uint256 public constant MAX_ENDPOINTS_BYTES = 16384;

    /// @notice Request envelope version carried by every GET_PRICE instruction.
    /// @dev The registry rejects an empty `message` with `MessageEmpty()`, so a
    /// price request cannot carry nothing — found on Coston2, 5 Aug 2026, the
    /// first time this path ran on-chain. A single version byte is the smallest
    /// payload that satisfies it while staying checkable: the extension requires
    /// exactly this value and rejects anything else, so no caller can come to
    /// depend on a field the extension silently drops. Bump it when the request
    /// envelope changes; the extension pins the same constant.
    bytes public constant GET_PRICE_REQUEST_V1 = hex"01";

    /// @notice TEE extension registry.
    ITeeExtensionRegistry public immutable TEE_EXTENSION_REGISTRY;
    /// @notice TEE machine registry.
    ITeeMachineRegistry public immutable TEE_MACHINE_REGISTRY;

    uint256 private constant FIRST_PUBLIC_EXTENSION_ID = 0x10000; // 65536

    uint256 private _extensionId;

    /// @notice keccak256 of the last published endpoint config per machine; UsdxPriceStore enforces this.
    mapping(address => bytes32) public expectedConfigHash;

    /// @notice Owner-only endpoint configuration; also controls price sources (no host allowlist).
    address public owner;

    /// @notice Emitted when a price update is requested.
    /// @dev Carries the instruction id so a keeper can fetch the result from the
    /// TEE proxy without parsing the registry's own event out of the receipt.
    event PriceUpdateRequested(address indexed requester, bytes32 indexed instructionId);

    /// @notice Emitted when endpoint configuration is sent to a TEE machine.
    event EndpointsSet(address indexed teeId, bytes32 indexed configHash);

    /// @notice Emitted when ownership is transferred.
    event OwnershipTransferred(address indexed from, address indexed to);

    error NotOwner();
    error ZeroTeeId();
    error EmptyConfig();
    error ConfigTooLarge();
    error ZeroOwner();

    modifier onlyOwner() {
        if (msg.sender != owner) revert NotOwner();
        _;
    }

    /// @notice Initializes the contract with registry addresses; deployer becomes owner.
    constructor(
        ITeeExtensionRegistry _teeExtensionRegistry,
        ITeeMachineRegistry _teeMachineRegistry
    ) {
        require(address(_teeExtensionRegistry) != address(0), "TeeExtensionRegistry cannot be zero address");
        require(address(_teeMachineRegistry) != address(0), "TeeMachineRegistry cannot be zero address");
        require(address(_teeExtensionRegistry).code.length > 0, "TeeExtensionRegistry has no code");
        require(address(_teeMachineRegistry).code.length > 0, "TeeMachineRegistry has no code");
        TEE_EXTENSION_REGISTRY = _teeExtensionRegistry;
        TEE_MACHINE_REGISTRY = _teeMachineRegistry;

        owner = msg.sender;
        emit OwnershipTransferred(address(0), msg.sender);
    }

    /// @notice Transfers configuration ownership.
    function transferOwnership(address _owner) external onlyOwner {
        if (_owner == address(0)) revert ZeroOwner();
        emit OwnershipTransferred(owner, _owner);
        owner = _owner;
    }

    /// @notice Publishes endpoint configuration to one specific TEE machine.
    /// @dev Owner-only, no credentials on-chain, one call per machine. Hash recorded before dispatch.
    function setEndpoints(address _teeId, bytes calldata _config) external payable onlyOwner {
        if (_teeId == address(0)) revert ZeroTeeId();
        if (_config.length == 0) revert EmptyConfig();
        if (_config.length > MAX_ENDPOINTS_BYTES) revert ConfigTooLarge();

        address[] memory teeIds = new address[](1);
        teeIds[0] = _teeId;
        address[] memory cosigners = new address[](0);

        ITeeExtensionRegistry.TeeInstructionParams memory params = ITeeExtensionRegistry.TeeInstructionParams({
            opType: OP_TYPE_USDX_CONFIG,
            opCommand: OP_COMMAND_SET_ENDPOINTS,
            message: _config,
            cosigners: cosigners,
            cosignersThreshold: 0,
            claimBackAddress: msg.sender
        });

        // Set expected hash before dispatch so undelivered instructions block stale submissions
        bytes32 configHash = keccak256(_config);
        expectedConfigHash[_teeId] = configHash;

        TEE_EXTENSION_REGISTRY.sendInstructions{value: msg.value}(teeIds, params);

        emit EndpointsSet(_teeId, configHash);
    }

    /// @notice Finds and sets this contract's extension id. Can only be set once.
    /// @dev DO NOT MODIFY this function.
    function setExtensionId() external {
        require(_extensionId == 0, "Extension ID already set.");

        uint256 c = TEE_EXTENSION_REGISTRY.nextPublicExtensionId();
        for (uint256 i = FIRST_PUBLIC_EXTENSION_ID; i < c; ++i) {
            if (TEE_EXTENSION_REGISTRY.getTeeExtensionInstructionsSender(i) == address(this)) {
                _extensionId = i;
                return;
            }
        }
        revert("Extension ID not found.");
    }

    /// @notice Requests a fresh price observation from a random registered TEE machine.
    /// @dev Open to anyone; registry fee bounds spam, price store verifies the TEE signature.
    /// The observation needs nothing from the caller, so the message is the fixed
    /// `GET_PRICE_REQUEST_V1` envelope rather than caller-supplied data.
    /// @return _instructionId Id of the dispatched instruction, also emitted in
    /// `PriceUpdateRequested`. The return value serves contract callers; the event
    /// is what an off-chain keeper reads, since a transaction returns nothing to it.
    function requestPriceUpdate() external payable returns (bytes32 _instructionId) {
        address[] memory teeIds = TEE_MACHINE_REGISTRY.getRandomTeeIds(_getExtensionId(), 1);
        address[] memory cosigners = new address[](0);

        ITeeExtensionRegistry.TeeInstructionParams memory params = ITeeExtensionRegistry.TeeInstructionParams({
            opType: OP_TYPE_USDX_ORACLE,
            opCommand: OP_COMMAND_GET_PRICE,
            message: GET_PRICE_REQUEST_V1,
            cosigners: cosigners,
            cosignersThreshold: 0,
            claimBackAddress: msg.sender
        });

        _instructionId = TEE_EXTENSION_REGISTRY.sendInstructions{value: msg.value}(
            teeIds,
            params
        );

        emit PriceUpdateRequested(msg.sender, _instructionId);
    }

    /// @notice Returns the cached extension ID, reverting if not yet set.
    function _getExtensionId() internal view returns (uint256) {
        require(_extensionId != 0, "Extension ID is not set.");
        return _extensionId;
    }
}
