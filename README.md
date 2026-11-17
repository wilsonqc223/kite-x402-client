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
go install github.com/Chesten223/kite-x402-client/cmd/kx402@latest
```

Or from a checkout:

```bash
go build -o kx402 ./cmd/kx402
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

### Decode a settlement receipt

```bash
$ kx402 receipt "$(echo ... | grep -i payment-response)"
settled on eip155:2368
transaction: 0xabc...
amount:      0.001 pieUSD
```

### Pay for the content

```bash
$ export KITE_PRIVATE_KEY=0x...   # or pass -key-file; never a CLI argument
$ kx402 pay https://paid-api.example.com/v1/forecast
paid option 0 as 0x1088...846
settled: tx 0xabc... on eip155:2368 (0.001 pieUSD)
{"data":"paid!"}
```

`pay` fetches the challenge, signs an EIP-3009 `transferWithAuthorization`
for the option you pick (`-o N`, default: the first option on a known Kite
network), retries the request with the `PAYMENT-SIGNATURE` header, and
prints the paid response plus the settlement receipt. The body goes to
stdout, progress and the receipt to stderr, so `kx402 pay … | jq` just
works. The EIP-712 wire format is verified against the real Kite
facilitator — `go run ./examples/facilitator-livecheck` reproduces the
proof (the facilitator recovers the payer from the signature).

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
