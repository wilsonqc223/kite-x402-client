# Changelog

All notable changes to this project are documented here. The format follows
[Keep a Changelog](https://keepachangelog.com/en/1.1.0/) and the project
adheres to [Semantic Versioning](https://semver.org/).

## [Unreleased]

### Added

- `kx402 pay -dry-run`: one unpaid request, then a report of what `pay`
  would sign — the option, its price in tokens and atomic units, the payee,
  and the price-cap verdict. No wallet is loaded and nothing is signed, so
  it works without a key and is safe against untrusted endpoints. Exits `3`
  when `pay` would refuse.
- `x402.Inspect`: the read-only half of `x402.Pay` for library users; `Pay`
  and `Inspect` now share one implementation of the request, option
  selection and cap check, so a dry run cannot disagree with a real one.

## [v0.2.0] - 2026-11-23

### Fixed — security

A self-review of the payment path found and fixed the following. Each fix
ships with a regression test that fails against the previous code.

- **Payments had no spending limit.** CLI and MCP signed whatever price the
  server asked for. `Pay` now refuses an option above a per-payment cap
  *before anything is signed*. The default is **$1** — far above any
  legitimate x402 per-call price, low enough that a hostile endpoint or a
  prompt-injected agent cannot drain a wallet in one request. Raise it per
  call with `kx402 pay -max-usd` (or `KITE_MAX_PAYMENT_USD`); the MCP server
  reads the cap only from its environment, so it cannot be raised from a
  tool call.
- **`payTo` and `asset` were not validated.** `common.HexToAddress` never
  fails — it silently pads or truncates — so a malformed address in a
  challenge was rewritten into a different valid-looking address, and that
  rewritten address was the one signed. Both must now be well-formed
  20-byte addresses, and the amount a non-negative decimal integer.
- **A key file other users can read was accepted.** `-key-file` now refuses
  files with group/other permission bits set and prints the `chmod 600`.

### Fixed — correctness

- The unpaid `402` response body was never closed on the paid path (or on
  two error paths), pinning connections on the pool.
- Automatic option selection used an `eip155:<digits>` format check as "a
  network this toolkit can pay on". A multi-chain challenge that listed
  another EVM chain first would select it and fail instead of stepping over
  it to the Kite option.
- MCP `kx402_pay`: an explicit `option: 0` was treated as "absent", so the
  first advertised option could not be selected on purpose.
- `PayResult.Paid` was `true` when the protected handler answered `>= 400`
  after verification. Nothing settles in that case, so it is not a paid
  request.
- `PaymentRequirements.Extra` was decoded as `map[string]string`; the x402
  schema allows number values, and one of them failed the whole challenge
  decode. It is `map[string]any` now.
- `-proxy` built a bare `http.Transport`, dropping the default transport's
  HTTP/2 support, connection pooling and handshake timeouts. It is now a
  clone of the default transport. A proxy URL without a scheme is rejected
  instead of being ignored.
- The README install snippet pointed at a bare binary, but release assets
  are `tar.gz` archives.

### Added

- **`kx402-mcp`**: the toolkit as Model Context Protocol tools over stdio
  (`kx402_probe`, `kx402_balance`, `kx402_pay`), so MCP-capable AI agents can
  use it without leaving their chat. `KITE_PROXY` applies to every tool call.
- `kx402 wallet`: the wallet's address (`-address-only` for scripting) and
  its balances on both Kite networks.
- `kx402 probe --json` and `kx402 receipt --json`: machine-readable output.
- `kx402 receipt -`: read the base64 `PAYMENT-RESPONSE` from stdin.
- `-proxy URL` on `probe`, `pay` and `balance`.

### Changed

- `cmd/kx402` and `cmd/kx402-mcp` share one implementation of the version,
  proxy-client, key-loading, probe and balance helpers in `internal/cli`
  (the CLI's local copies are gone, −203 lines).

### Verification

`go test -race` is clean; the cross-repository e2e still pays the real
[`kite-x402-axum`](https://github.com/tianzeshi-study/kite-x402-axum) service
under the default cap; the live facilitator at `facilitator.pieverse.io/v2`
still recovers the payer from the signature.

## [v0.1.0] - 2026-11-21

First release.

- `kx402 probe` — decode a `402 Payment Required` challenge and print every
  advertised payment option with a human-readable price.
- `kx402 pay` — the full client flow: challenge → EIP-3009
  `transferWithAuthorization` signature (EIP-712, matching the official v2
  client and verified against the live Kite facilitator) → paid request →
  settlement receipt.
- `kx402 balance` — native KITE and stablecoin balances via JSON-RPC, with a
  chain-id check before anything else is trusted.
- `kx402 receipt` — decode a `PAYMENT-RESPONSE` header.
- Cross-repository e2e against the real `kite-x402-axum` service binary
  behind a mock facilitator; GitHub Actions CI and tag-triggered
  cross-platform releases.

[v0.2.0]: https://github.com/wilsonqc223/kite-x402-client/releases/tag/v0.2.0
[v0.1.0]: https://github.com/wilsonqc223/kite-x402-client/releases/tag/v0.1.0
