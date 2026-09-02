// SPDX-License-Identifier: MIT
// Vendored from flare-smart-contracts-v2 @ 796784e6 by scripts/vendor-interfaces.sh.
// Only the import paths are rewritten, to point at the siblings in this
// directory. Do NOT edit the declarations: abigen derives the Go structs from
// them, and the enclave's codec in go/internal/provisioning/store plus the publisher's
// in tools/pkg/oraclepayload are both pinned to the same layout.

pragma solidity >=0.8.4 <0.9;

import { ITeeOracleFeedStore } from "./ITeeOracleFeedStore.sol";
import {
    ITeeOracleInstructionsSender
} from "./ITeeOracleInstructionsSender.sol";

/**
 * @title TeeOracleStructs
 * @notice ABI-only re-export of the TEE oracle payload structs so off-chain tooling can
 *         look up their layout via the compiled artifact (same pattern as the TEE structs
 *         interfaces). Not deployed; not called.
 */
interface TeeOracleStructs {

    function feedUpdateStruct(ITeeOracleFeedStore.FeedUpdate calldata) external;

    function feedUpdateRequestStruct(ITeeOracleInstructionsSender.FeedUpdateRequest calldata) external;

    function endpointsStruct(ITeeOracleInstructionsSender.Endpoints calldata) external;

    function endpointGroupStruct(ITeeOracleInstructionsSender.EndpointGroup calldata) external;

    function endpointStruct(ITeeOracleInstructionsSender.Endpoint calldata) external;

    function adminsStruct(ITeeOracleInstructionsSender.Admins calldata) external;

    function adminRoleStruct(ITeeOracleInstructionsSender.AdminRole calldata) external;
}
