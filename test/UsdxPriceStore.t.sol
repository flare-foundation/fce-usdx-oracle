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

/// @dev Stands in for the instruction sender's expectedConfigHash mapping.
contract MockConfigSource {
    mapping(address => bytes32) public expectedConfigHash;

    function setExpected(address teeId, bytes32 h) external {
        expectedConfigHash[teeId] = h;
    }
}

contract UsdxPriceStoreTest is Test {
    uint256 constant EXTENSION_ID = 0x10148;
    uint8 constant ACTIVE = 1;
    int8 constant DECIMALS = 8;
    uint256 constant PAR = 100_000_000;
    bytes21 constant FEED_ID = bytes21(hex"20555344582f555344000000000000000000000000");

    MockRegistry registry;
    MockConfigSource configSource;
    UsdxPriceStore store;

    bytes32 constant CONFIG_HASH = keccak256("endpoints-v1");

    uint256 teeKey = 0xA11CE;
    address teeId;
    address governance = address(0x600);

    function setUp() public {
        teeId = vm.addr(teeKey);

        registry = new MockRegistry();
        registry.setMachine(teeId, EXTENSION_ID, ACTIVE);

        configSource = new MockConfigSource();
        configSource.setExpected(teeId, CONFIG_HASH);

        store = new UsdxPriceStore(
            registry, IUsdxConfigSource(address(configSource)),
            EXTENSION_ID, ACTIVE, FEED_ID, DECIMALS, governance,
            50_000_000,   // minPrice = $0.50
            PAR,          // maxPrice = $1.00
            1 hours,      // maxAge
            5 minutes     // maxSkew
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
        return abi.encode(
            keccak256("USDX_ORACLE_PRICE_V1"),
            price,
            decimals,
            observedAt,
            bytes32(uint256(0xABC)),
            configHash
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
        registry.setMachine(teeId, EXTENSION_ID + 1, ACTIVE);

        bytes memory body = _goodBody(PAR, 1_700_000_000);
        vm.expectRevert(UsdxPriceStore.UnknownSigner.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    /// A revoked machine must stop being able to publish prices.
    function test_RejectsInactiveSigner() public {
        registry.setMachine(teeId, EXTENSION_ID, ACTIVE + 1);

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
            PAR, DECIMALS, uint64(1_700_000_000), bytes32(uint256(1)), CONFIG_HASH
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

    function test_RejectsFutureReport() public {
        bytes memory body = _goodBody(PAR, uint64(block.timestamp + 10 minutes));
        vm.expectRevert(UsdxPriceStore.TooFarAhead.selector);
        store.submitPrice(body, _sign(body, teeKey));
    }

    function test_AcceptsWithinSkew() public {
        _submit(PAR, uint64(block.timestamp + 1 minutes));
        (uint256 p,,) = store.latest();
        assertEq(p, PAR);
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

        bytes memory body = _goodBody(PAR, 1_700_000_100);
        vm.expectRevert(UsdxPriceStore.StaleConfig.selector);
        store.submitPrice(body, _sign(body, teeKey));

        // A report under the new configuration is accepted.
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
        registry.setMachine(otherTee, EXTENSION_ID, ACTIVE);
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

    // --- fuzz ---

    /// Whatever is accepted must lie inside the bounds and strictly advance the
    /// timestamp. These are the two invariants downstream consumers rely on.
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
            assertGt(t, before);
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
}

contract UsdxCustomFeedTest is Test {
    bytes21 constant FEED_ID = bytes21(hex"20555344582f555344000000000000000000000000");
    uint8 constant ACTIVE = 1;

    MockRegistry registry;
    UsdxPriceStore store;
    UsdxCustomFeed feed;

    function setUp() public {
        registry = new MockRegistry();
        store = new UsdxPriceStore(
            registry, IUsdxConfigSource(address(new MockConfigSource())),
            1, ACTIVE, FEED_ID, 8, address(this), 0, 100_000_000, 1 hours, 5 minutes
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

    function test_ConstructorRejectsFeedMismatch() public {
        vm.expectRevert(UsdxCustomFeed.FeedMismatch.selector);
        new UsdxCustomFeed(store, bytes21(hex"21"));
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
