// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { Test } from "forge-std/Test.sol";
import { UsdxPriceStore } from "../contracts/UsdxPriceStore.sol";
import { UsdxCustomFeed } from "../contracts/UsdxCustomFeed.sol";
import { ITeeMachineRegistry } from "../contracts/interfaces/ITeeMachineRegistry.sol";
import { IUsdxConfigSource } from "../contracts/interfaces/IUsdxConfigSource.sol";
import { ITeeExtensionRegistry } from "../contracts/interfaces/ITeeExtensionRegistry.sol";
import { UsdxOracleInstructionSender } from "../contracts/InstructionSender.sol";

/// @dev Stands in for the MachineManager diamond.
contract MockRegistry is ITeeMachineRegistry {
    mapping(address => uint256) public extensionOf;
    mapping(address => uint8) public statusOf;
    bool public shouldRevert;

    function setMachine(address teeId, uint256 extensionId, uint8 status) external {
        extensionOf[teeId] = extensionId;
        statusOf[teeId] = status;
    }

    function setShouldRevert(bool v) external { shouldRevert = v; }

    function getRandomTeeIds(uint256, uint256) external pure returns (address[] memory) {
        return new address[](0);
    }

    function getExtensionId(address _teeId) external view returns (uint256) {
        if (shouldRevert) revert("registry down");
        return extensionOf[_teeId];
    }

    function getTeeMachineStatus(address _teeId) external view returns (uint8) {
        if (shouldRevert) revert("registry down");
        return statusOf[_teeId];
    }
}

/// @dev Stands in for the instruction sender's expectedConfigHash and
/// expectedAdminsHash mappings.
contract MockConfigSource {
    mapping(address => bytes32) public expectedConfigHash;
    mapping(address => bytes32) public expectedAdminsHash;

    function setExpected(address teeId, bytes32 h) external {
        expectedConfigHash[teeId] = h;
    }

    function setExpectedAdmins(address teeId, bytes32 h) external {
        expectedAdminsHash[teeId] = h;
    }
}

/// @dev A config source that is deployed but broken — e.g. repointed at a
/// contract that does not implement the interface.
contract RevertingConfigSource {
    function expectedConfigHash(address) external pure returns (bytes32) {
        revert("config source down");
    }

    function expectedAdminsHash(address) external pure returns (bytes32) {
        revert("config source down");
    }
}

/// @dev Serves a config hash but fails on the admin set, so the admins branch
/// is exercised on its own rather than only behind a wholly dead source.
contract AdminsRevertingConfigSource {
    mapping(address => bytes32) public expectedConfigHash;

    function setExpected(address teeId, bytes32 h) external {
        expectedConfigHash[teeId] = h;
    }

    function expectedAdminsHash(address) external pure returns (bytes32) {
        revert("config source down");
    }
}

contract UsdxPriceStoreTest is Test {
    uint256 constant EXTENSION_ID = 0x10148;
    uint8 constant PRODUCTION = uint8(ITeeMachineRegistry.TeeStatus.PRODUCTION);
    int8 constant DECIMALS = 8;
    uint256 constant PAR = 100_000_000;
    bytes21 constant FEED_ID = bytes21(hex"20555344582f555344000000000000000000000000");

    MockRegistry registry;
    MockConfigSource configSource;
    UsdxPriceStore store;

    bytes32 constant CONFIG_HASH = keccak256("endpoints-v1");
    bytes32 constant ADMINS_HASH = keccak256("admins-v1");

    uint256 teeKey = 0xA11CE;
    address teeId;
    address governance = address(0x600);

    function setUp() public {
        teeId = vm.addr(teeKey);

        registry = new MockRegistry();
        registry.setMachine(teeId, EXTENSION_ID, PRODUCTION);

        configSource = new MockConfigSource();
        configSource.setExpected(teeId, CONFIG_HASH);
        configSource.setExpectedAdmins(teeId, ADMINS_HASH);

        store = new UsdxPriceStore(
            registry, IUsdxConfigSource(address(configSource)),
            EXTENSION_ID, DECIMALS, governance,
            50_000_000,   // minPrice = $0.50
            PAR,          // maxPrice = $1.00
            1 hours       // maxAge
        );

        vm.warp(1_700_000_000);
    }

    // --- helpers ---

    function _body(uint256 price, uint64 observedAt, int8 decimals) internal pure returns (bytes memory) {
        return _bodyWithConfig(price, observedAt, decimals, CONFIG_HASH);
    }

    function _bodyWithConfig(uint256 price, uint64 observedAt, int8 decimals, bytes32 configHash)
        internal pure returns (bytes memory)
    {
        return _bodyWith(price, observedAt, decimals, configHash, ADMINS_HASH);
    }

    function _bodyWithAdmins(uint256 price, uint64 observedAt, int8 decimals, bytes32 adminsHash)
        internal pure returns (bytes memory)
    {
        return _bodyWith(price, observedAt, decimals, CONFIG_HASH, adminsHash);
    }

    function _bodyWith(
        uint256 price,
        uint64 observedAt,
        int8 decimals,
        bytes32 configHash,
        bytes32 adminsHash
    ) internal pure returns (bytes memory) {
        return abi.encode(
            keccak256("USDX_ORACLE_PRICE_V1"),
            price,
            decimals,
            observedAt,
            bytes32(uint256(0xABC)),
            configHash,
            adminsHash
        );
    }

    function _goodBody(uint256 price, uint64 observedAt) internal pure returns (bytes memory) {
        return _body(price, observedAt, DECIMALS);
    }

    /// @dev Signs the way tee-node does, then normalises v to 27/28 the way the
    /// extension does. vm.sign already returns 27/28.
    function _sign(bytes memory body, uint256 key) internal pure returns (bytes memory) {
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", keccak256(body)));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(key, digest);
        return abi.encodePacked(r, s, v);
    }

    function _submit(uint256 price, uint64 observedAt) internal {
        bytes memory body = _goodBody(price, observedAt);
        store.submitPrice(body, _sign(body, teeKey));
    }

    // --- happy path ---

    function test_AcceptsValidReport() public {
        _submit(PAR, 1_700_000_000);

        (uint256 p, int8 d, uint64 t) = store.latest();
        assertEq(p, PAR);
        assertEq(d, DECIMALS);
        assertEq(t, 1_700_000_000);
    }

    /// Anyone may submit: the keeper is liveness, not trust.
    function test_AnyoneMaySubmit() public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        bytes memory sig = _sign(body, teeKey);

        vm.prank(address(0xDEADBEEF));
        store.submitPrice(body, sig);

        (uint256 p,,) = store.latest();
        assertEq(p, PAR);
    }

    // --- the EIP-191 trap ---

    /// A signature over the BARE hash must be rejected. It recovers a
    /// well-formed but different address, so without this the failure would
    /// present as an unregistered TEE.
    function test_RejectsSignatureOverUnprefixedHash() public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);

        (uint8 v, bytes32 r, bytes32 s) = vm.sign(teeKey, keccak256(body)); // no prefix
        bytes memory sig = abi.encodePacked(r, s, v);

        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, sig);
    }

    /// tee-node returns v as 0/1; the extension normalises to 27/28. The raw
    /// form must be rejected rather than silently accepted.
    function test_RejectsUnnormalisedRecoveryId() public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", keccak256(body)));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(teeKey, digest);

        bytes memory raw = abi.encodePacked(r, s, uint8(v - 27)); // back to 0/1

        vm.expectRevert(UsdxPriceStore.BadRecoveryId.selector);
        store.submitPrice(body, raw);
    }

    // --- signer binding ---

    function test_RejectsUnregisteredSigner() public {
        uint256 strangerKey = 0xBAD;
        bytes memory body = _goodBody(PAR, 1_700_000_000);

        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, strangerKey));
    }

    /// A machine registered to a DIFFERENT extension must not sign our prices,
    /// even though it is a genuine, active TEE machine.
    function test_RejectsSignerFromAnotherExtension() public {
        registry.setMachine(teeId, EXTENSION_ID + 1, PRODUCTION);

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A revoked machine must stop being able to publish prices.
    function test_RejectsInactiveSigner() public {
        registry.setMachine(teeId, EXTENSION_ID, uint8(ITeeMachineRegistry.TeeStatus.SUSPENDED));

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A reverting registry must surface as rejection, not as an opaque bubble.
    function test_RegistryRevertBecomesUnknownSigner() public {
        registry.setShouldRevert(true);

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    // --- replay and ordering ---

    /// FCC gives no ordering guarantee, so strict monotonicity has to cover both
    /// an exact replay and a legitimately-older report arriving late.
    function test_RejectsReplay() public {
        _submit(PAR, 1_700_000_000);

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.NotNewer.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsOutOfOrderReport() public {
        _submit(PAR, 1_700_000_000);

        bytes memory body = _goodBody(99_000_000, 1_699_999_999); // older
        vm.expectRevert(UsdxPriceStore.NotNewer.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    // --- schema ---

    function test_RejectsWrongDecimals() public {
        bytes memory body = _body(PAR, 1_700_000_000, 6);
        vm.expectRevert(UsdxPriceStore.WrongDecimals.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsWrongDomain() public {
        bytes memory body = abi.encode(
            keccak256("SOME_OTHER_DOMAIN_V1"),
            PAR, DECIMALS, uint64(1_700_000_000), bytes32(uint256(1)), CONFIG_HASH, ADMINS_HASH
        );
        vm.expectRevert(UsdxPriceStore.WrongDomain.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    // --- bounds and freshness ---

    /// Out-of-band values are REJECTED, not clamped: clamping would publish a
    /// number the TEE never signed.
    function test_RejectsPriceBelowMin() public {
        bytes memory body = _goodBody(1, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.OutOfBounds.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsPriceAboveMax() public {
        bytes memory body = _goodBody(PAR + 1, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.OutOfBounds.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsStaleReport() public {
        bytes memory body = _goodBody(PAR, uint64(block.timestamp - 2 hours));
        vm.expectRevert(UsdxPriceStore.TooOld.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// Monotonicity alone would accept this: nothing is stored yet, so the
    /// report is trivially "newer". maxAge is what stops a report held back and
    /// released long after it was observed — without it, a signed report never
    /// expires, and submission is deliberately unpermissioned.
    function test_HeldBackReportExpires() public {
        bytes memory body = _goodBody(PAR, uint64(block.timestamp));

        // The holder sits on a valid, signed report while the feed stays idle.
        vm.warp(block.timestamp + 30 days);

        vm.expectRevert(UsdxPriceStore.TooOld.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsFutureReport() public {
        bytes memory body = _goodBody(PAR, uint64(block.timestamp + 10 minutes));
        vm.expectRevert(UsdxPriceStore.TooFarAhead.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A report stamped exactly at chain time is accepted.
    function test_AcceptsReportAtChainTime() public {
        _submit(PAR, uint64(block.timestamp));
        (uint256 p,,) = store.latest();
        assertEq(p, PAR);
    }

    /// Ordinary clock drift is absorbed: a slightly-early report goes straight
    /// through instead of having to be held until chain time reaches it.
    function test_AcceptsReportWithinSkewWindow() public {
        uint64 early = uint64(block.timestamp) + 3;
        _submit(PAR, early);

        assertEq(store.price(), PAR);
        // The stamp is stored verbatim; `latest()` clamps what consumers see —
        // see test_LatestClampsFutureTimestampToChainTime.
        assertEq(store.observedAt(), early);
    }

    /// Both sides of the boundary, since this is the whole point of the window.
    function test_SkewWindowBoundary() public {
        uint64 limit = uint64(block.timestamp) + store.MAX_FUTURE_SKEW();

        bytes memory tooFar = _goodBody(PAR, limit + 1);
        vm.expectRevert(UsdxPriceStore.TooFarAhead.selector);
        store.submitPrice(tooFar, _sign(tooFar, teeKey));

        _submit(PAR, limit);
        assertEq(store.observedAt(), limit);
    }

    /// A report inside the window has no age yet, so the maxAge check must not
    /// fire on it — and must not underflow computing an age that is negative.
    function test_FutureReportIsNotAlsoTooOld() public {
        _submit(PAR, uint64(block.timestamp) + 1 minutes);
        assertEq(store.price(), PAR);
    }

    /// The skew window may leave `observedAt` briefly ahead of chain time, but
    /// consumers must never see that: every other feed an integrator uses has an
    /// on-chain timestamp, so `block.timestamp - ts` is the reflex, and it would
    /// panic on underflow. `latest()` clamps; the true stamp stays on
    /// `observedAt` for anyone who needs it.
    function test_LatestClampsFutureTimestampToChainTime() public {
        uint64 early = uint64(block.timestamp) + 90;
        _submit(PAR, early);

        (uint256 p,, uint64 t) = store.latest();
        assertEq(p, PAR);
        assertEq(t, uint64(block.timestamp));
        assertEq(store.observedAt(), early);

        // The subtraction a consumer actually writes must not revert.
        assertEq(block.timestamp - t, 0);

        // Once chain time passes the stamp, the true value is reported.
        vm.warp(uint256(early) + 5);
        (,, t) = store.latest();
        assertEq(t, early);
    }

    /// Clamping must not defeat the withdrawal signal.
    function test_InvalidationWinsOverClamping() public {
        _submit(PAR, uint64(block.timestamp) + 90);

        vm.prank(governance);
        store.setInvalidated(true);

        (,, uint64 t) = store.latest();
        assertEq(t, 0);
    }

    /// The window is also the worst-case freeze: a report accepted at the limit
    /// blocks honest ones until chain time catches up, then resolves itself.
    function test_SkewWindowFreezeIsSelfHealing() public {
        uint64 limit = uint64(block.timestamp) + store.MAX_FUTURE_SKEW();
        _submit(PAR, limit);

        // Honest reports stamped at real chain time lose to the ratchet.
        vm.warp(block.timestamp + 30);
        bytes memory blocked = _goodBody(99_000_000, uint64(block.timestamp));
        vm.expectRevert(UsdxPriceStore.NotNewer.selector);
        store.submitPrice(blocked, _sign(blocked, teeKey));

        // Once chain time passes the stamp, normal operation resumes on its own.
        vm.warp(uint256(limit) + 1);
        _submit(99_000_000, uint64(block.timestamp));
        assertEq(store.price(), 99_000_000);
    }

    /// A clock-jumped report cannot wedge the ratchet: it is rejected outright,
    /// and honest reporting continues unaffected.
    function test_ClockJumpCannotWedgeTheRatchet() public {
        _submit(PAR, uint64(block.timestamp));

        bytes memory jumped = _goodBody(99_000_000, uint64(block.timestamp + 365 days));
        vm.expectRevert(UsdxPriceStore.TooFarAhead.selector);
        store.submitPrice(jumped, _sign(jumped, teeKey));

        vm.warp(block.timestamp + 10 minutes);
        _submit(99_500_000, uint64(block.timestamp));

        (uint256 p,, uint64 t) = store.latest();
        assertEq(p, 99_500_000);
        assertEq(t, uint64(block.timestamp));
    }

    // --- malformed signatures ---

    function test_RejectsShortSignature() public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.BadSignatureLength.selector);
        store.submitPrice(body, hex"1234");
    }

    /// secp256k1 is malleable: (r, n-s, v^1) recovers the same signer. Accepting
    /// both would mean one report has two valid signatures.
    function test_RejectsMalleableSignature() public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", keccak256(body)));
        (uint8 v, bytes32 r, bytes32 s) = vm.sign(teeKey, digest);

        uint256 n = 0xFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFFEBAAEDCE6AF48A03BBFD25E8CD0364141;
        bytes32 flippedS = bytes32(n - uint256(s));
        uint8 flippedV = v == 27 ? 28 : 27;

        vm.expectRevert(UsdxPriceStore.MalleableSignature.selector);
        store.submitPrice(body, abi.encodePacked(r, flippedS, flippedV));
    }

    // --- configuration binding ---

    /// A machine running configuration other than what setEndpoints published
    /// cannot submit at all. This is what makes the on-chain config binding
    /// rather than advisory.
    function test_RejectsReportFromStaleConfig() public {
        bytes memory body = _bodyWithConfig(PAR, 1_700_000_000, DECIMALS, keccak256("other-config"));

        vm.expectRevert(UsdxPriceStore.StaleConfig.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// Fail closed before any configuration is published: no expected hash
    /// means nothing is accepted, rather than everything.
    function test_RejectsWhenNoConfigPublished() public {
        configSource.setExpected(teeId, bytes32(0));

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.NoConfigPublished.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// Republishing configuration invalidates reports built under the old one,
    /// which is the point: the machine must pick up the change before it can
    /// publish again.
    function test_RepublishingConfigInvalidatesOldReports() public {
        _submit(PAR, 1_700_000_000);

        configSource.setExpected(teeId, keccak256("endpoints-v2"));

        // Chain time must reach each observation timestamp: no report may be
        // stamped ahead of it.
        vm.warp(1_700_000_100);
        bytes memory body = _goodBody(PAR, 1_700_000_100);
        vm.expectRevert(UsdxPriceStore.StaleConfig.selector);
        store.submitPrice(body, _sign(body, teeKey));

        // A report under the new configuration is accepted.
        vm.warp(1_700_000_200);
        bytes memory fresh = _bodyWithConfig(PAR, 1_700_000_200, DECIMALS, keccak256("endpoints-v2"));
        store.submitPrice(fresh, _sign(fresh, teeKey));
        (uint256 p,, uint64 t) = store.latest();
        assertEq(p, PAR);
        assertEq(t, 1_700_000_200);
    }

    /// Each machine is bound to its own published configuration, so one that
    /// never received its setEndpoints call cannot publish from stale config.
    function test_ConfigIsPerMachine() public {
        uint256 otherKey = 0xB0B;
        address otherTee = vm.addr(otherKey);
        registry.setMachine(otherTee, EXTENSION_ID, PRODUCTION);
        // otherTee has no published configuration.

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.NoConfigPublished.selector);
        store.submitPrice(body, _sign(body, otherKey));
    }

    // --- governance ---

    function test_PauseFreezesSubmissions() public {
        _submit(PAR, 1_700_000_000);

        vm.prank(governance);
        store.setPaused(true);

        bytes memory body = _goodBody(99_000_000, 1_700_000_100);
        vm.expectRevert(UsdxPriceStore.IsPaused.selector);
        store.submitPrice(body, _sign(body, teeKey));

        // The last value is still readable — a pause freezes, it does not blind.
        (uint256 p,,) = store.latest();
        assertEq(p, PAR);
    }

    function test_OnlyGovernanceMayPause() public {
        vm.expectRevert(UsdxPriceStore.NotGovernance.selector);
        store.setPaused(true);
    }

    function test_OnlyGovernanceMaySetBounds() public {
        vm.expectRevert(UsdxPriceStore.NotGovernance.selector);
        store.setBounds(0, PAR);
    }

    // --- invalidation ---

    /// Pause preserves the value; invalidation withdraws it. Zeroing the
    /// TIMESTAMP is what makes every downstream freshness check fail, while the
    /// value is left alone so nobody reads a zero as "USDX is worthless".
    function test_InvalidateZeroesTimestampNotValue() public {
        _submit(PAR, 1_700_000_000);

        vm.prank(governance);
        store.setInvalidated(true);

        (uint256 p, int8 d, uint64 t) = store.latest();
        assertEq(p, PAR);
        assertEq(d, DECIMALS);
        assertEq(t, 0);

        // The underlying observation is untouched — only what consumers see.
        assertEq(store.observedAt(), 1_700_000_000);
    }

    function test_InvalidateIsReversible() public {
        _submit(PAR, 1_700_000_000);

        vm.prank(governance);
        store.setInvalidated(true);
        vm.prank(governance);
        store.setInvalidated(false);

        (,, uint64 t) = store.latest();
        assertEq(t, 1_700_000_000);
    }

    /// Invalidation does not stop writes; that is what pause is for. A new price
    /// still lands, and clearing the flag stays a deliberate governance act
    /// rather than a side effect of the next submission.
    function test_InvalidateDoesNotBlockSubmissionsNorSelfClear() public {
        _submit(PAR, 1_700_000_000);

        vm.prank(governance);
        store.setInvalidated(true);

        vm.warp(1_700_000_100);
        _submit(99_000_000, 1_700_000_100);

        assertEq(store.price(), 99_000_000);
        assertTrue(store.invalidated());
        (,, uint64 t) = store.latest();
        assertEq(t, 0);
    }

    function test_OnlyGovernanceMayInvalidate() public {
        vm.expectRevert(UsdxPriceStore.NotGovernance.selector);
        store.setInvalidated(true);
    }

    // --- extension repointing ---

    /// Re-registering the extension mints a new id and a new sender. Repointing
    /// keeps the store — and therefore the FtsoV2 feed registration — in place.
    function test_SetExtensionRepointsAcceptance() public {
        uint256 newExtensionId = EXTENSION_ID + 7;
        MockConfigSource newSource = new MockConfigSource();
        newSource.setExpected(teeId, CONFIG_HASH);
        newSource.setExpectedAdmins(teeId, ADMINS_HASH);

        // Under the old extension the machine is now a stranger.
        registry.setMachine(teeId, newExtensionId, PRODUCTION);
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, teeKey));

        vm.prank(governance);
        store.setExtension(newExtensionId, IUsdxConfigSource(address(newSource)));

        store.submitPrice(body, _sign(body, teeKey));
        assertEq(store.price(), PAR);
        assertEq(store.extensionId(), newExtensionId);
        assertEq(address(store.configSource()), address(newSource));
    }

    function test_SetExtensionRejectsZeroValues() public {
        vm.prank(governance);
        vm.expectRevert(UsdxPriceStore.ZeroExtensionId.selector);
        store.setExtension(0, IUsdxConfigSource(address(configSource)));

        vm.prank(governance);
        vm.expectRevert(UsdxPriceStore.ZeroAddress.selector);
        store.setExtension(EXTENSION_ID, IUsdxConfigSource(address(0)));
    }

    function test_OnlyGovernanceMaySetExtension() public {
        vm.expectRevert(UsdxPriceStore.NotGovernance.selector);
        store.setExtension(EXTENSION_ID, IUsdxConfigSource(address(configSource)));
    }

    /// A config source that reverts is rejected input, and must be
    /// distinguishable from "no configuration published for this machine".
    function test_RevertingConfigSourceIsTyped() public {
        RevertingConfigSource bad = new RevertingConfigSource();

        vm.prank(governance);
        store.setExtension(EXTENSION_ID, IUsdxConfigSource(address(bad)));

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.ConfigSourceUnavailable.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    // --- fuzz ---

    /// Whatever is accepted must lie inside the bounds, strictly advance the
    /// stored timestamp, and — as consumers see it — never sit ahead of chain
    /// time. The last one is what makes `block.timestamp - timestamp` safe
    /// downstream, and it holds even though `observedAt` itself may briefly be
    /// up to MAX_FUTURE_SKEW in the future: `latest()` clamps.
    function testFuzz_AcceptedPricesRespectInvariants(uint256 price, uint64 observedAt) public {
        price = bound(price, 0, PAR * 2);
        observedAt = uint64(bound(observedAt, 1, block.timestamp + 1 hours));

        bytes memory body = _goodBody(price, observedAt);
        bytes memory sig = _sign(body, teeKey);

        uint64 before = store.observedAt();

        try store.submitPrice(body, sig) {
            (uint256 p,, uint64 t) = store.latest();
            assertEq(p, price);
            assertGe(p, store.minPrice());
            assertLe(p, store.maxPrice());

            // Monotonicity is a property of the stored stamp, not the clamped one.
            assertGt(store.observedAt(), before);
            // What consumers see is never ahead of chain time.
            assertLe(t, block.timestamp);
        } catch {
            // Rejection is normal operation under fire-and-forget delivery.
        }
    }

    /// A price is only ever accepted from a signature that verifies. Fuzzing the
    /// signature bytes must never produce an accepted report.
    function testFuzz_GarbageSignatureNeverAccepted(bytes32 r, bytes32 s, uint8 v) public {
        bytes memory body = _goodBody(PAR, 1_700_000_000);

        try store.submitPrice(body, abi.encodePacked(r, s, v)) {
            // Only acceptable if the fuzzer somehow produced our TEE's signature.
            (uint256 p,,) = store.latest();
            assertEq(p, PAR);
        } catch {
            // Expected.
        }
        assertEq(store.observedAt() == 0 || store.observedAt() == 1_700_000_000, true);
    }

    // --- per-machine config proof ---
    //
    // expectedConfigHash records what was SENT to a machine. These record what a
    // machine PROVED it was running, which is the half that was missing: a
    // config can be rejected by the extension or never delivered at all, and
    // with several machines each needs its own setEndpoints call.

    function test_LandingAPriceRecordsTheMachine() public {
        _submit(PAR, 1_700_000_000);

        (bytes32 configHash, bytes32 adminsHash, uint64 obsAt, uint64 acceptedAt, uint32 submissions) =
            store.lastReport(teeId);
        assertEq(configHash, CONFIG_HASH);
        assertEq(adminsHash, ADMINS_HASH);
        assertEq(obsAt, 1_700_000_000);
        assertEq(acceptedAt, uint64(block.timestamp));
        assertEq(submissions, 1);

        assertTrue(store.isProvenConfigured(teeId));
        assertEq(store.seenTeeCount(), 1);
        assertEq(store.seenTees(0), teeId);
    }

    function test_FirstSightingEmitsTeeSeen() public {
        vm.expectEmit(true, true, false, false);
        emit UsdxPriceStore.TeeSeen(teeId, CONFIG_HASH);
        _submit(PAR, 1_700_000_000);
    }

    /// seenTees is append-only, so a machine that submits repeatedly appears
    /// once. Otherwise the array grows without bound and teeReports() eventually
    /// runs out of gas.
    function test_MachineIsRecordedOnceButCounted() public {
        // Chain time advances with the reports: observedAt may lead block
        // timestamp by at most MAX_FUTURE_SKEW, so three fixed stamps would trip
        // TooFarAhead rather than exercise the counter.
        _submit(PAR, 1_700_000_000);
        vm.warp(1_700_000_100);
        _submit(PAR - 1, 1_700_000_100);
        vm.warp(1_700_000_200);
        _submit(PAR - 2, 1_700_000_200);

        assertEq(store.seenTeeCount(), 1);

        (,,,, uint32 submissions) = store.lastReport(teeId);
        assertEq(submissions, 3);
    }

    /// An unknown machine is not "configured", it is unproven. The distinction
    /// matters: this is the state a machine sits in between being registered and
    /// being commissioned.
    function test_UnseenMachineIsNotProven() public view {
        assertFalse(store.isProvenConfigured(address(0xDEAD)));
    }

    /// Re-publishing a config withdraws the proof until that machine lands a
    /// price under the new one. Self-announcing by design — it is the signal to
    /// re-run commissioning.
    function test_RepublishingConfigWithdrawsTheProof() public {
        _submit(PAR, 1_700_000_000);
        assertTrue(store.isProvenConfigured(teeId));

        configSource.setExpected(teeId, keccak256("endpoints-v2"));
        assertFalse(store.isProvenConfigured(teeId));

        // The record still says what the machine last proved, which is what
        // tells an operator it is running the OLD config rather than nothing.
        (bytes32 configHash,,,,) = store.lastReport(teeId);
        assertEq(configHash, CONFIG_HASH);
    }

    /// A config source that has gone away must read as unproven, not revert and
    /// not accidentally report true.
    function test_BrokenConfigSourceIsNotProven() public {
        _submit(PAR, 1_700_000_000);

        // Deployed before the prank: `new` is itself a call and would consume it.
        RevertingConfigSource broken = new RevertingConfigSource();
        vm.prank(governance);
        store.setExtension(EXTENSION_ID, IUsdxConfigSource(address(broken)));

        assertFalse(store.isProvenConfigured(teeId));
    }

    /// The whole fleet in one call, so a dashboard needs no indexer.
    function test_TeeReportsEnumeratesTheFleet() public {
        uint256 secondKey = 0xB0B;
        address secondTee = vm.addr(secondKey);
        registry.setMachine(secondTee, EXTENSION_ID, PRODUCTION);
        configSource.setExpected(secondTee, CONFIG_HASH);
        configSource.setExpectedAdmins(secondTee, ADMINS_HASH);

        _submit(PAR, 1_700_000_000);

        bytes memory body = _goodBody(PAR - 1, 1_700_000_100);
        store.submitPrice(body, _sign(body, secondKey));

        (address[] memory tees, UsdxPriceStore.TeeReport[] memory reports) = store.teeReports();
        assertEq(tees.length, 2);
        assertEq(reports.length, 2);
        assertEq(tees[0], teeId);
        assertEq(tees[1], secondTee);
        assertEq(reports[0].observedAt, 1_700_000_000);
        assertEq(reports[1].observedAt, 1_700_000_100);
    }

    // --- per-machine admin-set proof ---
    //
    // The admin set decides who may install credentials in an enclave, so it is
    // checked on the same terms as the endpoint config: published on-chain by
    // the owner, committed to inside the signed body, and compared here.

    function test_RejectsStaleAdmins() public {
        bytes memory body = _bodyWithAdmins(PAR, 1_700_000_000, DECIMALS, keccak256("admins-v0"));
        vm.expectRevert(UsdxPriceStore.StaleAdmins.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_RejectsWhenNoAdminsPublished() public {
        configSource.setExpectedAdmins(teeId, bytes32(0));
        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.NoAdminsPublished.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A source that serves the config hash but fails on the admin set must be a
    /// typed rejection, not an opaque revert bubbling up from the try block.
    function test_AdminsRevertingConfigSourceIsTyped() public {
        AdminsRevertingConfigSource broken = new AdminsRevertingConfigSource();
        broken.setExpected(teeId, CONFIG_HASH);

        vm.prank(governance);
        store.setExtension(EXTENSION_ID, IUsdxConfigSource(address(broken)));

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.ConfigSourceUnavailable.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// Rotating the admin set withdraws the proof exactly as republishing
    /// endpoints does: the machine has not yet landed a price under the new set,
    /// so the rotated-out signers could still be the ones it honours.
    function test_RepublishingAdminsWithdrawsTheProof() public {
        _submit(PAR, 1_700_000_000);
        assertTrue(store.isProvenConfigured(teeId));

        configSource.setExpectedAdmins(teeId, keccak256("admins-v2"));
        assertFalse(store.isProvenConfigured(teeId));

        vm.warp(1_700_000_200);
        bytes memory fresh = _bodyWithAdmins(PAR, 1_700_000_200, DECIMALS, keccak256("admins-v2"));
        store.submitPrice(fresh, _sign(fresh, teeKey));
        assertTrue(store.isProvenConfigured(teeId));
    }

    /// A body from an extension still on the six-field layout must not be
    /// decoded as if its missing admin commitment were absent by choice. Without
    /// the length check the short body would simply fail to decode; with it the
    /// rejection names the real cause.
    function test_RejectsShortBody() public {
        bytes memory body = abi.encode(
            keccak256("USDX_ORACLE_PRICE_V1"),
            PAR, DECIMALS, uint64(1_700_000_000), bytes32(uint256(0xABC)), CONFIG_HASH
        );
        vm.expectRevert(UsdxPriceStore.BadBodyLength.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// Trailing bytes are the other half: abi.decode tolerates them, so without
    /// the length check a longer body would be silently truncated to the fields
    /// this store knows about.
    function test_RejectsOverlongBody() public {
        bytes memory body = abi.encodePacked(_goodBody(PAR, 1_700_000_000), bytes32(uint256(1)));
        vm.expectRevert(UsdxPriceStore.BadBodyLength.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A rejected submission must leave no trace. Recording an unproven machine
    /// would make the fleet view claim a commissioning that never happened.
    function test_RejectedSubmissionRecordsNothing() public {
        bytes memory body = _bodyWithConfig(PAR, 1_700_000_000, DECIMALS, keccak256("wrong-config"));

        vm.expectRevert(UsdxPriceStore.StaleConfig.selector);
        store.submitPrice(body, _sign(body, teeKey));

        assertEq(store.seenTeeCount(), 0);
        assertFalse(store.isProvenConfigured(teeId));
    }

    /// The losing side of a race records nothing either — which is exactly why
    /// commissioning has to target a chosen machine rather than hope it wins.
    function test_MachineLosingTheRaceIsNotRecorded() public {
        uint256 secondKey = 0xB0B;
        address secondTee = vm.addr(secondKey);
        registry.setMachine(secondTee, EXTENSION_ID, PRODUCTION);
        configSource.setExpected(secondTee, CONFIG_HASH);
        configSource.setExpectedAdmins(secondTee, ADMINS_HASH);

        _submit(PAR, 1_700_000_100);

        // An older report from the second machine: valid, but not newer.
        bytes memory body = _goodBody(PAR - 1, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.NotNewer.selector);
        store.submitPrice(body, _sign(body, secondKey));

        assertEq(store.seenTeeCount(), 1);
        assertFalse(store.isProvenConfigured(secondTee));
    }
}

contract UsdxCustomFeedTest is Test {
    bytes21 constant FEED_ID = bytes21(hex"20555344582f555344000000000000000000000000");

    MockRegistry registry;
    UsdxPriceStore store;
    UsdxCustomFeed feed;

    function setUp() public {
        registry = new MockRegistry();
        store = new UsdxPriceStore(
            registry, IUsdxConfigSource(address(new MockConfigSource())),
            1, 8, address(this), 0, 100_000_000, 1 hours
        );
        feed = new UsdxCustomFeed(store, FEED_ID);
    }

    function test_FeedId() public view {
        assertEq(feed.feedId(), FEED_ID);
    }

    /// No underlying FTSO read to pay for.
    function test_FeeIsZero() public view {
        assertEq(feed.calculateFee(), 0);
    }

    function test_ConstructorRejectsZeroStore() public {
        vm.expectRevert(UsdxCustomFeed.ZeroAddress.selector);
        new UsdxCustomFeed(UsdxPriceStore(address(0)), FEED_ID);
    }

    /// FtsoV2 would reject a zero id at registration, but only at a governance
    /// proposal with a timelock. Fail at deploy instead.
    function test_ConstructorRejectsZeroFeedId() public {
        vm.expectRevert(UsdxCustomFeed.ZeroFeedId.selector);
        new UsdxCustomFeed(store, bytes21(0));
    }

    /// The adapter is the sole authority on the feed id: FtsoV2 reads `feedId()`
    /// off the contract being registered rather than taking an id argument, and
    /// the store holds no copy to disagree with. A second adapter over the same
    /// store may therefore carry a different id — deliberately allowed, since a
    /// rescaled or renamed variant would need exactly that.
    function test_AdapterOwnsTheFeedIdIndependentlyOfTheStore() public {
        bytes21 otherId = bytes21(hex"21555344582f555344000000000000000000000000");
        UsdxCustomFeed other = new UsdxCustomFeed(store, otherId);

        assertEq(other.feedId(), otherId);
        assertEq(address(other.STORE()), address(store));
        // Both adapters serve the same store, under different ids.
        assertEq(address(feed.STORE()), address(other.STORE()));
    }

    /// The critical liveness property. FtsoV2.getFeedById forwards this call, so
    /// a revert here becomes an outage for every consumer at once. It must
    /// return, even with nothing ever published.
    function test_NeverRevertsWhenNoPriceEverPublished() public {
        (uint256 v, int8 d, uint64 t) = feed.getCurrentFeed();
        assertEq(v, 0);
        assertEq(d, 8);
        assertEq(t, 0);
    }

    /// A paused store must freeze the feed, not make it throw.
    function test_NeverRevertsWhenPaused() public {
        store.setPaused(true);
        (uint256 v,, uint64 t) = feed.getCurrentFeed();
        assertEq(v, 0);
        assertEq(t, 0);
    }

    /// Invalidation is the withdrawal signal, and it must reach consumers
    /// through the return value rather than a revert — FtsoV2 forwards this call
    /// straight through, so throwing would take down every consumer at once.
    function test_NeverRevertsWhenInvalidated() public {
        store.setInvalidated(true);
        (uint256 v, int8 d, uint64 t) = feed.getCurrentFeed();
        assertEq(v, 0);
        assertEq(d, 8);
        assertEq(t, 0);
    }
}

/// Cross-language verification of the report encoding.
///
/// The vector below is the exact output of the Go encoder
/// (internal/report.EncodeBody), captured from TestEncodeBody_GoldenLayout —
/// which itself builds its expectation word by word from the ABI spec rather
/// than from the encoder. So this test closes the loop: Go's encoder, an
/// independent hand-derivation, and Solidity's decoder all agree.
///
/// If this fails, the TEE and the store disagree about the payload layout, and
/// on chain that would present as an invalid signature rather than as the
/// encoding bug it is.
contract ReportEncodingCompatibilityTest is Test {
    bytes constant GO_ENCODED_BODY =
        hex"88b5ff1e9159fdf5d2b094502b22516857105f507dfc36b0248520a930571a0d"
        hex"0000000000000000000000000000000000000000000000000000000005f5e100"
        hex"0000000000000000000000000000000000000000000000000000000000000008"
        hex"000000000000000000000000000000000000000000000000000000006553f100"
        hex"0000000000000000000000000000000000000000000000000000000000000011"
        hex"0000000000000000000000000000000000000000000000000000000000009f3c";

    function test_SolidityDecodesGoEncodedBody() public pure {
        (
            bytes32 domain,
            uint256 price,
            int8 decimals,
            uint64 observedAt,
            bytes32 instructionId,
            bytes32 endpointsHash
        ) = abi.decode(
            GO_ENCODED_BODY,
            (bytes32, uint256, int8, uint64, bytes32, bytes32)
        );

        assertEq(domain, keccak256("USDX_ORACLE_PRICE_V1"), "domain separator");
        assertEq(price, 100_000_000, "price ($1.00 at 8 decimals)");
        assertEq(decimals, int8(8), "decimals");
        assertEq(observedAt, uint64(1_700_000_000), "observedAt");
        assertEq(instructionId, bytes32(uint256(0x11)), "instruction id");
        assertEq(endpointsHash, bytes32(uint256(0x9f3c)), "endpoints hash");
    }

    /// The Go-encoded body must be exactly five static words.
    function test_GoEncodedBodyIsSixWords() public pure {
        assertEq(GO_ENCODED_BODY.length, 6 * 32, "body length");
    }
}

/// @dev Captures what was sent, so tests can assert the instruction's contents
/// rather than merely that it did not revert.
contract MockExtensionRegistry is ITeeExtensionRegistry {
    address[] public lastTeeIds;
    bytes32 public lastOpType;
    bytes32 public lastOpCommand;
    bytes public lastMessage;
    uint256 public sendCount;
    uint256 public nextId = 0x10001;

    function sendInstructions(address[] calldata _teeIds, TeeInstructionParams calldata _params)
        external payable returns (bytes32)
    {
        delete lastTeeIds;
        for (uint256 i = 0; i < _teeIds.length; ++i) lastTeeIds.push(_teeIds[i]);
        lastOpType = _params.opType;
        lastOpCommand = _params.opCommand;
        lastMessage = _params.message;
        sendCount++;
        return bytes32(uint256(sendCount));
    }

    function nextPublicExtensionId() external view returns (uint256) { return nextId; }

    function getTeeExtensionInstructionsSender(uint256) external view returns (address) {
        return msg.sender;
    }

    function teeIdCount() external view returns (uint256) { return lastTeeIds.length; }
}

contract SetEndpointsTest is Test {
    MockExtensionRegistry extReg;
    MockRegistry machineReg;
    UsdxOracleInstructionSender sender;

    address teeA = address(0xA1);
    address teeB = address(0xB2);
    address stranger = address(0xBAD);

    bytes config = bytes('{"chains":[{"name":"flare"}]}');

    function setUp() public {
        extReg = new MockExtensionRegistry();
        machineReg = new MockRegistry();
        sender = new UsdxOracleInstructionSender(extReg, machineReg);
    }

    function test_DeployerIsOwner() public view {
        assertEq(sender.owner(), address(this));
    }

    function test_SetEndpointsSendsConfiguration() public {
        sender.setEndpoints(teeA, config);

        assertEq(extReg.sendCount(), 1);
        assertEq(extReg.lastOpType(), bytes32("USDX_CONFIG"));
        assertEq(extReg.lastOpCommand(), bytes32("SET_ENDPOINTS"));
        assertEq(extReg.lastMessage(), config, "payload must be delivered verbatim");
    }

    /// The machine is named explicitly. A random pick would configure one
    /// machine and leave the rest stale.
    function test_TargetsTheNamedMachineOnly() public {
        sender.setEndpoints(teeB, config);

        assertEq(extReg.teeIdCount(), 1);
        assertEq(extReg.lastTeeIds(0), teeB);
    }

    /// An open setter is a denial-of-service hole: anyone could repoint the
    /// extension's data sources and it would fail closed on every observation.
    function test_OnlyOwnerMaySetEndpoints() public {
        vm.prank(stranger);
        vm.expectRevert(UsdxOracleInstructionSender.NotOwner.selector);
        sender.setEndpoints(teeA, config);
    }

    function test_RejectsZeroTeeId() public {
        vm.expectRevert(UsdxOracleInstructionSender.ZeroTeeId.selector);
        sender.setEndpoints(address(0), config);
    }

    function test_RejectsEmptyConfig() public {
        vm.expectRevert(UsdxOracleInstructionSender.EmptyConfig.selector);
        sender.setEndpoints(teeA, "");
    }

    function test_RejectsOversizedConfig() public {
        bytes memory big = new bytes(sender.MAX_ENDPOINTS_BYTES() + 1);
        vm.expectRevert(UsdxOracleInstructionSender.ConfigTooLarge.selector);
        sender.setEndpoints(teeA, big);
    }

    /// The event carries the config hash so an observer can confirm which
    /// configuration was published without re-reading calldata.
    function test_EmitsConfigHash() public {
        vm.expectEmit(true, true, false, false);
        emit UsdxOracleInstructionSender.EndpointsSet(teeA, keccak256(config));
        sender.setEndpoints(teeA, config);
    }

    function test_OwnershipTransfer() public {
        sender.transferOwnership(stranger);
        assertEq(sender.owner(), stranger);

        vm.expectRevert(UsdxOracleInstructionSender.NotOwner.selector);
        sender.setEndpoints(teeA, config);

        vm.prank(stranger);
        sender.setEndpoints(teeA, config);
        assertEq(extReg.sendCount(), 1);
    }

    function test_RejectsZeroOwner() public {
        vm.expectRevert(UsdxOracleInstructionSender.ZeroOwner.selector);
        sender.transferOwnership(address(0));
    }

    /// requestPriceUpdate stays permissionless: the store verifies the TEE
    /// signature, so anyone paying the fee may trigger an observation.
    function test_RequestPriceUpdateIsNotOwnerOnly() public {
        machineReg.setMachine(teeA, 1, 1);
        vm.prank(stranger);
        // Reverts only because the extension id is unset, not on authorisation.
        vm.expectRevert(bytes("Extension ID is not set."));
        sender.requestPriceUpdate();
    }
}
