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

## Requirements

- **Release binaries:** Linux, macOS or Windows on amd64 or arm64. Each
  binary is static; there is nothing else to install.
- **From source:** Go 1.27 or later.
- **To pay:** a wallet holding the service's stablecoin — `pieUSD` on Kite
  testnet, `USDC.e` on Kite mainnet. The payer only signs an authorization
  and never needs KITE for gas: the facilitator broadcasts the settlement.
- **Network:** the client talks to the paid endpoint only. The facilitator
  and the chain are contacted by the service, not by you; `kx402 balance`
  is the one command that calls a Kite RPC endpoint itself.

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
for the option you pick, sends the request again with the `PAYMENT-SIGNATURE`
header, and prints the paid response plus the settlement receipt. The option
is the first one on a known Kite network unless you say otherwise — by
position with `-o N`, or by network with `-network mainnet`,
`-network testnet` or a CAIP-2 id such as `-network eip155:2366`. A network
the challenge does not offer is refused before anything is signed. The body
goes to stdout, progress and the receipt to stderr, so `kx402 pay … | jq`
just works. The EIP-712 wire format is verified against the real Kite
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

## Configuration

`kx402` and `kx402-mcp` read the environment; [.env.example](.env.example)
lists every variable with its meaning. Nothing is read from a config file,
and the key is never a command-line argument.

| Variable | Read by | Meaning |
|---|---|---|
| `KITE_PRIVATE_KEY` | `pay`, `wallet`, `balance`, `kx402-mcp` | The paying wallet's private key: 64 hex characters, with or without `0x`. `pay`, `wallet` and `balance` take `-key-file PATH` instead; a file other users can read is refused (`chmod 600`). |
| `KITE_MAX_PAYMENT_USD` | `pay`, `kx402-mcp` | The default for `pay -max-usd`, and the only cap `kx402-mcp` has. Default `1`; `0` disables it. |
| `KITE_NETWORK` | `balance` | The default for `balance -net`: `testnet` (default) or `mainnet`. |
| `KITE_PROXY` | `kx402-mcp` | Egress proxy for every request the MCP server makes, e.g. `socks5://127.0.0.1:10800`. The CLI takes `-proxy` per command instead. |

To keep settings out of a shell profile, copy the example and load it for one
shell:

```bash
cp .env.example .env      # then edit it
set -a; . ./.env; set +a
```

`.env` is not committed: this repository ignores it.

## Use it from an AI agent (MCP)

`kx402-mcp` speaks the Model Context Protocol over stdio, so MCP-capable
agents get four tools without leaving their chat:

| Tool | What it does | Spends? |
|---|---|---|
| `kx402_probe` | Decode an endpoint's 402 challenge: every option with price and payee. | No |
| `kx402_inspect` | Report exactly what `kx402_pay` would sign — the option named by `option` index, by `network`, or chosen automatically — and whether it passes the price cap. Needs no wallet. | No |
| `kx402_balance` | An address's KITE and stablecoin balances on a Kite network. | No |
| `kx402_pay` | Sign and pay the option named by `option` or `network` (default: automatic), returning the content and the settlement receipt. | **Yes** |

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

## How this fits the Kite AI ecosystem

Kite AI's x402 stack has two sides. **Services** are HTTP wrappers that
answer `402` with a `PAYMENT-REQUIRED` challenge, verify the buyer's
signature through the Kite facilitator, call the upstream API and settle.
Templates for that side are
[`tianzeshi-study/kite-x402-axum`](https://github.com/tianzeshi-study/kite-x402-axum)
(Rust), the Express and Go templates in
[`gokite-ai/kite-x402-services`](https://github.com/gokite-ai/kite-x402-services/tree/main/templates),
and [`BrKDDD/kite-x402-hono`](https://github.com/BrKDDD/kite-x402-hono).
**This repository is the other side**: the client that answers the challenge.

Where the Kite AI specifics live in this code:

| What | Where | Follows |
|---|---|---|
| Network ids, RPC URLs, token contracts, decimals, EIP-712 domain names and versions, facilitator URL | `internal/kite/chain.go` | `kite.ts` / `kite.go` / `kite.rs` in the templates above |
| Integer `$`-price parser, no floats | `internal/kite/chain.go`, `ParsePrice` | `kiteMoneyParser` in the templates |
| EIP-712 `transferWithAuthorization` (EIP-3009) typing and signing | `internal/x402/sign.go` | what the facilitator verifies |
| `PAYMENT-REQUIRED`, `PAYMENT-SIGNATURE`, `PAYMENT-RESPONSE` codecs | `internal/x402/wire.go` | the x402 v2 wire format |

Two live links keep the two sides from drifting apart:

- `examples/e2e-axum-service` builds the real `kite-x402-service` and pays it;
  this repository's CI runs it on every push.
- The `kite-x402-axum` repository's CI has a `kx402 client contract` job that
  runs this client against the service on every change there
  (`.github/workflows/ci.yml`).

The constants and the price parser follow Apache-2.0 sources; see
[ATTRIBUTION](ATTRIBUTION.md).

## Development

```bash
gofmt -l .                 # must print nothing
go vet ./...
go test -race ./...        # unit + integration; httptest mocks, no network, no wallet
go run ./examples/e2e-axum-service -bin ../kite-x402-axum/target/release/kite-x402-service
```

Test wallets are generated at runtime; the repository holds no key material.

Layout:

- `internal/kite` — Kite network constants and the integer-math price parser
  (no floats), mirroring `kite.ts` / `kite.go` / `kite.rs` from the official
  templates.
- `internal/x402` — wire types and codecs for the 402 challenge, payment
  payload, and settlement receipt; EIP-3009 signing; `Pay` and `Inspect`.
- `internal/rpc` — the JSON-RPC calls behind `balance`.
- `internal/cli` — what the CLI and the MCP server share: key loading, the
  HTTP client, and the report both print for a dry run.
- `cmd/kx402` — the CLI. `cmd/kx402-mcp` — the MCP server.
- `examples/` — `e2e-axum-service`, which pays the real Rust service, and
  `facilitator-livecheck`, which sends one authorization to the live
  facilitator.

## Known limitations

**What has been run for real, and what has not.**

- **The signature format is checked against the live facilitator.**
  `go run ./examples/facilitator-livecheck` signs with a throwaway key and
  sends the authorization to the real Kite facilitator's `/verify`. It
  answers `200` with `invalidReason: "insufficient_funds"` and the payer
  address recovered from the signature: the format is accepted, and only the
  empty wallet stops it.
- **Settlement has only been exercised behind a mock facilitator.** The tests
  and the cross-repository e2e run the real `kite-x402-service` with a mock
  facilitator. No test of this project has settled a payment on the Kite
  chain, and neither has its author so far. On testnet that takes `pieUSD`,
  which the web faucet did not hand out when this was checked (it gave KITE
  only; `kpass faucet drop … --token pieUSD` needs a Kite Passport login); on
  mainnet it takes real `USDC.e`. Until that is done, treat on-chain
  settlement as unverified by this project: look with `kx402 pay -dry-run`
  first, and keep `-max-usd` small for a first real payment.

**What it does not do.**

- **Only the `exact` scheme, only the two Kite networks.** The EIP-712 domain
  is pinned to Kite testnet and mainnet. An option on any other network is
  listed by `probe` but cannot be paid, and automatic selection skips it.
- **No retry of a paid request.** If the connection fails after the payment
  was sent, the outcome is unknown — the service may have settled.
  `kx402` reports the error and stops, because trying again could pay twice.
  Check the service or the wallet before retrying.
- **Responses are buffered, not streamed.** Up to 8 MiB of a response is read
  into memory and printed whole; anything beyond is cut off and reported.
- **Fixed timeouts.** `pay` allows 60 seconds per request, `probe` 30; there
  is no flag for either.

## License

Apache-2.0
