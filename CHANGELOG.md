# Changelog

Each release has a `## [vX.Y.Z]` section, newest first. When a `v*` tag is pushed,
`.github/workflows/release.yml` publishes the section whose heading names that tag as
the GitHub release notes, so move the entries out of `[Unreleased]` under the new
heading before tagging.

## [Unreleased]

### Added

- `send-secret signers`: recovers each signature on an envelope offline, names it against the
  admin set that governs the ref, and fails below that set's threshold.
- `--build-only` on `upload-rpc-keys.sh` and `upload-hex-key.sh`: a coordinator who holds the
  credentials but no admin key encrypts each one to the machine and writes the envelope out
  unsigned, to `deploy/envelopes/<teeId>-<ref>.json`.
- `check-endpoints.sh`: tries every provider in an endpoint configuration with the credential
  that would be installed for it, before the configuration is published.
- The Songbird deployment's endpoint configuration, admin sets and contract addresses.
- GitHub workflows: the extension image built reproducibly on every tag and published to
  `ghcr.io/flare-foundation/fce-usdx-oracle`, with its code hash in the job summary; and a GitHub
  release for every `v*` tag, from this file.

### Changed

- `verify-tee` and `fleet-status` run without a signing key.
- `upload-rpc-keys.sh` leaves `hex_cash_url` to `upload-hex-key.sh`, as it already did
  `hex_api_key`: both are backing credentials.
- `use-chain.sh` no longer writes admin keys into `.env` for a chain that already has an admins
  file.
