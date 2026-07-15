# Adapter Capabilities

This matrix is the live-only operating contract for the currently implemented
adapter subset. Unsupported report surfaces must return
`contract.ErrNotSupported`. Reconciliation authority comes only from each
domain's typed state and frozen scope; warnings are diagnostic. Complete
open-order coverage can prove that a scoped cached order is no longer open, but
it cannot invent a terminal cause or fill history.
The `Mass status` column lists every report domain that the execution adapter
can directly own in one mass-status response; account-only snapshots do not
count as execution mass-status support.

| Venue | Product | Market stream | Private order stream | Account stream | Account-state snapshot | Submit | Cancel | Modify | Order status reports | Fill reports | Position reports | Mass status | Single-order query | Open-only caveat | Latency timestamps | Acceptance target |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ASTER | Spot cash | yes | yes | yes | yes | yes | yes | no | open orders | my trades | unsupported | open orders, bounded fills | venue order id | yes | runtime timestamps | make test-aster-testnet-runtime-spot |
| ASTER | USDT-linear Perp | yes | yes | yes | yes | yes | yes | no | open orders | my trades | account snapshot | open orders, bounded fills, positions | venue order id | yes | runtime timestamps | make test-aster-testnet-runtime-perp |
| NADO | Spot no-borrow | yes | yes | yes | yes | yes | yes | no | open orders | matches | unsupported | open orders, bounded fills | order digest | yes | runtime timestamps | make test-nado-testnet-runtime-spot |
| NADO | Perp | yes | yes | yes | yes | yes | yes | no | open orders | matches | account snapshot | open orders, bounded fills, positions | order digest | yes | runtime timestamps | make test-nado-testnet-runtime-perp |
| BINANCE | USD-M Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | runtime timestamps | make test-binance-demo-runtime-perp |
| BINANCE | Spot | yes | yes | yes | yes | yes | yes | no | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | runtime timestamps | make test-binance-demo-runtime-spot |
| OKX | USDT-linear SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | runtime timestamps | make test-okx-demo-runtime-perp |
| OKX | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | runtime timestamps | make test-okx-demo-runtime-spot |
| BYBIT | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | unsupported | open orders, bounded fills | open order filter | yes | runtime timestamps | make test-bybit-spot-acceptance |
| BYBIT | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | account snapshot | open orders, bounded fills, positions | open order filter | yes | runtime timestamps | make test-bybit-usdt-perp-acceptance |
| BYBIT | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | account snapshot | open orders, bounded fills, positions | open order filter | yes | runtime timestamps | make test-bybit-usdc-perp-acceptance |
| BITGET | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | unsupported | open orders, bounded fills | open order filter | yes | runtime timestamps | make test-bitget-spot-acceptance |
| BITGET | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | account snapshot | open orders, bounded fills, positions | open order filter | yes | runtime timestamps | make test-bitget-usdt-perp-acceptance |
| BITGET | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | account snapshot | open orders, bounded fills, positions | open order filter | yes | runtime timestamps | make test-bitget-usdc-perp-acceptance |
| GATE | Spot cash | yes | yes | yes | yes | yes | yes | no | open orders | my trades | unsupported | open orders, bounded fills | venue order id | yes | runtime timestamps | make test-gate-spot-acceptance |
| GATE | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | no | open orders | my trades | account snapshot | open orders, bounded fills, positions | venue order id | yes | runtime timestamps | make test-gate-usdt-perp-acceptance |
| HYPERLIQUID | Spot cash | no | no | no | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-spot |
| HYPERLIQUID | Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | venue order id | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-perp |
| HYPERLIQUID | HIP-3 Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | venue order id | yes | runtime timestamps | make test-hyperliquid-testnet-runtime-hip3 |
| LIGHTER | Spot cash | no | no | no | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | runtime timestamps | make test-lighter-testnet-runtime-spot |
| LIGHTER | Perp | no | no | no | yes | yes | yes | yes | open orders | unsupported | account snapshot | open orders, positions | open order filter | yes | runtime timestamps | make test-lighter-testnet-runtime-perp |

## Non-Production Acceptance Scope

Default CI remains credential-free. Demo/Testnet acceptance is explicit:

```sh
make test-binance-demo-acceptance
make test-okx-demo-acceptance
make test-hyperliquid-testnet-acceptance
make test-lighter-testnet-acceptance
make test-bybit-acceptance
make test-bitget-acceptance
make test-gate-testnet-acceptance
make test-aster-testnet-acceptance
make test-nado-testnet-acceptance
make test-aster-nado-testnet-acceptance
make test-bybit-bitget-acceptance
```

Raw live `go test` runs skip when required Demo/Testnet credentials are absent.
CEX rows use each venue's official non-production write surface: Demo Trading,
paper trading, or Testnet. DEX rows use Testnet.
Hyperliquid Testnet runtime acceptance requires a verified mandatory
`contract.AccountClient.AccountState` snapshot before risk-increasing orders; API
wallet keys are resolved through Hyperliquid `userRole`, and
`HYPERLIQUID_ACCOUNT_ADDRESS` should be the owner 0x user address when it differs
from the signing key. Non-0x Hyperliquid account aliases are rejected before
venue `/info` requests.
The Hyperliquid, Lighter, Bybit, Bitget, and Gate Make acceptance targets
additionally fail on any selected skipped test, so missing funding, missing
HIP-3 config, missing Demo/Testnet credentials, invalid endpoint overrides, or
dirty account state is reported as incomplete acceptance. Production
credentials are not accepted as fallback inputs for Demo/Testnet acceptance.

Gate rows use Gate's official Testnet endpoint profile. The phase-one support
surface is Spot cash plus USDT-linear futures/perps only; USDC-linear futures
are intentionally deferred until an official non-production path is proven.

Aster rows use only the official V3 Testnet profiles and API-wallet EIP-712
credentials. Symbols whose normalized venue symbol starts with `TEST` are never
eligible for write acceptance. Nado rows share one unified-margin account;
Spot is funded-only/no-borrow, while venue capacity for both Spot and Perp is
server-authoritative. Their aggregate targets are noskip-gated and require all
read-only plus four adapter/runtime write rows per venue to execute and leave
no test-owned orders or Perp exposure.

The Nado rows describe implemented adapter capabilities. Submit remains
fail-closed around local validation, adapter-local signing and exact response
identity, one-time execution, and reconciliation. Runtime invokes the same
ordinary `ValidateSubmit`/optional-risk/`Submit` protocol used by other venues;
there is no venue-capacity admission protocol. Historical Testnet results do
not certify this converged tree; G008 must pass all four Nado write rows without
skips on the frozen candidate.
For isolated-only Perp products, discovery selects isolated execution and the
adapter transfers conservative 1x opening margin; reduce-only closes add zero
margin.

## Derivative Reference Data

Perp venues expose funding/reference data as market data. Funding rate, mark
price, and index price or oracle price are streamed into
`runtime.Cache.DerivativeReference`; current open interest is query-only through
`contract.OpenInterestClient` and must not be cached.

```sh
make test-reference-data-offline
make test-reference-data-read
```

| Venue | Product | Reference source | Index/oracle | Current OI | Read-only acceptance |
| --- | --- | --- | --- | --- | --- |
| BINANCE | USD-M Perp | WS mark-price stream | index price | REST query only | make test-binance-demo-reference-data-read |
| OKX | USDT-linear SWAP | WS funding/mark/index streams | index price | REST query only | make test-okx-demo-reference-data-read |
| BYBIT | USDT/USDC-linear Perp | WS ticker stream | index price | REST ticker query only | make test-bybit-demo-reference-data-read |
| BITGET | USDT/USDC-linear Perp | WS ticker stream | index price | REST query only | make test-bitget-demo-reference-data-read |
| GATE | USDT-linear Perp/SWAP | REST snapshot event | index price | REST ticker query only | make test-gate-testnet-reference-data-read |
| HYPERLIQUID | Perp and HIP-3 Perp | REST snapshot event | oracle price | REST asset context query only | make test-hyperliquid-testnet-reference-data-read |
| LIGHTER | Perp | WS market-stats stream | index price | REST order-book-detail query only | make test-lighter-testnet-reference-data-read |
| ASTER | USDT-linear Perp | REST snapshot plus WS mark-price stream | index price | REST query only | make test-aster-testnet-reference-data-read |
| NADO | Perp | REST funding/mark/index/oracle snapshot plus WS funding-rate updates | exact x18 index and oracle prices | REST `all_products` query only | make test-nado-testnet-reference-data-read |

Nado's funding endpoint reports a 24-hour rate basis while settlement occurs
hourly. The normalized snapshot preserves that native rate and advertises a
`24h` funding interval; it does not manufacture a next-funding timestamp. Nado
mark, index, and oracle values use the Archive V1 x18 queries and each field's
official `update_time`. The native WS stream supplies funding updates only, so
cached mark/index/oracle values retain their independent REST freshness.
Nado funding history is intentionally not capability-advertised because the
official Archive V1 contract documents current funding only.

## Deferred / Unsupported Products

| Venue | Product | Status | Contract result | Acceptance guard |
| --- | --- | --- | --- | --- |
| BYBIT | Dated linear futures | deferred | filtered from first-phase perp registry | `TestInstrumentFromBybitRejectsDatedLinearFutures` |
| GATE | USDC-linear Perp/Futures | deferred | `contract.ErrNotSupported` | `TestGateTestnetUSDCPerpDeferredCapability` |

Bybit and Bitget rows are the first unified-account adapter/runtime slice for
those venues. The offline contract proves SDK conversion, stream decoding,
account-state safety envelopes, account-state reconciliation, portfolio/risk
reads, and private stream subscription wiring. Historical pre-convergence G010
evidence exercised the first-stage Bybit Demo Trading and Bitget
Demo/paper-trading rows:
the runtime entrypoints verified live market data, authoritative account-state
snapshots, risk fail-closed behavior, reconciliation into cache/portfolio,
private stream startup, and a bounded resting-cancel plus IOC fill/close cleanup
ladder. It does not certify this tree. G008 reruns both noskip-gated aggregates
and requires real credentials, sufficient funding, valid endpoint profiles,
and clean venue accounts.
