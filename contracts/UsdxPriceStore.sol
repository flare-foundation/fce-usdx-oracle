// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { ITeeMachineRegistry } from "./interfaces/ITeeMachineRegistry.sol";
import { IUsdxConfigSource } from "./interfaces/IUsdxConfigSource.sol";

/// @title UsdxPriceStore
/// @author Flare Foundation
/// @notice Accepts TEE-signed USDX price reports and stores the latest one.
/// @dev Open submission; trust is signature plus registry membership. Strictly increasing `observedAt` covers replay and out-of-order delivery.
contract UsdxPriceStore {
    /// @notice Report schema version; change when the signed layout changes.
    bytes32 public constant DOMAIN = keccak256("USDX_ORACLE_PRICE_V1");

    /// @notice Registry of TEE machines authorised for this extension.
    /// @dev Immutable — the FlareTeeManager diamond is a permanent address.
    ITeeMachineRegistry public immutable TEE_MACHINE_REGISTRY;

    /// @notice Per-machine configuration hashes published via setEndpoints.
    /// @dev Updatable with extensionId so re-registration does not require redeploying the store.
    IUsdxConfigSource public configSource;

    /// @notice Extension whose registered TEE machines may sign reports.
    uint256 public extensionId;

    /// @notice Max clock drift allowed between report timestamp and chain time.
    /// @dev Caps how long a future-dated report can freeze the feed; keep well below maxAge.
    uint64 public constant MAX_FUTURE_SKEW = 2 minutes;

    /// @notice Required machine status for an accepted signer (PRODUCTION = 2).
    /// @dev Fixed constant — the only valid value; previously misconfigured as a constructor arg on Coston2.
    uint8 public constant REQUIRED_TEE_STATUS = uint8(ITeeMachineRegistry.TeeStatus.PRODUCTION);

    /// @notice Fixed-point decimal scale of stored prices.
    int8 public immutable DECIMALS;

    /// @notice Address that may pause the store and set price bounds.
    address public governance;

    /// @notice Latest accepted price, scaled by 10^DECIMALS.
    uint256 public price;

    /// @notice Timestamp of the latest accepted observation; must strictly increase.
    uint64 public observedAt;

    /// @notice Instruction id of the latest accepted report.
    bytes32 public observedAtInstructionID;

    /// @notice Inclusive price acceptance band; out-of-band reports are rejected, not clamped.
    uint256 public minPrice;
    uint256 public maxPrice;

    /// @notice Maximum report age at submission time.
    uint64 public maxAge;

    /// @notice When true, new submissions are rejected; consumers keep reading the last price.
    bool public paused;

    /// @notice When true, latest() returns timestamp 0 so consumers treat the feed as empty.
    /// @dev Unlike paused, this withdraws the value without zeroing price (zero would read as worthless).
    bool public invalidated;

    /// @notice What one machine last proved about itself by landing a price.
    ///
    /// `expectedConfigHash` on the sender records what was SENT to a machine.
    /// Nothing recorded what a machine was actually RUNNING — a config can be
    /// rejected by the extension or never delivered at all, and with several
    /// machines each needs its own setEndpoints call. This is the other half:
    /// submitPrice has already recovered the signer and checked its config hash
    /// against the expected one, so writing it down turns an accepted price into
    /// standing evidence that this machine ran that configuration.
    struct TeeReport {
        /// @dev The config the machine proved it was running, not the one sent to it.
        bytes32 configHash;
        /// @dev The report's own timestamp, from inside the enclave.
        uint64 observedAt;
        /// @dev Chain time when the report was accepted.
        uint64 acceptedAt;
        uint32 submissions;
    }

    /// @notice Per-machine record of the last price it landed.
    mapping(address => TeeReport) public lastReport;

    /// @notice Every machine that has ever landed a price, append-only.
    /// @dev Enumerable so a dashboard can read the fleet without an indexer.
    /// Bounded in practice by the number of machines an extension ever runs.
    address[] public seenTees;

    event PriceUpdated(uint256 indexed price, uint64 indexed observedAt, address indexed teeId);

    /// @notice Emitted the first time a machine lands a price — its commissioning.
    /// @dev Only on first sighting; later submissions update lastReport silently.
    event TeeSeen(address indexed teeId, bytes32 indexed configHash);
    event Paused(bool paused);
    event Invalidated(bool invalidated);
    event BoundsUpdated(uint256 minPrice, uint256 maxPrice);
    event GovernanceTransferred(address indexed from, address indexed to);
    event ExtensionUpdated(uint256 extensionId, IUsdxConfigSource configSource);

    error NotGovernance();
    error IsPaused();
    error BadSignatureLength();
    error BadRecoveryId();
    error MalleableSignature();
    error UnknownSigner();
    error WrongDomain();
    error WrongDecimals();
    error NotNewer();
    error TooOld();
    error TooFarAhead();
    error OutOfBounds();
    error ZeroAddress();
    error BadBounds();
    error StaleConfig();
    error NoConfigPublished();
    error ConfigSourceUnavailable();
    error ZeroExtensionId();

    modifier onlyGovernance() {
        if (msg.sender != governance) revert NotGovernance();
        _;
    }

    constructor(
        ITeeMachineRegistry _teeMachineRegistry,
        IUsdxConfigSource _configSource,
        uint256 _extensionId,
        int8 _decimals,
        address _governance,
        uint256 _minPrice,
        uint256 _maxPrice,
        uint64 _maxAge
    ) {
        if (address(_teeMachineRegistry) == address(0)) revert ZeroAddress();
        if (address(_configSource) == address(0)) revert ZeroAddress();
        if (_governance == address(0)) revert ZeroAddress();
        if (_extensionId == 0) revert ZeroExtensionId();
        if (_minPrice > _maxPrice) revert BadBounds();

        TEE_MACHINE_REGISTRY = _teeMachineRegistry;
        configSource = _configSource;
        extensionId = _extensionId;
        DECIMALS = _decimals;
        governance = _governance;
        minPrice = _minPrice;
        maxPrice = _maxPrice;
        maxAge = _maxAge;
    }

    /// @notice Submits a TEE-signed price report.
    /// @param _body ABI-encoded PriceReport, exactly as signed.
    /// @param _signature 65-byte ECDSA signature, v in {27, 28}.
    /// @dev Body is verified as received, then decoded — never re-encoded.
    function submitPrice(bytes calldata _body, bytes calldata _signature) external {
        if (paused) revert IsPaused();

        address teeId = _recover(_body, _signature);

        _requireRegisteredTee(teeId);

        (
            bytes32 domain,
            uint256 reportedPrice,
            int8 decimals,
            uint64 reportedAt,
            bytes32 instructionID,
            bytes32 configHash
        ) = abi.decode(_body, (bytes32, uint256, int8, uint64, bytes32, bytes32));

        if (domain != DOMAIN) revert WrongDomain();
        if (decimals != DECIMALS) revert WrongDecimals();

        // Config source errors are rejected input, not opaque reverts.
        bytes32 expected;
        try configSource.expectedConfigHash(teeId) returns (bytes32 h) {
            expected = h;
        } catch {
            revert ConfigSourceUnavailable();
        }
        if (expected == bytes32(0)) revert NoConfigPublished();
        if (configHash != expected) revert StaleConfig();

        // Strictly increasing observedAt covers replay and out-of-order delivery.
        if (reportedAt <= observedAt) revert NotNewer();

        // Reject reports too far in the future; observedAt only ratchets up.
        if (reportedAt > block.timestamp + MAX_FUTURE_SKEW) revert TooFarAhead();

        // Inside the skew window, age is zero; also prevents subtraction underflow.
        if (reportedAt < block.timestamp && block.timestamp - reportedAt > maxAge) revert TooOld();

        // Todo: Claude don't touch this! We need to check if there is anything alse, to add since we removed a few unnecessary checks. 

        if (reportedPrice < minPrice || reportedPrice > maxPrice) revert OutOfBounds();

        price = reportedPrice;
        observedAt = reportedAt;
        observedAtInstructionID = instructionID;

        _recordTee(teeId, configHash, reportedAt);

        emit PriceUpdated(reportedPrice, reportedAt, teeId);
    }

    /// @notice Every machine that has landed a price, with what it last proved.
    /// @dev One call for the whole fleet; the array is machine-count sized.
    function teeReports() external view returns (address[] memory _tees, TeeReport[] memory _reports) {
        _tees = seenTees;
        _reports = new TeeReport[](_tees.length);
        for (uint256 i = 0; i < _tees.length; ++i) {
            _reports[i] = lastReport[_tees[i]];
        }
    }

    /// @notice How many machines have ever landed a price.
    function seenTeeCount() external view returns (uint256) {
        return seenTees.length;
    }

    /// @notice Whether `_teeId` has proven it runs the configuration currently published for it.
    ///
    /// @dev False for a machine that never landed a price, and false again as
    /// soon as setEndpoints publishes something new for it — which is correct
    /// and self-announcing: a re-pushed config has not been proven until that
    /// machine lands a price under it. Re-commission with
    /// `submit-price -direct -p <that machine's proxy>`.
    ///
    /// This is a record of commissioning, not of liveness. Only the machine that
    /// wins a given cycle updates its entry, so a stale `acceptedAt` means "has
    /// not won lately", not "unhealthy" — read GET /state per machine for that.
    function isProvenConfigured(address _teeId) external view returns (bool) {
        TeeReport storage report = lastReport[_teeId];
        if (report.acceptedAt == 0) return false;

        // A config source that has gone away must not read as "configured".
        try configSource.expectedConfigHash(_teeId) returns (bytes32 expected) {
            return expected != bytes32(0) && report.configHash == expected;
        } catch {
            return false;
        }
    }

    /// @dev Records what a machine just proved. First sighting appends to seenTees.
    function _recordTee(address _teeId, bytes32 _configHash, uint64 _reportedAt) private {
        TeeReport storage report = lastReport[_teeId];

        // acceptedAt is the first-sighting flag: it is only ever set here, and a
        // machine cannot reach this point without having passed every guard.
        if (report.acceptedAt == 0) {
            seenTees.push(_teeId);
            emit TeeSeen(_teeId, _configHash);
        }

        report.configHash = _configHash;
        report.observedAt = _reportedAt;
        report.acceptedAt = uint64(block.timestamp);
        report.submissions += 1;
    }

    /// @notice Returns the stored price and its observation timestamp.
    /// @dev Never reverts. Zero timestamp means invalidated or unpublished. Clamped to block.timestamp so staleness checks cannot underflow.
    function latest() external view returns (uint256 _price, int8 _decimals, uint64 _timestamp) {
        if (invalidated) return (price, DECIMALS, 0);

        uint64 ts = observedAt > block.timestamp ? uint64(block.timestamp) : observedAt;
        return (price, DECIMALS, ts);
    }

    /// @notice Freezes or unfreezes acceptance of new prices.
    /// @dev Stops writes only; use setInvalidated to withdraw the value from consumers.
    function setPaused(bool _paused) external onlyGovernance {
        paused = _paused;
        emit Paused(_paused);
    }

    /// @notice Withdraws or restores the stored value for consumers.
    /// @dev Not cleared automatically — recovery requires an explicit governance decision.
    function setInvalidated(bool _invalidated) external onlyGovernance {
        invalidated = _invalidated;
        emit Invalidated(_invalidated);
    }

    /// @notice Repoints the store at a new extension generation.
    /// @dev Id and config source move together on re-registration; consider invalidating until the new generation publishes.
    function setExtension(uint256 _extensionId, IUsdxConfigSource _configSource) external onlyGovernance {
        // Zero id would make the registry check vacuous.
        if (_extensionId == 0) revert ZeroExtensionId();
        if (address(_configSource) == address(0)) revert ZeroAddress();

        extensionId = _extensionId;
        configSource = _configSource;
        emit ExtensionUpdated(_extensionId, _configSource);
    }

    /// @notice Updates the acceptance bounds.
    function setBounds(uint256 _minPrice, uint256 _maxPrice) external onlyGovernance {
        if (_minPrice > _maxPrice) revert BadBounds();
        minPrice = _minPrice;
        maxPrice = _maxPrice;
        emit BoundsUpdated(_minPrice, _maxPrice);
    }

    /// @notice Transfers governance.
    function transferGovernance(address _governance) external onlyGovernance {
        if (_governance == address(0)) revert ZeroAddress();
        emit GovernanceTransferred(governance, _governance);
        governance = _governance;
    }

    /// @notice Reverts unless `_teeId` is a production TEE machine for this extension.
    /// @dev Registry errors surface as UnknownSigner, not opaque reverts.
    function _requireRegisteredTee(address _teeId) internal view {
        try TEE_MACHINE_REGISTRY.getExtensionId(_teeId) returns (uint256 signerExtensionId) {
            if (signerExtensionId != extensionId) revert UnknownSigner();
        } catch {
            revert UnknownSigner();
        }

        try TEE_MACHINE_REGISTRY.getTeeMachineStatus(_teeId) returns (uint8 status) {
            if (status != REQUIRED_TEE_STATUS) revert UnknownSigner();
        } catch {
            revert UnknownSigner();
        }
    }

    /// @notice Recovers the TEE signer from a 65-byte signature over the report body.
    /// @dev Applies the EIP-191 prefix to match tee-node; requires v in {27, 28} as normalised by the extension.
    function _recover(bytes calldata _body, bytes calldata _signature) internal pure returns (address) {
        if (_signature.length != 65) revert BadSignatureLength();

        bytes32 r;
        bytes32 s;
        uint8 v;
        assembly {
            r := calldataload(_signature.offset)
            s := calldataload(add(_signature.offset, 32))
            v := byte(0, calldataload(add(_signature.offset, 64)))
        }

        if (v != 27 && v != 28) revert BadRecoveryId();

        // Reject high-s signatures; (r, n-s, v^1) recovers the same signer.
        if (uint256(s) > 0x7FFFFFFFFFFFFFFFFFFFFFFFFFFFFFFF5D576E7357A4501DDFE92F46681B20A0) {
            revert MalleableSignature();
        }

        bytes32 digest = keccak256(
            abi.encodePacked("\x19Ethereum Signed Message:\n32", keccak256(_body))
        );

        address signer = ecrecover(digest, v, r, s);
        if (signer == address(0)) revert UnknownSigner();
        return signer;
    }
}
