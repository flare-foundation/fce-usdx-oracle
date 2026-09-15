// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import { Script, console2 } from "forge-std/Script.sol";
import { UsdxTeeExtensionStateVerifier } from "../src/UsdxTeeExtensionStateVerifier.sol";

/**
 * Deploys the extension's state verifier on its own, on any network. The
 * simulation deploy (DeployTeeOracle) includes it; this is for a testnet
 * or mainnet, where the oracle contracts come from upstream's deploy and the
 * verifier is the one contract this repository adds, and for a simulation
 * whose oracle contracts predate it.
 *
 * The verifier has no owner and no configuration. The extension owner then
 * publishes its address with setExtensionContracts (tools/cmd/wire-extension).
 *
 *   forge script script/DeployStateVerifier.s.sol:DeployStateVerifier \
 *       --rpc-url <RPC> --private-key <KEY> --broadcast
 *
 * The output is the "DEPLOYED: <name>, <contract>: <address>" line the
 * addresses importer reads.
 */
contract DeployStateVerifier is Script {
    function run() external {
        vm.startBroadcast();
        UsdxTeeExtensionStateVerifier verifier = new UsdxTeeExtensionStateVerifier();
        vm.stopBroadcast();

        console2.log(
            string.concat(
                "DEPLOYED: UsdxTeeExtensionStateVerifier, UsdxTeeExtensionStateVerifier: ",
                vm.toString(address(verifier))
            )
        );
    }
}
