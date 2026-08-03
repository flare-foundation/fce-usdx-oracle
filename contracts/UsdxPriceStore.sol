// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { ITeeMachineRegistry } from "./interfaces/ITeeMachineRegistry.sol";

/// @title UsdxPriceStore
/// @author Flare Foundation
/// @notice Accepts TEE-signed USDX price reports and stores the latest one.
///
/// @dev Anyone may submit. The keeper is a liveness convenience, not a trusted
/// party: acceptance depends entirely on the signature recovering to a TEE
/// machine registered for this extension. That is deliberate — if submission
/// were permissioned, the keeper's availability would become a dependency of
/// the feed.
///
/// FCC is explicitly "fire and forget": no ordering guarantee and no delivery
/// guarantee. Two consequences shape this contract. First, `observedAt` must be
/// strictly increasing, which covers replay AND out-of-order delivery in one
/// rule. Second, a rejected submission is normal operation, not an error — a
/// slow relayer racing a fast one is expected.
contract UsdxPriceStore {
    /// @notice Domain separator for the signed report. Any change to the report
    /// layout must change this string, so old signatures cannot be reinterpreted.
    bytes32 public constant DOMAIN = keccak256("USDX_ORACLE_PRICE_V1");

    /// @notice The registry that says which TEE machines serve this extension.
    ITeeMachineRegistry public immutable TEE_MACHINE_REGISTRY;

    /// @notice The extension id whose TEE machines may sign price reports.
    uint256 public immutable EXTENSION_ID;

    /// @notice The `getTeeMachineStatus` value a signer must report to be
    /// accepted.
    ///
    /// @dev A constructor parameter rather than a constant because the enum's
    /// numeric values are not exposed by the published bindings — the ABI types
    /// the return as a bare uint8. Guessing it inline would be worse than
    /// either failure mode it causes: too low and revoked machines keep
    /// signing, too high and every submission reverts. CONFIRM THIS AGAINST
    /// flare-smart-contracts-v2 BEFORE MAINNET DEPLOYMENT.
    uint8 public immutable REQUIRED_TEE_STATUS;

    /// @notice The feed this store serves.
    bytes21 public immutable FEED_ID;

    /// @notice Fixed-point scale of the stored price.
    int8 public immutable DECIMALS;

    /// @notice Governance address, able to pause and set bounds.
    address public governance;

    /// @notice Latest accepted price, scaled by 10^DECIMALS.
    uint256 public price;

    /// @notice Observation timestamp of the latest accepted price. Also the
    /// monotonicity key: a submission must beat this to be accepted.
    uint64 public observedAt;

    /// @notice Lower and upper acceptance bounds for `price`, inclusive.
    /// @dev A value outside the band is REJECTED, not clamped. Clamping would
    /// publish a number the TEE never signed, and silently: the whole point of
    /// the signature is that the published price is the one the attested code
    /// computed.
    uint256 public minPrice;
    uint256 public maxPrice;

    /// @notice Maximum age of a report at submission time.
    uint64 public maxAge;

    /// @notice Tolerance for a report stamped slightly in the future, covering
    /// ordinary clock skew between the TEE and the chain.
    uint64 public maxSkew;

    /// @notice When true, submissions are rejected and the feed freezes at its
    /// last value.
    /// @dev Freezes rather than throws. `UsdxCustomFeed.getCurrentFeed()` keeps
    /// returning the last price with its ageing timestamp, because reverting
    /// would turn a paused feed into a liveness outage for every consumer at
    /// once — see the note in UsdxCustomFeed.
    bool public paused;

    event PriceUpdated(uint256 indexed price, uint64 indexed observedAt, address indexed teeId);
    event Paused(bool paused);
    event BoundsUpdated(uint256 minPrice, uint256 maxPrice);
    event GovernanceTransferred(address indexed from, address indexed to);

    error NotGovernance();
    error IsPaused();
    error BadSignatureLength();
    error BadRecoveryId();
    error MalleableSignature();
    error UnknownSigner();
    error WrongDomain();
    error WrongChain();
    error WrongStore();
    error WrongFeed();
    error WrongDecimals();
    error NotNewer();
    error TooOld();
    error TooFarAhead();
    error OutOfBounds();
    error ZeroAddress();
    error BadBounds();

    modifier onlyGovernance() {
        if (msg.sender != governance) revert NotGovernance();
        _;
    }

    constructor(
        ITeeMachineRegistry _teeMachineRegistry,
        uint256 _extensionId,
        uint8 _requiredTeeStatus,
        bytes21 _feedId,
        int8 _decimals,
        address _governance,
        uint256 _minPrice,
        uint256 _maxPrice,
        uint64 _maxAge,
        uint64 _maxSkew
    ) {
        if (address(_teeMachineRegistry) == address(0)) revert ZeroAddress();
        if (_governance == address(0)) revert ZeroAddress();
        if (_minPrice > _maxPrice) revert BadBounds();

        TEE_MACHINE_REGISTRY = _teeMachineRegistry;
        EXTENSION_ID = _extensionId;
        REQUIRED_TEE_STATUS = _requiredTeeStatus;
        FEED_ID = _feedId;
        DECIMALS = _decimals;
        governance = _governance;
        minPrice = _minPrice;
        maxPrice = _maxPrice;
        maxAge = _maxAge;
        maxSkew = _maxSkew;
    }

    /// @notice Submits a TEE-signed price report.
    /// @param _body The ABI-encoded PriceReport, exactly as signed.
    /// @param _signature 65-byte ECDSA signature, v in {27, 28}.
    ///
    /// @dev `_body` is verified as received and only then decoded. It is never
    /// re-encoded from its fields: re-encoding would make verification depend on
    /// this contract agreeing byte-for-byte with the TEE's encoder, and any
    /// future divergence would present as an invalid signature rather than as
    /// the encoding bug it is.
    function submitPrice(bytes calldata _body, bytes calldata _signature) external {
        if (paused) revert IsPaused();

        address teeId = _recover(_body, _signature);

        // The signature is worthless unless the signer is a TEE machine
        // currently serving THIS extension. This is the entire trust anchor.
        //
        // Two separate questions, both of which must hold:
        //   1. is this machine registered to OUR extension, not another one?
        //   2. is it still active, rather than revoked or decommissioned?
        // Checking only (1) would let a revoked machine keep signing prices
        // forever; checking only (2) would accept any active machine on the
        // network, including one running entirely different code.
        _requireRegisteredTee(teeId);

        (
            bytes32 domain,
            uint256 chainId,
            address store,
            bytes21 feedId,
            uint256 reportedPrice,
            int8 decimals,
            uint64 reportedAt,
        ) = abi.decode(_body, (bytes32, uint256, address, bytes21, uint256, int8, uint64, bytes32));

        // Binding checks. Each blocks a distinct replay: a signature valid for
        // some other schema, chain, deployment or feed must not be valid here.
        if (domain != DOMAIN) revert WrongDomain();
        if (chainId != block.chainid) revert WrongChain();
        if (store != address(this)) revert WrongStore();
        if (feedId != FEED_ID) revert WrongFeed();
        if (decimals != DECIMALS) revert WrongDecimals();

        // Strict monotonicity. Covers replay and out-of-order delivery together,
        // which matters because FCC guarantees neither.
        if (reportedAt <= observedAt) revert NotNewer();

        if (block.timestamp > reportedAt && block.timestamp - reportedAt > maxAge) revert TooOld();
        if (reportedAt > block.timestamp && reportedAt - block.timestamp > maxSkew) revert TooFarAhead();

        if (reportedPrice < minPrice || reportedPrice > maxPrice) revert OutOfBounds();

        price = reportedPrice;
        observedAt = reportedAt;

        emit PriceUpdated(reportedPrice, reportedAt, teeId);
    }

    /// @notice Returns the stored price and its observation timestamp.
    /// @dev Never reverts on staleness. Consumers must check `_timestamp`.
    function latest() external view returns (uint256 _price, int8 _decimals, uint64 _timestamp) {
        return (price, DECIMALS, observedAt);
    }

    /// @notice Freezes or unfreezes acceptance of new prices.
    function setPaused(bool _paused) external onlyGovernance {
        paused = _paused;
        emit Paused(_paused);
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
    ///
    /// @dev Both calls are wrapped: a registry that reverts on an unknown
    /// machine must produce UnknownSigner here, not bubble an opaque revert out
    /// of submitPrice. An unregistered signer is ordinary rejected input, not an
    /// exceptional condition.
    function _requireRegisteredTee(address _teeId) internal view {
        try TEE_MACHINE_REGISTRY.getExtensionId(_teeId) returns (uint256 extensionId) {
            if (extensionId != EXTENSION_ID) revert UnknownSigner();
        } catch {
            revert UnknownSigner();
        }

        try TEE_MACHINE_REGISTRY.getTeeMachineStatus(_teeId) returns (uint8 status) {
            if (status != REQUIRED_TEE_STATUS) revert UnknownSigner();
        } catch {
            revert UnknownSigner();
        }
    }

    /// @notice Recovers the signer of an EIP-191 prefixed report body.
    ///
    /// @dev tee-node signs `keccak256("\x19Ethereum Signed Message:\n32" ‖
    /// keccak256(body))` — verified against tee-node
    /// v0.0.21-0.20260619120252-31fc839ae6d2, `pkg/utils/crypto.go`:
    ///
    ///     crypto.Sign(accounts.TextHash(crypto.Keccak256(message)), privKey)
    ///
    /// so the prefix MUST be applied here. Recovering against the bare hash does
    /// not revert; it returns a different, well-formed address, and the failure
    /// then presents as an unregistered TEE rather than as a hashing mismatch.
    ///
    /// tee-node returns v as a recovery id of 0 or 1. The extension normalises
    /// it to 27/28 before publishing (see internal/teenode.NormalizeV), so this
    /// contract requires the normalised form and rejects anything else rather
    /// than accepting both — one accepted representation means one code path to
    /// audit.
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

        // Reject the high-s form. secp256k1 signatures are malleable: (r, s, v)
        // and (r, n-s, v^1) recover the same signer, so accepting both would let
        // an observer mint a second, distinct signature for an already-submitted
        // report. Monotonic `observedAt` makes that harmless here, but rejecting
        // it keeps "one report, one signature" true for anything downstream that
        // keys off the signature bytes.
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
