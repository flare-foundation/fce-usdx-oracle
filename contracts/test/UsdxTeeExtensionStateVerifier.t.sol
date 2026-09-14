// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { Test } from "forge-std/Test.sol";
import { UsdxTeeExtensionStateVerifier } from "../src/UsdxTeeExtensionStateVerifier.sol";

// The registry consults the verifier on every availability check, so each of
// these is a machine reaching or keeping production, or not.
contract UsdxTeeExtensionStateVerifierTest is Test {
    UsdxTeeExtensionStateVerifier internal verifier;
    address internal constant TEE = address(0x1234);

    // The extension's version string as tee-node carries it: bytes32("0.1.0").
    bytes32 internal constant VERSION = bytes32(bytes("0.1.0"));

    function setUp() public {
        verifier = new UsdxTeeExtensionStateVerifier();
    }

    function testAcceptsAReportedState() public view {
        assertTrue(verifier.verifyTeeState(TEE, VERSION, bytes('{"lastPrice":"100000000"}')));
    }

    function testAcceptsAnyVersionWithBytes() public view {
        // The verifier pins no schema: a new build's version passes as long as
        // it reports something.
        assertTrue(verifier.verifyTeeState(TEE, bytes32(bytes("9.9.9")), bytes("x")));
    }

    function testAcceptsNoStateFromANodeThatDoesNotFoldItIn() public view {
        // The zero state, as the registry itself accepts for an extension with
        // no verifier, so an older tee-node still passes.
        assertTrue(verifier.verifyTeeState(TEE, bytes32(0), bytes("")));
    }

    function testRejectsTheUnavailableSentinel() public view {
        // tee-node could not read its extension: not available.
        assertFalse(verifier.verifyTeeState(TEE, bytes32(type(uint256).max), bytes("")));
        // Even with bytes attached, the version alone decides.
        assertFalse(verifier.verifyTeeState(TEE, bytes32(type(uint256).max), bytes("{}")));
    }

    function testRejectsAVersionWithoutBytes() public view {
        assertFalse(verifier.verifyTeeState(TEE, VERSION, bytes("")));
    }

    function testRejectsBytesWithoutAVersion() public view {
        assertFalse(verifier.verifyTeeState(TEE, bytes32(0), bytes("{}")));
    }

    function testIsIndifferentToTheMachine() public view {
        assertTrue(verifier.verifyTeeState(address(0), VERSION, bytes("{}")));
        assertTrue(verifier.verifyTeeState(address(0xdead), VERSION, bytes("{}")));
    }
}
