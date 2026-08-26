// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { Test, console2 } from "forge-std/Test.sol";
import { ITeeOracleFeedStore } from "../contracts/interfaces/ITeeOracleFeedStore.sol";
import { Signature } from "../contracts/interfaces/ISignature.sol";

// FeedUpdate and Signature come from the vendored interfaces, so this fixture
// cannot drift from the ABI the Go bindings are generated off.
//
// SignedPayload is the one thing still copied: upstream's library pulls in
// OpenZeppelin's MessageHashUtils, which this repo does not vendor. It is four
// lines and reproduced verbatim below, and testPrefixIsNotAHash plus the
// inline-encoding assertions are what keep the copy honest.
library SignedPayload {
    struct Payload {
        bytes32 prefix;
        uint256 chainId;
        bytes32 dataHash;
    }

    function messageHash(bytes32 _prefix, bytes32 _dataHash) internal view returns (bytes32) {
        return keccak256(abi.encode(Payload(_prefix, block.chainid, _dataHash)));
    }
}

bytes32 constant TEE_ORACLE_FEED = bytes32("TEE_ORACLE_FEED");

/**
 * Pins the two facts internal/report in the Go enclave relies on, and prints the
 * golden vector its unit test asserts against.
 *
 * The load-bearing claim is that a struct whose every field is static encodes
 * inline — no offset word — so abi.encode(struct) is byte-identical to
 * abi.encode(field, field, ...). That is what lets the Go side use a flat
 * abi.Arguments list instead of go-ethereum's tuple marshalling.
 */
contract FeedUpdateVectorTest is Test {
    uint256 constant CHAIN_ID = 114;

    function _vector() internal pure returns (ITeeOracleFeedStore.FeedUpdate memory) {
        return ITeeOracleFeedStore.FeedUpdate({
            extensionId: 66281,
            feedId: bytes21(hex"20555344582f555344000000000000000000000000"),
            value: int32(99950000),
            decimals: int8(8),
            observedAt: uint64(1787230941),
            endpointsHash: keccak256("endpoints"),
            adminsHash: keccak256("admins")
        });
    }

    /// abi.encode(FeedUpdate) == abi.encode(its fields, flat).
    function testStructEncodingIsFlat() public pure {
        ITeeOracleFeedStore.FeedUpdate memory u = _vector();

        assertEq(
            abi.encode(u),
            abi.encode(
                u.extensionId, u.feedId, u.value, u.decimals,
                u.observedAt, u.endpointsHash, u.adminsHash
            ),
            "FeedUpdate does not encode inline"
        );
        assertEq(abi.encode(u).length, 7 * 32, "FeedUpdate is not seven words");
    }

    /// abi.encode(FeedUpdate, Signature) == the ten fields, flat. This is the
    /// ActionResult payload the keeper relays into submitFeedUpdate.
    function testSubmissionEncodingIsFlat() public pure {
        ITeeOracleFeedStore.FeedUpdate memory u = _vector();
        Signature memory sig = Signature({
            v: 28,
            r: keccak256("r"),
            s: keccak256("s")
        });

        assertEq(
            abi.encode(u, sig),
            abi.encode(
                u.extensionId, u.feedId, u.value, u.decimals,
                u.observedAt, u.endpointsHash, u.adminsHash,
                sig.v, sig.r, sig.s
            ),
            "submission payload does not encode inline"
        );
        assertEq(abi.encode(u, sig).length, 10 * 32, "submission payload is not ten words");
    }

    /// The SignedPayload preimage is three words and carries the chain id.
    function testPayloadPreimageIsFlat() public {
        vm.chainId(CHAIN_ID);
        bytes32 dataHash = keccak256(abi.encode(_vector()));

        bytes memory preimage = abi.encode(
            SignedPayload.Payload(TEE_ORACLE_FEED, block.chainid, dataHash)
        );

        assertEq(
            preimage,
            abi.encode(TEE_ORACLE_FEED, block.chainid, dataHash),
            "SignedPayload does not encode inline"
        );
        assertEq(preimage.length, 3 * 32, "SignedPayload is not three words");
        assertEq(keccak256(preimage), SignedPayload.messageHash(TEE_ORACLE_FEED, dataHash));
    }

    /// Prefix is the ASCII string right-padded, NOT keccak256 of it.
    function testPrefixIsNotAHash() public pure {
        assertTrue(TEE_ORACLE_FEED != keccak256("TEE_ORACLE_FEED"), "prefix must not be a hash");
        assertEq(TEE_ORACLE_FEED, bytes32(bytes("TEE_ORACLE_FEED")), "prefix must be padded ascii");
    }

    function testPrintVector() public {
        vm.chainId(CHAIN_ID);
        ITeeOracleFeedStore.FeedUpdate memory u = _vector();

        bytes memory encoded = abi.encode(u);
        bytes32 dataHash = keccak256(encoded);
        bytes memory preimage = abi.encode(TEE_ORACLE_FEED, block.chainid, dataHash);
        bytes32 messageHash = keccak256(preimage);
        bytes32 digest = keccak256(abi.encodePacked("\x19Ethereum Signed Message:\n32", messageHash));

        console2.log("PREFIX");
        console2.logBytes32(TEE_ORACLE_FEED);
        console2.log("ENCODED");
        console2.logBytes(encoded);
        console2.log("DATA_HASH");
        console2.logBytes32(dataHash);
        console2.log("PAYLOAD_PREIMAGE");
        console2.logBytes(preimage);
        console2.log("MESSAGE_HASH");
        console2.logBytes32(messageHash);
        console2.log("DIGEST");
        console2.logBytes32(digest);
    }
}
