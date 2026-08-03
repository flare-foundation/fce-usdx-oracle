// SPDX-License-Identifier: MIT
pragma solidity ^0.8.27;

import { IICustomFeed } from "./interfaces/IICustomFeed.sol";
import { UsdxPriceStore } from "./UsdxPriceStore.sol";

/// @title UsdxCustomFeed
/// @author Flare Foundation
/// @notice FTSO custom feed adapter over UsdxPriceStore.
///
/// @dev Deliberately thin: a read adapter and nothing else.
///
/// `FtsoV2.replaceCustomFeeds` can repoint an existing feed id at a new contract
/// address, so this contract can be upgraded without integrators re-integrating.
/// Keeping it thin means almost every change lands in the store or the extension
/// instead, and needs no governance action at all.
///
/// # This is a primary feed, not a derivative one
///
/// The two shipped examples (SFlrCustomFeed, StXrpCustomFeed) read an underlying
/// FTSO feed from FastUpdater and rescale it. This one has no underlying feed —
/// it serves a value our own TEE produced. That is allowed (the registry only
/// requires the id be in range and the interface implemented), but it means
/// neither example can be copied wholesale: their structure transfers, their
/// logic does not.
contract UsdxCustomFeed is IICustomFeed {
    /// @notice The store this feed reads.
    UsdxPriceStore public immutable STORE;

    /// @notice This feed's id: 1 category byte plus 20 bytes of ASCII name.
    bytes21 public immutable FEED_ID;

    error ZeroAddress();
    error FeedMismatch();

    constructor(UsdxPriceStore _store, bytes21 _feedId) {
        if (address(_store) == address(0)) revert ZeroAddress();
        // The adapter and the store must agree on which feed this is, or the
        // feed would serve a price signed for a different id.
        if (_store.FEED_ID() != _feedId) revert FeedMismatch();

        STORE = _store;
        FEED_ID = _feedId;
    }

    /// @inheritdoc IICustomFeed
    ///
    /// @dev **This function must never revert.** `FtsoV2.getFeedById` forwards
    /// the call straight through, so a revert here becomes a revert in every
    /// downstream consumer simultaneously — turning a stale-data problem into a
    /// protocol-wide liveness outage.
    ///
    /// So there is no staleness check and no pause check. A stale price is
    /// returned with its true, ageing `_timestamp`, and consumers apply their
    /// own freshness policy. A paused store simply stops accepting new prices;
    /// this keeps serving the last one.
    ///
    /// It is `payable` and non-`view` because the interface says so, not because
    /// it needs to be: the implementation only reads storage. The mutability
    /// must match or the contract does not implement the interface. Consumers
    /// call it as a transaction, or `staticcall` it off-chain.
    function getCurrentFeed() external payable returns (uint256 _value, int8 _decimals, uint64 _timestamp) {
        return STORE.latest();
    }

    /// @inheritdoc IICustomFeed
    function feedId() external view returns (bytes21 _feedId) {
        return FEED_ID;
    }

    /// @inheritdoc IICustomFeed
    ///
    /// @dev Zero. The shipped examples defer to
    /// `FeeCalculator.calculateFeeByIds([referenceFeedId])` because their
    /// consumers are effectively paying for the underlying FTSO read. This feed
    /// has no underlying read to pay for, so charging would be metering access
    /// for its own sake.
    function calculateFee() external view returns (uint256 _fee) {
        return 0;
    }
}
