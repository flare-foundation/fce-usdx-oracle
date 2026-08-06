// SPDX-License-Identifier: MIT
pragma solidity >=0.7.6 <0.9;

// TODO: Replace this minimal interface with the full import once flare-smart-contracts-v2
// is published as a package:
//   import { ITeeMachineRegistry } from "flare-smart-contracts-v2/contracts/userInterfaces/tee/ITeeMachineRegistry.sol";
//
// The three methods below are the ones this repo uses. Signatures confirmed
// against the MachineManager ABI in
// go-flare-common/pkg/contracts/tee/machinemanager.
interface ITeeMachineRegistry {
    /// @notice A TEE machine's lifecycle status.
    /// @dev Verbatim from flare-smart-contracts-v2 IMachineManager.sol; restated
    /// because the ABI types it as a bare uint8, losing the values. Only
    /// PRODUCTION accepts instructions, and NONE is what a stranger reports.
    enum TeeStatus {
        NONE,
        INITIALIZED,
        PRODUCTION,
        SUSPENDED,
        PAUSED,
        BANNED
    }

    function getRandomTeeIds(uint256 _extensionId, uint256 _count)
        external view returns (address[] memory);

    /// @notice Returns the extension a TEE machine is registered to.
    /// @dev Returns 0 for an unknown machine; the store rejects either way.
    function getExtensionId(address _teeId) external view returns (uint256);

    /// @notice Returns a TEE machine's lifecycle status.
    /// @dev uint8 rather than TeeStatus, to match the published ABI exactly.
    function getTeeMachineStatus(address _teeId) external view returns (uint8);
}
