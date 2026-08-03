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
    function getRandomTeeIds(uint256 _extensionId, uint256 _count)
        external view returns (address[] memory);

    /// @notice Returns the extension a TEE machine is registered to.
    /// @dev Used by UsdxPriceStore to bind a signer to this extension. Reverts
    /// or returns 0 for an unknown machine, depending on the implementation —
    /// the store treats a mismatch as rejection either way.
    function getExtensionId(address _teeId) external view returns (uint256);

    /// @notice Returns a TEE machine's lifecycle status.
    /// @dev The enum's numeric values are NOT visible from the published Go
    /// bindings (the ABI types it as a bare uint8). UsdxPriceStore therefore
    /// takes the expected value as a constructor parameter rather than
    /// hard-coding a guess — see REQUIRED_TEE_STATUS.
    function getTeeMachineStatus(address _teeId) external view returns (uint8);
}
