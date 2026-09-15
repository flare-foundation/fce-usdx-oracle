// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { ITeeExtensionStateVerifier } from
    "flare-smart-contracts-v2/userInterfaces/tee/ITeeExtensionStateVerifier.sol";

/**
 * The USDX oracle extension's state verifier, consulted by the TEE machine
 * registry on every availability check: `toProduction` and `confirmAvailability`
 * pass the machine's attested extension state through `verifyTeeState` and
 * reject the check when it returns false. Without a verifier the registry
 * accepts only an empty state, and tee-node now reports the extension's
 * `GET /state` in the TEE info, so one is required for any machine to pass.
 *
 * Minimal on purpose. The state is the extension's operational snapshot (price,
 * counters, per-source health, readiness); nothing on chain consumes it, so
 * nothing here parses it. What is checked is only what an availability check
 * should refuse:
 *
 *  - tee-node's "unavailable" sentinel, the all-ones version with no bytes,
 *    which tee-node reports when it could not read its extension. A machine
 *    whose extension is down or malformed is not available.
 *  - a version with no bytes, or bytes with no version: a malformed report.
 *
 * An empty state with the zero version — what a tee-node that does not fold
 * the extension's state in reports — passes, exactly as the registry treats it
 * for an extension with no verifier at all.
 */
contract UsdxTeeExtensionStateVerifier is ITeeExtensionStateVerifier {
    /// tee-node's StateUnavailableVersion: it could not read its extension.
    bytes32 public constant STATE_UNAVAILABLE_VERSION = bytes32(type(uint256).max);

    /// @inheritdoc ITeeExtensionStateVerifier
    function verifyTeeState(
        address,
        bytes32 _stateVersion,
        bytes calldata _state
    )
        external
        pure
        returns (bool _isValid)
    {
        if (_stateVersion == STATE_UNAVAILABLE_VERSION) {
            return false;
        }
        // Version and bytes go together: both present, or both absent.
        return (_stateVersion == bytes32(0)) == (_state.length == 0);
    }
}
