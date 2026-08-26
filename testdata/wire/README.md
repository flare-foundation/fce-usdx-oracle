# Wire fixtures

Solidity's own encoding of the two configuration payloads, produced by
`test/ConfigPayloadVector.t.sol`. Regenerate with:

    forge test --match-path test/ConfigPayloadVector.t.sol -vv

Two independent encoders are asserted against these bytes:

- `go/internal/runtimecfg` — the enclave's decoder, which must read what the
  contract dispatched.
- `tools/pkg/oraclepayload` — the publisher's encoder, which must predict the
  hash the contract will record.

They are kept separate on purpose, so the tooling cross-checks the extension
rather than mirroring it. These files are what stops the two drifting: both
payloads are ABI-DYNAMIC, so there is no single valid encoding for a given
configuration, and an encoder that differs by one padding word records a hash
no machine can ever satisfy — `submitFeedUpdate` then reverts `StaleEndpoints`
forever, with nothing in the logs pointing at the encoder.

`endpoints-v7.abi.hex` is `abi.encode(Endpoints{version: 7, groups})` for the
four-group fixture (flare, ethereum, songbird, and a PRIVATE hex_cash
committing to a salted URL). `admins-v3.abi.hex` is
`abi.encode(Admins{version: 3, roles})` for 1-of-1 backing and 2-of-2 providers.
