// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {Script, console2} from "forge-std/Script.sol";
import {IGovernanceSettings} from
    "@flarenetwork/flare-periphery-contracts/flare/IGovernanceSettings.sol";
import {TeeOracleInstructionsSender} from
    "flare-smart-contracts-v2/tee/extensions/oracle/implementation/TeeOracleInstructionsSender.sol";
import {TeeOracleInstructionsSenderProxy} from
    "flare-smart-contracts-v2/tee/extensions/oracle/proxy/TeeOracleInstructionsSenderProxy.sol";
import {TeeOracleFeedStore} from
    "flare-smart-contracts-v2/tee/extensions/oracle/implementation/TeeOracleFeedStore.sol";
import {TeeOracleFeedStoreProxy} from
    "flare-smart-contracts-v2/tee/extensions/oracle/proxy/TeeOracleFeedStoreProxy.sol";
import {ITeeOracleFeedStore} from
    "flare-smart-contracts-v2/userInterfaces/tee/ITeeOracleFeedStore.sol";
import {ITeeOracleInstructionsSender} from
    "flare-smart-contracts-v2/userInterfaces/tee/ITeeOracleInstructionsSender.sol";
import {UsdxTeeExtensionStateVerifier} from "../src/UsdxTeeExtensionStateVerifier.sol";

/**
 * Deploys the TEE oracle contracts deployer-governed: the way upstream's
 * deployment/scripts/DeployTeeOracle.s.sol deploys them, minus its last step.
 * The sender and the feed store stay governed by the deployer instead of being
 * switched to production mode, so a publication executes in the transaction
 * that submits it rather than after GovernanceSettings' timelock, and the
 * deployer alone runs the whole flow. Production mode is one-way, so this is a
 * deploy-time choice, made for the simulation and for test deployments on a
 * testnet; a production deployment uses upstream's script.
 *
 * Everything else matches upstream's configuration (deployment/chain-config):
 * the USDX/USD feed with its submission policy and the fee destination. The
 * extension id comes from the environment, since a test deployment may run
 * under a public id the registry minted rather than the reserved 1. The
 * contracts are upstream's own sources, compiled from the sibling checkout,
 * plus this repository's one contract, the extension's state verifier, which the
 * registry consults on every availability check now that tee-node reports the
 * extension's state.
 *
 * Inputs come from the environment, set by deploy/scripts/deploy-oracle.sh:
 * DEPLOYER_PRIVATE_KEY, EXTENSION_ID (default 1), and the network's
 * GOVERNANCE_SETTINGS, ADDRESS_UPDATER, FLARE_TEE_MANAGER, FDC2_VERIFICATION and
 * FEE_CALCULATOR addresses. The output is the "DEPLOYED: <name>, <contract>:
 * <address>" lines upstream's script prints, which the shell turns into the
 * addresses file.
 */
contract DeployTeeOracle is Script {
    string internal constant FEED_REGISTRY_NAME = "UsdxFeedStore";
    uint8 internal constant FEED_CATEGORY = 0x20;
    bytes20 internal constant FEED_NAME = bytes20("USDX/USD");
    uint8 internal constant REQUIRED_SIGNATURES = 1;
    uint16 internal constant MAX_SPREAD_BIPS = 100;
    uint64 internal constant MAX_SPREAD_ABSOLUTE = 0;
    address internal constant FEE_DESTINATION = 0x000000000000000000000000000000000000dEaD;

    function run() external {
        address deployer = vm.addr(vm.envUint("DEPLOYER_PRIVATE_KEY"));
        uint256 extensionId = vm.envOr("EXTENSION_ID", uint256(1));
        address governanceSettings = vm.envAddress("GOVERNANCE_SETTINGS");
        address addressUpdater = vm.envAddress("ADDRESS_UPDATER");
        address flareTeeManager = vm.envAddress("FLARE_TEE_MANAGER");
        address fdc2Verification = vm.envAddress("FDC2_VERIFICATION");
        address feeCalculator = vm.envAddress("FEE_CALCULATOR");

        // bytes21 feed id: the category byte followed by the right-padded name.
        bytes21 feedId = bytes21(
            (uint168(FEED_CATEGORY) << 160) | uint168(uint160(FEED_NAME))
        );

        vm.startBroadcast();

        // The deployer is initial governance and address updater; the wiring
        // below hands the updater role to the real AddressUpdater. Governance
        // stays with the deployer: no switchToProductionMode.
        TeeOracleInstructionsSender senderImpl = new TeeOracleInstructionsSender();
        _logDeployed(
            "TeeOracleInstructionsSenderImplementation",
            "TeeOracleInstructionsSender",
            address(senderImpl)
        );
        TeeOracleInstructionsSender sender = TeeOracleInstructionsSender(
            address(
                new TeeOracleInstructionsSenderProxy(
                    IGovernanceSettings(governanceSettings),
                    deployer,
                    deployer,
                    extensionId,
                    address(senderImpl)
                )
            )
        );
        _logDeployed(
            "TeeOracleInstructionsSender", "TeeOracleInstructionsSender", address(sender)
        );

        TeeOracleFeedStore storeImpl = new TeeOracleFeedStore();
        _logDeployed(
            "TeeOracleFeedStoreImplementation", "TeeOracleFeedStore", address(storeImpl)
        );
        TeeOracleFeedStore store = TeeOracleFeedStore(
            address(
                new TeeOracleFeedStoreProxy(
                    IGovernanceSettings(governanceSettings),
                    deployer,
                    deployer,
                    ITeeOracleInstructionsSender(address(sender)),
                    feedId,
                    FEE_DESTINATION,
                    ITeeOracleFeedStore.SubmissionPolicy({
                        requiredSignatures: REQUIRED_SIGNATURES,
                        maxSpreadBIPS: MAX_SPREAD_BIPS,
                        maxSpreadAbsolute: MAX_SPREAD_ABSOLUTE
                    }),
                    address(storeImpl)
                )
            )
        );
        _logDeployed(FEED_REGISTRY_NAME, "TeeOracleFeedStore", address(store));

        bytes32[] memory senderNames = new bytes32[](2);
        senderNames[0] = _encodeContractName("AddressUpdater");
        senderNames[1] = _encodeContractName("FlareTeeManager");
        address[] memory senderAddrs = new address[](2);
        senderAddrs[0] = addressUpdater;
        senderAddrs[1] = flareTeeManager;
        sender.updateContractAddresses(senderNames, senderAddrs);

        bytes32[] memory storeNames = new bytes32[](3);
        storeNames[0] = _encodeContractName("AddressUpdater");
        storeNames[1] = _encodeContractName("Fdc2Verification");
        storeNames[2] = _encodeContractName("FeeCalculator");
        address[] memory storeAddrs = new address[](3);
        storeAddrs[0] = addressUpdater;
        storeAddrs[1] = fdc2Verification;
        storeAddrs[2] = feeCalculator;
        store.updateContractAddresses(storeNames, storeAddrs);

        // The extension owner publishes this address with setExtensionContracts
        // (tools/cmd/wire-extension reads it from the addresses file).
        UsdxTeeExtensionStateVerifier verifier = new UsdxTeeExtensionStateVerifier();
        _logDeployed(
            "UsdxTeeExtensionStateVerifier", "UsdxTeeExtensionStateVerifier", address(verifier)
        );

        vm.stopBroadcast();
    }

    // AddressUpdater keys contracts by the hash of the ABI-encoded name.
    function _encodeContractName(string memory _name) internal pure returns (bytes32) {
        return keccak256(abi.encode(_name));
    }

    function _logDeployed(string memory _name, string memory _contractName, address _addr)
        internal
        pure
    {
        console2.log(
            string.concat("DEPLOYED: ", _name, ", ", _contractName, ": ", vm.toString(_addr))
        );
    }
}
