# Wire fixtures

Solidity's own encoding of the payloads that cross the extension boundary,
produced by the Foundry vector tests in `contracts/test/`. Regenerate with:

    ./deploy/scripts/regen-wire-fixtures.sh

The Go codecs in `pkg/wire` are asserted against these bytes, in both
directions where both exist: the enclave decodes what the contract dispatched,
and the publishing tools encode to predict the hash the contract will record.
There is one implementation of each format and the tooling imports it, so
these files are the independent reference: Solidity's encoding, not a second
Go encoder. That matters most for the two configuration payloads, which are
ABI-DYNAMIC. There is no single valid encoding for a given configuration, and
an encoder that differed from the contract's by one padding word would record a
hash no machine can ever satisfy; `submitFeedUpdates` would then revert
`StaleEndpoints` forever, with nothing in the logs pointing at the encoder.

`endpoints-v7.abi.hex` is `abi.encode(Endpoints{version: 7, feedId, groups})`
for the four-group fixture (flare, ethereum, songbird, and a PRIVATE hex_cash
committing to a salted URL). `admins-v3.abi.hex` is
`abi.encode(Admins{version: 3, feedId, roles})` for 1-of-1 backing and 2-of-2
providers. `feed-update.abi.hex`, `feed-update-payload-preimage.abi.hex` and
`feed-update-request.abi.hex` are the signed feed update, the preimage its
signature covers, and the GET_FEED request message.
