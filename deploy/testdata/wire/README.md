# Wire fixtures

Solidity's own encoding of the payloads that cross the extension boundary, printed by the Foundry
vector tests in `contracts/test/` (`ConfigPayloadVector.t.sol`, `FeedUpdateVector.t.sol`). The
structs come from upstream's interfaces, so these bytes are the contract's encoding, not a second
Go encoder's. There is one Go implementation of each format and the tooling imports it, so these
files are the independent reference.

That matters most for the two configuration payloads, which are ABI-DYNAMIC. There is no single
valid encoding for a given configuration, and an encoder that differed from the contract's by one
padding word would predict a hash no machine can ever satisfy: `submitFeedUpdates` would then
revert `StaleEndpoints` forever, with nothing in the logs pointing at the encoder.

| File | Bytes | Checked by |
| --- | --- | --- |
| `endpoints-v7.abi.hex` | `abi.encode(Endpoints{version: 7, feedId, groups})` for the four-group fixture: flare, ethereum, songbird, and a PRIVATE `hex_cash` endpoint committing to a salted URL | read by `pkg/wire/configpayload` (the publisher's encoder, `TestEncodeEndpointsMatchesSolidity`) and `internal/provisioning/store` (the enclave's decoder, `TestDecodeEndpointsMatchesSolidity`) |
| `admins-v3.abi.hex` | `abi.encode(Admins{version: 3, feedId, roles})`, 1-of-1 `backing` and 2-of-2 `providers` | read by the same two packages (`TestEncodeAdminsMatchesSolidity`, `TestDecodeAdminsMatchesSolidity`) |
| `feed-update-request.abi.hex` | `abi.encode(FeedUpdateRequest{feedId})`, the `GET_FEED` instruction message: one word | read by `pkg/wire/feedupdate` (`TestFeedRequestMatchesSolidity`) |
| `feed-update.abi.hex` | `abi.encode(FeedUpdate)`, the **unsigned** update: seven words. Not the action result, which appends the signature | no Go test reads it. Its keccak256 is pinned as `vectorDataHash` in `pkg/wire/feedupdate/feedupdate_test.go` |
| `feed-update-payload-preimage.abi.hex` | `abi.encode(bytes32("TEE_ORACLE_FEED"), chainId, dataHash)`, the `SignedPayload` preimage the machine's signature covers: three words | no Go test reads it. Its keccak256 is pinned as `vectorMessageHash` in the same file, beside the EIP-191 digest (`vectorDigest`, also pinned in `tools/pkg/feedupdate`) |

The vector tests print further values that Go pins inline rather than reading a file: the salt,
the cash-URL commitment and the two configuration hashes (in both `pkg/wire/configpayload` and
`internal/provisioning/store`), and the feed-update hashes above.

## Regenerating

```bash
./deploy/scripts/regen-wire-fixtures.sh
```

It runs `forge test --match-path 'test/*Vector.t.sol' -vv` in `contracts/` and writes the five
files from the labelled values in the output. Forge compiles the whole Foundry project, so it needs
what `forge test` needs:

- `forge` on the `PATH`;
- the sibling checkout of `flare-smart-contracts-v2` (branch `tee-oracle-extension`) at
  `../../../fsp/flare-smart-contracts-v2` from the repository root, the path the remappings in
  `contracts/foundry.toml` name, with its Soldeer dependencies installed
  (`forge soldeer install` in that checkout);
- forge-std in the git-ignored `contracts/lib/`:
  `cd contracts && forge install --no-git foundry-rs/forge-std`.

The script rewrites only the five files. If a feed-update or configuration vector changed, update
the inline constants above from the same output (`DATA_HASH`, `MESSAGE_HASH`, `DIGEST`, `SALT`,
`COMMITMENT`, `ENDPOINTS_HASH`, `ADMINS_HASH`), then run `GOWORK=off go test ./...` at the root
and in `tools/`. A vector that changed means the wire format changed: check that the store was
redeployed before assuming the Go side is what is wrong.
