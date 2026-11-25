# kite-x402-client

`kx402` is a client toolkit for **x402 paid HTTP APIs on the Kite AI chains**:
probe a `402 Payment Required` challenge, pay it with an EIP-3009
`transferWithAuthorization` signature, and decode settlement receipts.

It exists because the KiteAI ecosystem templates
([kite-x402-axum](https://github.com/tianzeshi-study/kite-x402-axum),
[kite-x402-hono](https://github.com/BrKDDD/kite-x402-hono), …) are all
**server-side** wrappers that charge for APIs. Testing one by hand means
base64-decoding headers and hand-building JSON payloads. `kx402` is the
missing client side: `curl`, but it speaks x402.

## Install

```bash
go install github.com/wilsonqc223/kite-x402-client/cmd/kx402@latest
```

Or from a checkout:

```bash
go build -o kx402 ./cmd/kx402
```

Check what you got: `kx402 version` reports the version the binary was built
at — `v0.3.1` for a tagged install, a commit-identifying pseudo-version for
`@main` or `@<commit>`.

After a fresh release, module proxies take a little while to list the new
tag, and `@latest` then installs the previous version or fails. Name the
version until they catch up — the newest tag on the
[releases page](https://github.com/wilsonqc223/kite-x402-client/releases):

```bash
go install github.com/wilsonqc223/kite-x402-client/cmd/kx402@v0.3.1
```

## Usage

### Probe a paid endpoint

```bash
$ kx402 probe https://paid-api.example.com/v1/forecast
x402 v2 challenge from https://paid-api.example.com/v1/forecast
resource: Daily weather forecast
2 payment option(s):

  [0] eip155:2368
      price:  0.001 pieUSD
      asset:  0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A
      payTo:  0xC0FFEE0000000000000000000000000000C0FFEE
      scheme: exact (max timeout 60s)
      domain: pieUSD v1
  ...
```

### Show your wallet

```bash
$ kx402 wallet -address-only     # the address to fund
0x1088...
$ kx402 wallet                   # address + balances on both networks
```

### Decode a settlement receipt

```bash
$ kx402 receipt --json "$(curl -si $URL | awk -F': ' '/payment-response/{print $2}')"
{
  "success": true,
  "transaction": "0xabc...",
  "network": "eip155:2368",
  "amount": "1000000000000000"
}
# or human-readable, straight off stdin:
$ ... | kx402 receipt -
settled on eip155:2368
```

### Pay for the content

```bash
$ export KITE_PRIVATE_KEY=0x...   # or pass -key-file; never a CLI argument
$ kx402 pay https://paid-api.example.com/v1/forecast
paid option 0 as 0x1088...846
settled: tx 0xabc... on eip155:2368 (0.001 pieUSD)
{"data":"paid!"}
```

**Look before you pay.** `kx402 pay -dry-run <url>` sends one unpaid
request and reports exactly what `pay` would sign — which option, at what
price, and whether it passes the cap — without loading a wallet or signing
anything. It exits `3` when `pay` would refuse, so it doubles as a check in
scripts and for agents deciding whether a price is acceptable:

```bash
$ kx402 pay -dry-run https://paid-api.example.com/v1/forecast
dry run — nothing was signed or sent
https://paid-api.example.com/v1/forecast answered 402 with 2 payment option(s); pay would sign option 0:
  (chosen automatically: the first option on a Kite network)
  network:  eip155:2368
  price:    0.001 pieUSD  (1000000000000000 units)
  asset:    0x38129cf4CE5E183eFF248F42A7D345Bb1B47621A
  payTo:    0xC0FFEE…
  validity: 60s
  token:    pieUSD
price cap: $1 — within cap
```

`probe`, `pay` and `balance` all take `-proxy URL` (e.g.
`socks5://127.0.0.1:10800`) so the client works from networks where paid
APIs are only reachable through an egress proxy.

**Price cap.** No single payment costs more than **$1** unless you say
otherwise: an option priced above the cap is refused *before anything is
signed*. Raise it per call with `-max-usd 10` (or `KITE_MAX_PAYMENT_USD`),
or disable it with `-max-usd 0`. x402 prices are fractions of a cent, so a
dollar is far above any legitimate per-call price — and it is what stops a
hostile endpoint, or a prompt-injected agent, from draining a wallet in one
request. The challenge's `payTo` and `asset` must be well-formed 20-byte
addresses; anything else is refused rather than normalized.

**Response size.** `kx402` reads at most **8 MiB** of a response into memory,
so an endpoint cannot exhaust the caller's memory by answering big. A longer
body is cut off at that point and never passed off as whole: `pay` prints the
beginning on stdout and says so on stderr, the MCP server's `kx402_pay` says
so in its result, and a dry run shows only the first 2 KiB of a free
endpoint's body. Raise it per call with `-max-body-bytes N`.

`pay` fetches the challenge, signs an EIP-3009 `transferWithAuthorization`
for the option you pick (`-o N`, default: the first option on a known Kite
network), retries the request with the `PAYMENT-SIGNATURE` header, and
prints the paid response plus the settlement receipt. The body goes to
stdout, progress and the receipt to stderr, so `kx402 pay … | jq` just
works. The EIP-712 wire format is verified against the real Kite
facilitator — `go run ./examples/facilitator-livecheck` reproduces the
proof (the facilitator recovers the payer from the signature).

### Cross-repo end-to-end against the kite-x402-axum service

```bash
cd ../kite-x402-axum && cargo build -p kite-x402-service
cd ../kite-x402-client
go run ./examples/e2e-axum-service -bin ../kite-x402-axum/target/debug/kite-x402-service
```

Launches the real Rust service behind a mock facilitator/upstream and pays
it with `kx402` — probe, EIP-3009 signature, settle receipt — proving the
client against the server template end to end. CI runs the same flow on
every push.

## Use it from an AI agent (MCP)

`kx402-mcp` speaks the Model Context Protocol over stdio, so MCP-capable
agents get four tools without leaving their chat:

| Tool | What it does | Spends? |
|---|---|---|
| `kx402_probe` | Decode an endpoint's 402 challenge: every option with price and payee. | No |
| `kx402_inspect` | Report exactly what `kx402_pay` would sign and whether it passes the price cap. Needs no wallet. | No |
| `kx402_balance` | An address's KITE and stablecoin balances on a Kite network. | No |
| `kx402_pay` | Sign and pay, returning the content and the settlement receipt. | **Yes** |

Agents should call `kx402_inspect` before `kx402_pay` whenever the price
matters; an unaffordable endpoint comes back as a normal answer saying the
payment would be refused, not as an error:

```json
{
  "mcpServers": {
    "kx402": {
      "command": "kx402-mcp",
      "env": {
        "KITE_PRIVATE_KEY": "0x…",
        "KITE_PROXY": "socks5://127.0.0.1:10800",
        "KITE_MAX_PAYMENT_USD": "1"
      }
    }
  }
}
```

`KITE_PRIVATE_KEY` is only needed for `kx402_pay` (and to default
`kx402_balance` to the wallet's own address); with it unset the server
still probes, inspects and queries balances, so a read-only deployment is
just the server without the key.

The key lives in the server environment and is never returned to the agent.
Each `kx402_pay` is capped at `KITE_MAX_PAYMENT_USD` (default **$1**) by the
operator — the cap cannot be read or raised from a tool call, so an agent
cannot talk itself into overspending. A key file other users can read
(mode 0644) is refused; keep it `0600`.

## Networks

| Network | CAIP-2      | Asset        | Decimals | EIP-712 domain             |
|---------|-------------|--------------|----------|----------------------------|
| testnet | eip155:2368 | pieUSD       | 18       | `pieUSD` v1                |
| mainnet | eip155:2366 | USDC.e       | 6        | `Bridged USDC (Kite AI)` v2 |

Payments are settled by the Kite facilitator at
`https://facilitator.pieverse.io/v2` (`/verify`, `/settle`, `/supported`).

## Development

```bash
go test ./...     # unit + integration tests (httptest mocks, no network)
go vet ./...
```

Layout:

- `internal/kite` — Kite network constants and the integer-math price parser
  (no floats), mirroring `kite.ts` / `kite.go` / `kite.rs` from the official
  templates.
- `internal/x402` — wire types and codecs for the 402 challenge, payment
  payload, and settlement receipt, plus client-side option matching.
- `cmd/kx402` — the CLI.

## License

Apache-2.0
