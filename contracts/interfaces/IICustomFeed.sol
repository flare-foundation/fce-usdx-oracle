// SPDX-License-Identifier: MIT
pragma solidity >=0.7.6 <0.9;

// TODO: Replace with the import once flare-smart-contracts-v2 ships as a package:
//   import { IICustomFeed } from "flare-smart-contracts-v2/contracts/customFeeds/interface/IICustomFeed.sol";
//
// Reproduced verbatim from that file. The whole interface is three functions.

/// @notice A custom feed served through FtsoV2.
interface IICustomFeed {
    /// @notice Returns the current value, its decimals, and the observation time.
    /// @dev `payable` and NOT `view`. FtsoV2.getFeedById forwards msg.value
    /// straight through, so an implementation cannot narrow the mutability even
    /// if it only reads storage.
    function getCurrentFeed() external payable returns (uint256 _value, int8 _decimals, uint64 _timestamp);

    /// @notice The feed id: bytes21, being 1 category byte plus 20 name bytes.
    function feedId() external view returns (bytes21 _feedId);

    /// @notice The fee a consumer must send with getCurrentFeed().
    function calculateFee() external view returns (uint256 _fee);
}
