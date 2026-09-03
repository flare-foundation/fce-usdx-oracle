// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { Test, console2 } from "forge-std/Test.sol";
import {
    ITeeOracleInstructionsSender
} from "flare-smart-contracts-v2/userInterfaces/tee/ITeeOracleInstructionsSender.sol";

/**
 * Prints Solidity's own encoding of the three payloads the extension has to read
 * or the publisher has to produce, into deploy/testdata/wire.
 *
 * The structs are IMPORTED from the upstream interface rather than re-declared,
 * so this fixture cannot drift from the ABI the bindings are generated off.
 * Both Go codecs — the enclave's in internal/provisioning/store and the publisher's
 * in tools/pkg/oraclepayload — assert against the bytes printed here.
 *
 * Endpoints and Admins are ABI-DYNAMIC, so there is no canonical encoding to
 * fall back on: two encoders can disagree and both look reasonable. That is the
 * whole reason these fixtures exist.
 */
contract ConfigPayloadVectorTest is Test {
    // The USDX feed: custom category 0x20 followed by "USDX/USD", zero-padded to
    // 20 bytes. Same id as the feed-update vector.
    bytes21 constant FEED_ID = bytes21(hex"20555344582f555344000000000000000000000000");

    string constant CASH_URL =
        "https://api.htdigitalassets.com/alm-stablecoin-db/restricted/metrics/current_cash_reserves_amount";

    function testPrintEndpoints() public pure {
        bytes32 salt = keccak256("salt");
        bytes32 commitment = keccak256(abi.encode(salt, CASH_URL));

        ITeeOracleInstructionsSender.EndpointGroup[] memory groups =
            new ITeeOracleInstructionsSender.EndpointGroup[](4);

        ITeeOracleInstructionsSender.Endpoint[] memory flare =
            new ITeeOracleInstructionsSender.Endpoint[](2);
        flare[0] = ITeeOracleInstructionsSender.Endpoint({
            kind: ITeeOracleInstructionsSender.EndpointKind.PUBLIC,
            url: "https://flare-api.flare.network/ext/C/rpc",
            urlHash: bytes32(0),
            secretRef: ""
        });
        flare[1] = ITeeOracleInstructionsSender.Endpoint({
            kind: ITeeOracleInstructionsSender.EndpointKind.PUBLIC,
            url: "https://rpc.ankr.com/flare/{secret}",
            urlHash: bytes32(0),
            secretRef: "ankr_flare_key"
        });
        groups[0] = ITeeOracleInstructionsSender.EndpointGroup({
            group: bytes32("flare"), threshold: 2, endpoints: flare
        });

        ITeeOracleInstructionsSender.Endpoint[] memory ethereum =
            new ITeeOracleInstructionsSender.Endpoint[](1);
        ethereum[0] = ITeeOracleInstructionsSender.Endpoint({
            kind: ITeeOracleInstructionsSender.EndpointKind.PUBLIC,
            url: "https://eth.example/rpc",
            urlHash: bytes32(0),
            secretRef: ""
        });
        groups[1] = ITeeOracleInstructionsSender.EndpointGroup({
            group: bytes32("ethereum"), threshold: 1, endpoints: ethereum
        });

        ITeeOracleInstructionsSender.Endpoint[] memory songbird =
            new ITeeOracleInstructionsSender.Endpoint[](1);
        songbird[0] = ITeeOracleInstructionsSender.Endpoint({
            kind: ITeeOracleInstructionsSender.EndpointKind.PUBLIC,
            url: "https://sgb.example/rpc",
            urlHash: bytes32(0),
            secretRef: ""
        });
        groups[2] = ITeeOracleInstructionsSender.EndpointGroup({
            group: bytes32("songbird"), threshold: 1, endpoints: songbird
        });

        ITeeOracleInstructionsSender.Endpoint[] memory cash =
            new ITeeOracleInstructionsSender.Endpoint[](1);
        cash[0] = ITeeOracleInstructionsSender.Endpoint({
            kind: ITeeOracleInstructionsSender.EndpointKind.PRIVATE,
            url: "",
            urlHash: commitment,
            secretRef: "hex_cash_url"
        });
        groups[3] = ITeeOracleInstructionsSender.EndpointGroup({
            group: bytes32("hex_cash"), threshold: 1, endpoints: cash
        });

        bytes memory message = abi.encode(ITeeOracleInstructionsSender.Endpoints({
            version: 7, feedId: FEED_ID, groups: groups
        }));

        console2.log("SALT");
        console2.logBytes32(salt);
        console2.log("COMMITMENT");
        console2.logBytes32(commitment);
        console2.log("ENDPOINTS_HASH");
        console2.logBytes32(keccak256(message));
        console2.log("ENDPOINTS");
        console2.logBytes(message);
    }

    function testPrintAdmins() public pure {
        ITeeOracleInstructionsSender.AdminRole[] memory roles =
            new ITeeOracleInstructionsSender.AdminRole[](2);

        address[] memory backing = new address[](1);
        backing[0] = address(0x1111111111111111111111111111111111111111);
        roles[0] = ITeeOracleInstructionsSender.AdminRole({
            role: bytes32("backing"), threshold: 1, admins: backing
        });

        address[] memory providers = new address[](2);
        providers[0] = address(0x2222222222222222222222222222222222222222);
        providers[1] = address(0x3333333333333333333333333333333333333333);
        roles[1] = ITeeOracleInstructionsSender.AdminRole({
            role: bytes32("providers"), threshold: 2, admins: providers
        });

        bytes memory message = abi.encode(ITeeOracleInstructionsSender.Admins({
            version: 3, feedId: FEED_ID, roles: roles
        }));

        console2.log("ADMINS_HASH");
        console2.logBytes32(keccak256(message));
        console2.log("ADMINS");
        console2.logBytes(message);
    }

    /// The GET_FEED instruction message. Replaces the old one-byte 0x01 envelope.
    function testPrintFeedUpdateRequest() public pure {
        bytes memory message = abi.encode(
            ITeeOracleInstructionsSender.FeedUpdateRequest({ feedId: FEED_ID })
        );

        // One static field, so the tuple encodes inline: a single word, no
        // offset, and identical to abi.encode(feedId) on its own.
        assertEq(message.length, 32, "FeedUpdateRequest is not one word");
        assertEq(message, abi.encode(FEED_ID), "FeedUpdateRequest does not encode inline");

        console2.log("FEED_UPDATE_REQUEST");
        console2.logBytes(message);
    }
}
