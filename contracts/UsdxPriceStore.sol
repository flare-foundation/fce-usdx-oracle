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
    /// @dev Settable with `extensionId` so re-registering the extension does not
    /// force a store redeploy and a fresh addCustomFeeds proposal.
    IUsdxConfigSource public configSource;

    /// @notice Extension whose registered TEE machines may sign reports.
    uint256 public extensionId;

    /// @notice How far ahead of chain time a report may be stamped.
    /// @dev Absorbs TEE clock drift, and bounds the worst-case feed freeze (see
    /// the ratchet note in submitPrice). Keep far below `maxAge`.
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

    /// @notice When true, `latest()` reports a zero timestamp so consumers read
    /// the feed as having no usable data.
    /// @dev `paused` stops writes but leaves the last price readable and fresh;
    /// this withdraws it. Zeroing the timestamp rather than the value, because
    /// a zero value reads as "USDX is worthless" to consumers that ignore it.
    bool public invalidated;

    event PriceUpdated(uint256 indexed price, uint64 indexed observedAt, address indexed teeId);
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
    /// @param _body The ABI-encoded PriceReport, exactly as signed.
    /// @param _signature 65-byte ECDSA signature, v in {27, 28}.
    ///
    /// @dev `_body` is verified as received, then decoded — never re-encoded, or
    /// an encoder divergence would surface as a bad signature instead.
    function submitPrice(bytes calldata _body, bytes calldata _signature) external {
        if (paused) revert IsPaused();

        address teeId = _recover(_body, _signature);

        // is this machine registered to OUR extension
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

        // Wrapped like the registry calls: a broken config source is rejected
        // input, and must be distinguishable from "no config published".
        //
        // TODO(M5): expectedConfigHash records what was SENT, not what the
        // machine RUNS. Verify it against the TEE's GET /state `endpointsHash`
        // before it becomes authoritative.
        bytes32 expected;
        try configSource.expectedConfigHash(teeId) returns (bytes32 h) {
            expected = h;
        } catch {
            revert ConfigSourceUnavailable();
        }
        if (expected == bytes32(0)) revert NoConfigPublished();
        if (configHash != expected) revert StaleConfig();

        // Strict monotonicity. Covers replay and out-of-order delivery together,
        // which matters because FCC guarantees neither.
        if (reportedAt <= observedAt) revert NotNewer();

        // `observedAt` is a one-way ratchet with no reset, so a future stamp
        // blocks honest reports until chain time catches up. The bound caps that
        // freeze at MAX_FUTURE_SKEW; unbounded, one clock-jumped report would
        // wedge the feed for as long as it claimed.
        if (reportedAt > block.timestamp + MAX_FUTURE_SKEW) revert TooFarAhead();

        // A report inside the skew window has no age yet. The guard also keeps
        // the subtraction from underflowing.
        if (reportedAt < block.timestamp && block.timestamp - reportedAt > maxAge) revert TooOld();

        // Todo: Claude don't touch this! We need to check if there is anything alse, to add since we removed a few unnecessary checks. 

        if (reportedPrice < minPrice || reportedPrice > maxPrice) revert OutOfBounds();

        price = reportedPrice;
        observedAt = reportedAt;
        observedAtInstructionID = instructionID;

        emit PriceUpdated(reportedPrice, reportedAt, teeId);
    }

    /// @notice Returns the stored price and its observation timestamp.
    /// @dev Never reverts. A zero timestamp means governance has withdrawn the
    /// value, or nothing has been published.
    ///
    /// The timestamp is clamped to chain time: MAX_FUTURE_SKEW can leave
    /// `observedAt` briefly ahead, which would make the usual
    /// `block.timestamp - ts` staleness check panic on underflow. The exact
    /// stamp stays readable on `observedAt` and in `PriceUpdated`.
    function latest() external view returns (uint256 _price, int8 _decimals, uint64 _timestamp) {
        if (invalidated) return (price, DECIMALS, 0);

        uint64 ts = observedAt > block.timestamp ? uint64(block.timestamp) : observedAt;
        return (price, DECIMALS, ts);
    }

    /// @notice Freezes or unfreezes acceptance of new prices.
    /// @dev Stops writes only; pair with `setInvalidated` to withdraw the value.
    function setPaused(bool _paused) external onlyGovernance {
        paused = _paused;
        emit Paused(_paused);
    }

    /// @notice Withdraws or restores the stored value for consumers.
    /// @dev Never cleared automatically — recovery is a decision, not a side
    /// effect of whatever price arrives next.
    function setInvalidated(bool _invalidated) external onlyGovernance {
        invalidated = _invalidated;
        emit Invalidated(_invalidated);
    }

    /// @notice Repoints the store at a new extension generation.
    /// @dev Both move together — a re-registration mints a new id and a new
    /// sender. The stored price is left alone; consider `setInvalidated` until
    /// the new generation has published one.
    function setExtension(uint256 _extensionId, IUsdxConfigSource _configSource) external onlyGovernance {
        // Zero would make the id comparison vacuous: the registry returns 0 for
        // machines it does not know.
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

    /// @notice Reverts unless `_teeId` is an active TEE machine for this extension.
    /// @dev Wrapped so a reverting registry yields UnknownSigner, not an opaque
    /// bubble. This pair also carries the code-hash guarantee — MachineManager
    /// checks the attested hash at register, toProduction and every
    /// confirmAvailability — but only for any approved version, and revoking one
    /// waits on the availability lease, so it is no emergency stop. Pausing is.
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

        // Reject the high-s form: (r, n-s, v^1) recovers the same signer, so
        // accepting both would give one report two valid signatures.
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
