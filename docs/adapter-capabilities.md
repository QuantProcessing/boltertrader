# Adapter Capabilities

This matrix is the live-only operating contract for the currently implemented
adapter subset. Unsupported report surfaces must return `contract.ErrNotSupported`;
open-only order reports must be treated as ambiguous absence during
reconciliation.

| Venue | Product | Market stream | Private order stream | Account stream | Account-state snapshot | Submit | Cancel | Modify | Order status reports | Fill reports | Position reports | Mass status | Single-order query | Open-only caveat | Latency timestamps | Acceptance target |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| BINANCE | USD-M Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | runtime timestamps | make test-binance-demo-runtime-perp |
| BINANCE | Spot | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | runtime timestamps | make test-binance-demo-runtime-spot |
| OKX | USDT-linear SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | runtime timestamps | make test-okx-demo-runtime-perp |
| OKX | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | runtime timestamps | make test-okx-demo-runtime-spot |
| BYBIT | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-bybit-spot-acceptance |
| BYBIT | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | open order filter | yes | runtime timestamps | make test-bybit-usdt-perp-acceptance |
| BYBIT | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | open order filter | yes | runtime timestamps | make test-bybit-usdc-perp-acceptance |
| BITGET | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-bitget-spot-acceptance |
| BITGET | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | open order filter | yes | runtime timestamps | make test-bitget-usdt-perp-acceptance |
| BITGET | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | open order filter | yes | runtime timestamps | make test-bitget-usdc-perp-acceptance |
| HYPERLIQUID | Spot cash | no | no | no | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-spot |
| HYPERLIQUID | Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | venue order id | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-perp |
| HYPERLIQUID | HIP-3 Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | venue order id | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-hip3 |
| LIGHTER | Spot cash | no | no | no | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-lighter-testnet-runtime-spot |
| LIGHTER | Perp | no | no | no | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | open order filter | yes | runtime timestamps | make test-lighter-testnet-runtime-perp |

## Non-Production Acceptance Scope

Default CI remains credential-free. Demo/Testnet acceptance is explicit:

```sh
make test-binance-demo-acceptance
make test-okx-demo-acceptance
make test-hyperliquid-testnet-acceptance
make test-lighter-testnet-acceptance
make test-bybit-acceptance
make test-bitget-acceptance
make test-bybit-bitget-acceptance
```

Raw live `go test` runs skip when required Demo/Testnet credentials are absent.
CEX rows use Demo or paper-trading environments, while DEX rows use Testnet.
Hyperliquid Testnet runtime acceptance requires a verified
`contract.AccountStateReporter` snapshot before risk-increasing orders; API
wallet keys are resolved through Hyperliquid `userRole`, and
`HYPERLIQUID_ACCOUNT_ADDRESS` should be the owner 0x user address when it differs
from the signing key. Non-0x Hyperliquid account aliases are rejected before
venue `/info` requests.
The Hyperliquid, Lighter, Bybit, and Bitget Make acceptance targets additionally
fail on any selected skipped test, so missing funding, missing HIP-3 config,
missing Demo/Testnet credentials, invalid Bitget endpoint overrides, or dirty
account state is reported as incomplete acceptance. Production credentials are
not accepted as fallback inputs for Demo/Testnet acceptance.

Bybit and Bitget rows are the first unified-account adapter/runtime slice for
those venues. The offline contract proves SDK conversion, stream decoding,
account-state safety envelopes, account-state reconciliation, portfolio/risk
reads, and private stream subscription wiring. The current G010 evidence marks
the first-stage Bybit Demo Trading and Bitget Demo/paper-trading rows accepted:
the runtime entrypoints verified live market data, authoritative account-state
snapshots, risk fail-closed behavior, reconciliation into cache/portfolio,
private stream startup, and a bounded resting-cancel plus IOC fill/close cleanup
ladder. Future acceptance reruns remain noskip-gated and require real
credentials, sufficient funding, valid endpoint profiles, and clean venue
accounts.
