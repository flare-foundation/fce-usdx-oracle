// SPDX-License-Identifier: MIT
// Vendored from flare-smart-contracts-v2 @ 796784e6 by scripts/vendor-interfaces.sh.
// Only the import paths are rewritten, to point at the siblings in this
// directory. Do NOT edit the declarations: abigen derives the Go structs from
// them, and the enclave's codec in go/internal/provisioning/store plus the publisher's
// in tools/pkg/oraclepayload are both pinned to the same layout.

pragma solidity >=0.7.6 <0.9;

/// Signature structure
struct Signature {
    uint8 v;
    bytes32 r;
    bytes32 s;
}