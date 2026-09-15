// SPDX-License-Identifier: MIT
pragma solidity >=0.8.4 <0.9;

// The upstream interfaces this repository generates Go bindings for. Importing
// them here puts them in forge's compilation graph, which is how
// deploy/scripts/generate-bindings.sh finds their ABI under out/. Nothing in
// this file is deployed.
import "flare-smart-contracts-v2/tee/extensions/oracle/interface/IITeeOracleInstructionsSender.sol";
import "flare-smart-contracts-v2/tee/extensions/oracle/interface/IITeeOracleFeedStore.sol";
import "flare-smart-contracts-v2/tee/structs/TeeOracleStructs.sol";
