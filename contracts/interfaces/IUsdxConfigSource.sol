// SPDX-License-Identifier: MIT
pragma solidity >=0.7.6 <0.9;

/// @notice The subset of UsdxOracleInstructionSender that UsdxPriceStore reads.
///
/// @dev A narrow interface rather than importing the sender directly: the store
/// only needs to know which configuration was published for a machine, and
/// depending on the whole contract would couple the two deployments more than
/// the check requires.
interface IUsdxConfigSource {
    /// @notice keccak256 of the endpoint configuration last published for a TEE
    /// machine, or zero if none has been.
    function expectedConfigHash(address _teeId) external view returns (bytes32);
}
