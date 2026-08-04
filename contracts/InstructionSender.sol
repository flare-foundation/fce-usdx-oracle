// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

// TODO: Replace local interfaces with imports from flare-smart-contracts-v2 once published as a package.
import { ITeeExtensionRegistry } from "./interfaces/ITeeExtensionRegistry.sol";
import { ITeeMachineRegistry } from "./interfaces/ITeeMachineRegistry.sol";

/// @title UsdxOracleInstructionSender
/// @author Flare Foundation
/// @notice USDX price oracle — on-chain entry point for sending instructions to the TEE.
///
/// DO NOT MODIFY: constructor, setExtensionId(), _getExtensionId()
contract UsdxOracleInstructionSender {
    /// @notice Operation type for USDX oracle actions.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_TYPE_USDX_ORACLE = bytes32("USDX_ORACLE");

    /// @notice Command for the GET_PRICE action.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_COMMAND_GET_PRICE = bytes32("GET_PRICE");

    /// @notice Operation type for extension configuration.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_TYPE_USDX_CONFIG = bytes32("USDX_CONFIG");

    /// @notice Command for the SET_ENDPOINTS action.
    // forge-lint: disable-next-line(unsafe-typecast)
    bytes32 public constant OP_COMMAND_SET_ENDPOINTS = bytes32("SET_ENDPOINTS");

    /// @notice Upper bound on an endpoint configuration payload.
    /// @dev The real configuration is a few kilobytes. This bounds the gas an
    /// owner can burn by accident and keeps one instruction from carrying an
    /// unreasonable message.
    uint256 public constant MAX_ENDPOINTS_BYTES = 16384;

    /// @notice Reference to the TEE extension registry contract.
    ITeeExtensionRegistry public immutable TEE_EXTENSION_REGISTRY;
    /// @notice Reference to the TEE machine registry contract.
    ITeeMachineRegistry public immutable TEE_MACHINE_REGISTRY;

    /// @notice First public extension ID. The registry reserves IDs below this
    /// for system/reserved extensions; public extensions are assigned from here up.
    uint256 private constant FIRST_PUBLIC_EXTENSION_ID = 0x10000; // 65536

    uint256 private _extensionId;

    /// @notice keccak256 of the endpoint configuration last published for each
    /// TEE machine.
    ///
    /// @dev UsdxPriceStore reads this and refuses a price whose signed report
    /// commits to a different configuration. That is what makes the on-chain
    /// config binding rather than advisory: without it, this contract records
    /// what was *sent* while a machine might be running something else — a
    /// rejected payload, an undelivered instruction, or, with several machines,
    /// one that simply never received its call.
    mapping(address => bytes32) public expectedConfigHash;

    /// @notice Address permitted to configure endpoints.
    /// @dev Set to the deployer. Endpoint configuration is owner-only because
    /// an open setter is a denial-of-service hole: anyone could repoint the
    /// extension's data sources, and the extension would fail closed on every
    /// observation until the real configuration was restored.
    ///
    /// The owner is also price-controlling, by decision: there is no host
    /// allowlist, so whoever holds this key can name any https host and any
    /// threshold, and the attested code will faithfully sign whatever those
    /// sources return (findings doc §15). Consumers must trust this key as
    /// well as the TEE.
    address public owner;

    /// @notice Emitted when a price update is requested, for keeper/monitoring correlation.
    /// @param requester The account that paid for and triggered the instruction.
    event PriceUpdateRequested(address indexed requester);

    /// @notice Emitted when endpoint configuration is sent to a TEE machine.
    /// @param teeId The machine the configuration was addressed to.
    /// @param configHash keccak256 of the configuration payload, so an observer
    /// can confirm which configuration was published without re-reading calldata.
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

    /// @notice Initializes the contract with registry addresses.
    /// @param _teeExtensionRegistry Address of the TEE extension registry.
    /// @param _teeMachineRegistry Address of the TEE machine registry.
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

        // The deployer owns configuration. Taking it from msg.sender rather than
        // a constructor argument keeps the deployment tooling unchanged; transfer
        // it explicitly afterwards if the deployer is not the intended owner.
        owner = msg.sender;
        emit OwnershipTransferred(address(0), msg.sender);
    }

    /// @notice Transfers configuration ownership.
    function transferOwnership(address _owner) external onlyOwner {
        if (_owner == address(0)) revert ZeroOwner();
        emit OwnershipTransferred(owner, _owner);
        owner = _owner;
    }

    /// @notice Sends an endpoint configuration to one TEE machine.
    ///
    /// @param _teeId The machine to configure.
    /// @param _config The configuration payload, delivered verbatim to the
    /// extension as the instruction message.
    ///
    /// @dev The machine is named EXPLICITLY rather than drawn from
    /// getRandomTeeIds(): configuration must reach a specific machine, and a
    /// random pick would configure one at random and leave the rest stale.
    /// Running N machines means N calls.
    ///
    /// Publishing the configuration on-chain is the point — it is what makes the
    /// extension's data sources verifiable rather than merely asserted. The
    /// payload therefore carries no credentials; those travel out of band as a
    /// signed, encrypted envelope over the direct channel, and there is
    /// deliberately no secret-handling path in this contract.
    ///
    /// No cosigners: the machine executes on delivery without waiting for
    /// external co-signatures. See the findings doc before changing this.
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

        // Recorded BEFORE dispatch so the expected hash is set even if delivery
        // never happens. A machine that never receives the instruction then
        // cannot submit, rather than continuing on its previous configuration.
        bytes32 configHash = keccak256(_config);
        expectedConfigHash[_teeId] = configHash;

        TEE_EXTENSION_REGISTRY.sendInstructions{value: msg.value}(teeIds, params);

        emit EndpointsSet(_teeId, configHash);
    }

    /// @notice Finds and sets this contract's extension id. Can only be set once.
    /// DO NOT MODIFY this function.
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

    /// @notice Requests a fresh USDX price observation from the TEE.
    ///
    /// @dev Takes no parameters by design. The observation needs nothing from the
    /// caller: which sources are read is configured by SET_ENDPOINTS, and where the
    /// signed report is accepted is settled by the price store's registered-machine
    /// check rather than by the payload.
    ///
    /// Deliberately unpermissioned: each call costs the registry instruction fee, which
    /// bounds spam, and the price store verifies the TEE signature rather than trusting
    /// the requester. Keepers are a liveness convenience, not a trusted party.
    ///
    /// No cosigners: the machine executes on delivery without waiting for
    /// external co-signatures. See the findings doc before changing this.
    function requestPriceUpdate() external payable {
        address[] memory teeIds = TEE_MACHINE_REGISTRY.getRandomTeeIds(_getExtensionId(), 1);
        address[] memory cosigners = new address[](0);

        ITeeExtensionRegistry.TeeInstructionParams memory params = ITeeExtensionRegistry.TeeInstructionParams({
            opType: OP_TYPE_USDX_ORACLE,
            opCommand: OP_COMMAND_GET_PRICE,
            message: "",
            cosigners: cosigners,
            cosignersThreshold: 0,
            claimBackAddress: msg.sender
        });

        TEE_EXTENSION_REGISTRY.sendInstructions{value: msg.value}(
            teeIds,
            params
        );

        emit PriceUpdateRequested(msg.sender);
    }

    /// @notice Returns the cached extension ID, reverting if not yet set.
    /// @return The extension ID assigned to this contract.
    function _getExtensionId() internal view returns (uint256) {
        require(_extensionId != 0, "Extension ID is not set.");
        return _extensionId;
    }
}
