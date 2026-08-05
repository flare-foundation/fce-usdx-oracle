// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { IICustomFeed } from "./interfaces/IICustomFeed.sol";
import { UsdxPriceStore } from "./UsdxPriceStore.sol";

/// @title UsdxCustomFeed
/// @author Flare Foundation
/// @notice FTSO custom feed adapter over UsdxPriceStore.
/// @dev Thin read adapter so upgrades land in the store or extension; primary feed with no underlying FTSO source.
contract UsdxCustomFeed is IICustomFeed {
    /// @notice Price store this feed reads.
    UsdxPriceStore public immutable STORE;

    /// @notice Feed id returned to FtsoV2 at registration; sole authority on the id.
    bytes21 public immutable FEED_ID;

    error ZeroAddress();
    error ZeroFeedId();

    constructor(UsdxPriceStore _store, bytes21 _feedId) {
        if (address(_store) == address(0)) revert ZeroAddress();
        // FtsoV2 would reject this too, but only at a timelocked proposal.
        // Category (32..63) is left to FtsoV2 and the deploy tooling.
        if (_feedId == bytes21(0)) revert ZeroFeedId();

        STORE = _store;
        FEED_ID = _feedId;
    }

    /// @inheritdoc IICustomFeed
    /// @dev Must never revert — stale or paused prices are returned with their true timestamp.
    function getCurrentFeed() external payable returns (uint256 _value, int8 _decimals, uint64 _timestamp) {
        return STORE.latest();
    }

    /// @inheritdoc IICustomFeed
    function feedId() external view returns (bytes21 _feedId) {
        return FEED_ID;
    }

    /// @inheritdoc IICustomFeed
    /// @dev Zero — no underlying FTSO read to charge for.
    function calculateFee() external view returns (uint256 _fee) {
        return 0;
    }
}
