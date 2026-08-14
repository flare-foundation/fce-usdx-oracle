// SPDX-License-Identifier: MIT
pragma solidity >=0.7.6 <0.9;

/// @notice The subset of UsdxOracleInstructionSender that UsdxPriceStore reads.
/// @dev Narrow by design — importing the sender would couple the two
/// deployments more than this one check needs.
interface IUsdxConfigSource {
    /// @notice keccak256 of the endpoint configuration last published for a TEE
    /// machine, or zero if none has been.
    function expectedConfigHash(address _teeId) external view returns (bytes32);

    /// @notice keccak256 of the admin signer set last published for a TEE
    /// machine, or zero if none has been.
    function expectedAdminsHash(address _teeId) external view returns (bytes32);
}
