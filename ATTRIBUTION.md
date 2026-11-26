# Attribution

`kx402` is an independent project and is not affiliated with Kite AI. Parts of
it were written to match Apache-2.0 sources; those are acknowledged here.
This project is itself licensed under Apache-2.0 (see [LICENSE](LICENSE)).

## Kite network constants and the price parser

`internal/kite/chain.go` carries the Kite mainnet and testnet constants —
CAIP-2 network ids, RPC URLs, stablecoin contracts, decimals, EIP-712 domain
names and versions, and the facilitator URL — and the integer `$`-price
parser. They follow, field for field:

- `kite.ts` and `kite.go` in the official templates,
  <https://github.com/gokite-ai/kite-x402-services/tree/main/templates>
  (Apache-2.0);
- `kite.rs` in <https://github.com/tianzeshi-study/kite-x402-axum>
  (Apache-2.0), which mirrors the official files in turn.

These are protocol constants rather than original work: the facilitator
accepts a signature only when the EIP-712 domain matches the token contract
exactly, so the values are kept identical to the templates.

## End-to-end test target

`examples/e2e-axum-service` and the `e2e` CI job build and pay
`kite-x402-service` from <https://github.com/tianzeshi-study/kite-x402-axum>
(Apache-2.0). That repository is a test dependency of this one and is not
distributed with it.

## Dependencies

`go.mod` lists the Go modules this project is built on. `kx402` takes its
secp256k1 signing from `github.com/ethereum/go-ethereum`, which is licensed
under LGPL-3.0; see the `LICENSE` file inside that module. Each dependency
keeps its own license.
