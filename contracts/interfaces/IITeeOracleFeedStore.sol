// SPDX-License-Identifier: MIT
// Vendored from flare-smart-contracts-v2 @ 796784e6 by scripts/vendor-interfaces.sh.
// Only the import paths are rewritten, to point at the siblings in this
// directory. Do NOT edit the declarations: abigen derives the Go structs from
// them, and the enclave's codec in go/internal/provisioning/store plus the publisher's
// in tools/pkg/oraclepayload are both pinned to the same layout.

pragma solidity >=0.8.4 <0.9;

import { ITeeOracleFeedStore } from "./ITeeOracleFeedStore.sol";

/**
 * @title IITeeOracleFeedStore
 * @notice Internal interface for the TEE oracle feed store — adds the
 *         Flare-governance-only methods on top of the public interface.
 */
interface IITeeOracleFeedStore is ITeeOracleFeedStore {

    /**
     * Updates the destination address collected read fees are forwarded to.
     * Only governance can call this method.
     * @param _feeDestination The fee destination address; must be non-zero.
     */
    function setFeeDestination(
        address _feeDestination
    )
        external;
}
