# Testing Strategy

BolterTrader's default test suite is offline deterministic. It must not depend
on credentials, public internet, current exchange listings, live market state, or
wall-clock timing beyond local test deadlines.

## Standard Commands

```sh
make test
make test-race
make test-core
make test-adapter
make test-sdk
make test-capabilities
make test-reference-data-offline
make test-p6-offline
```

The default Make targets use Go's `-short` mode where adapter or SDK packages
may otherwise discover local credentials. Keep real venue reads/writes on the
explicit live, demo, or testnet targets below.

## Test Levels

1. Unit tests cover pure model, enum, cache, risk, accounting, conversion, and
   state-transition behavior.
2. Golden fixture tests cover SDK and adapter request, response, and stream
   payloads.
3. Contract conformance tests cover venue-neutral client behavior in
   `core/contract/contracttest`.
4. Scenario tests cover product-level flows such as spot inventory, perp
   funding, futures expiry, options exercise, liquidation, reconnect, and
   reconciliation.
5. Account-state acceptance tests cover the NT-style safety envelope: canonical
   account IDs, reported venue balances/free/locked totals, margin requirements,
   cache freshness, portfolio reads, risk fail-closed behavior, and
   reconciliation application from one authoritative snapshot.
6. Deterministic replay tests feed fixed event streams and assert exact final
   orders, fills, positions, balances, and PnL.
7. Race and lifecycle tests cover runtime goroutine boundaries, cancellation,
   reconnect loops, and shutdown.
8. Live smoke tests are opt-in and excluded from default verification.

## NT-Style Acceptance Ladder

BolterTrader follows NautilusTrader's testing shape: keep normal CI
deterministic, then run venue-backed spec acceptance explicitly when credentials
and exchange state are available.

- `make test-core`: local core/runtime/strategy behavior.
- `make test-adapter`: venue-neutral adapter contracts.
- `make test-sdk`: SDK request/response/stream behavior without live writes.
- `make test-capabilities`: adapter capability matrix plus package-level
  capability probes, including the account-state snapshot contract.
- `make test-reference-data-offline`: credential-free funding/reference-data
  model, runtime cache, adapter conversion, and OI query-only checks.
- `make test-p6-offline`: the full credential-free acceptance gate for core,
  runtime, SDK, adapter, and capability behavior.
- `make test-reference-data-read`: read-only Demo/Testnet funding/reference-data
  acceptance for all implemented perp venues.
- `make test-binance-demo-reference-data-read`: read-only Binance Demo USD-M
  funding/mark/index stream-cache plus current OI query acceptance.
- `make test-okx-demo-reference-data-read`: read-only OKX Demo SWAP
  funding/mark/index stream-cache plus current OI query acceptance.
- `make test-bybit-demo-reference-data-read`: read-only Bybit Demo USDT/USDC
  Perp funding/mark/index stream-cache plus current OI query acceptance.
- `make test-bitget-demo-reference-data-read`: read-only Bitget Demo USDT/USDC
  Perp funding/mark/index stream-cache plus current OI query acceptance.
- `make test-gate-testnet-reference-data-read`: read-only Gate Testnet USDT
  Perp funding/mark/index snapshot-cache plus current OI query acceptance.
- `make test-hyperliquid-testnet-reference-data-read`: read-only Hyperliquid
  Testnet standard Perp and configured HIP-3 funding/mark/oracle cache plus
  current OI query acceptance.
- `make test-lighter-testnet-reference-data-read`: read-only Lighter Testnet
  Perp funding/mark/index stream-cache plus current OI query acceptance.
- `make test-aster-testnet-reference-data-read`: read-only Aster Testnet Perp
  funding/mark/index stream-cache plus current OI query acceptance.
- `make test-nado-testnet-reference-data-read`: read-only Nado Testnet Perp
  funding/mark/index/oracle stream-cache plus current OI query acceptance.
- `make test-demo-acceptance`: the aggregate credential-gated CEX Demo or
  paper-trading acceptance gate for Binance, OKX, Bybit, and Bitget.
- `make test-binance-demo-perp`: adapter-level Binance USD-M Demo execution.
- `make test-binance-demo-runtime-perp`: runtime-level Binance USD-M Demo
  execution through `runtime.TradingNode`.
- `make test-binance-demo-spot-data`: read-only Binance Spot Demo data
  acceptance.
- `make test-binance-demo-spot`: adapter-level Binance Spot Demo execution.
- `make test-binance-demo-runtime-spot`: runtime-level Binance Spot Demo cash
  execution through `runtime.TradingNode`.
- `make test-binance-demo-acceptance`: complete Binance Demo acceptance gate for
  implemented products.
- `make test-okx-demo-spot`: adapter-level OKX Demo Spot cash execution.
- `make test-okx-demo-runtime-spot`: runtime-level OKX Demo Spot cash execution
  through `runtime.TradingNode`.
- `make test-okx-demo-perp`: adapter-level OKX Demo USDT-linear SWAP execution.
- `make test-okx-demo-runtime-perp`: runtime-level OKX Demo USDT-linear SWAP
  execution through `runtime.TradingNode`.
- `make test-okx-demo-acceptance`: complete OKX Demo acceptance gate for the
  implemented OKX Spot/Perp subset.
- `make test-bybit-demo-spot`: adapter-level Bybit Demo Spot cash
  acceptance.
- `make test-bybit-demo-runtime-spot`: runtime-level Bybit Demo Spot cash
  acceptance through `runtime.TradingNode`.
- `make test-bybit-demo-usdt-perp`: adapter-level Bybit Demo USDT-linear
  Perp acceptance.
- `make test-bybit-demo-runtime-usdt-perp`: runtime-level Bybit Demo
  USDT-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bybit-demo-usdc-perp`: adapter-level Bybit Demo USDC-linear
  Perp acceptance.
- `make test-bybit-demo-runtime-usdc-perp`: runtime-level Bybit Demo
  USDC-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bybit-acceptance`: complete Bybit Demo acceptance gate for
  Spot, USDT-linear Perp, and USDC-linear Perp.
- `make test-bitget-demo-spot`: adapter-level Bitget Demo Spot cash
  acceptance.
- `make test-bitget-demo-runtime-spot`: runtime-level Bitget Demo Spot
  cash acceptance through `runtime.TradingNode`.
- `make test-bitget-demo-usdt-perp`: adapter-level Bitget Demo
  USDT-linear Perp acceptance.
- `make test-bitget-demo-runtime-usdt-perp`: runtime-level Bitget Demo
  USDT-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bitget-demo-usdc-perp`: adapter-level Bitget Demo
  USDC-linear Perp acceptance.
- `make test-bitget-demo-runtime-usdc-perp`: runtime-level Bitget Demo
  USDC-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bitget-acceptance`: complete Bitget Demo/paper-trading acceptance
  gate for Spot, USDT-linear Perp, and USDC-linear Perp.
- `make test-gate-testnet-read`: read-only Gate Testnet Spot/USDT futures
  market and account-state discovery.
- `make test-gate-testnet-spot`: adapter-level Gate Testnet Spot cash
  acceptance.
- `make test-gate-testnet-runtime-spot`: runtime-level Gate Testnet Spot cash
  acceptance through `runtime.TradingNode`.
- `make test-gate-testnet-usdt-perp`: adapter-level Gate Testnet USDT-linear
  Perp/SWAP acceptance.
- `make test-gate-testnet-runtime-usdt-perp`: runtime-level Gate Testnet
  USDT-linear Perp/SWAP acceptance through `runtime.TradingNode`.
- `make test-gate-testnet-usdc-perp-deferred`: credential-free regression that
  Gate USDC-linear futures remain unsupported/deferred in phase one.
- `make test-gate-testnet-acceptance`: complete Gate Testnet acceptance gate
  for Spot cash and USDT-linear Perp/SWAP.
- `make test-hyperliquid-testnet-spot-read`: read-only Hyperliquid Testnet Spot
  market/account discovery.
- `make test-hyperliquid-testnet-spot`: adapter-level Hyperliquid Testnet Spot
  execution.
- `make test-hyperliquid-testnet-runtime-spot`: runtime-level Hyperliquid
  Testnet Spot execution through `runtime.TradingNode`.
- `make test-hyperliquid-testnet-perp-read`: read-only Hyperliquid Testnet Perp
  market/account discovery.
- `make test-hyperliquid-testnet-perp`: adapter-level Hyperliquid Testnet Perp
  execution.
- `make test-hyperliquid-testnet-runtime-perp`: runtime-level Hyperliquid
  Testnet Perp execution through `runtime.TradingNode`.
- `make test-hyperliquid-testnet-hip3`: read-only Hyperliquid HIP-3 Testnet
  discovery for a configured `dex:coin`.
- `make test-hyperliquid-testnet-hip3-write`: adapter-level bounded Hyperliquid
  HIP-3 fill and reduce-only close for a configured `dex:coin`.
- `make test-hyperliquid-testnet-runtime-hip3`: runtime-level Hyperliquid HIP-3
  Testnet execution through `runtime.TradingNode`.
- `make test-hyperliquid-testnet-acceptance`: complete Hyperliquid Testnet
  acceptance gate for Spot, standard Perp, and configured HIP-3.
- `make test-lighter-testnet-read`: read-only Lighter Testnet Spot/Perp market
  and unified account discovery.
- `make test-lighter-testnet-spot`: adapter-level Lighter Testnet Spot
  execution.
- `make test-lighter-testnet-runtime-spot`: runtime-level Lighter Testnet Spot
  execution through `runtime.TradingNode`.
- `make test-lighter-testnet-perp`: adapter-level Lighter Testnet Perp
  execution.
- `make test-lighter-testnet-runtime-perp`: runtime-level Lighter Testnet Perp
  execution through `runtime.TradingNode`.
- `make test-lighter-testnet-acceptance`: complete Lighter Testnet acceptance
  gate for Spot and Perp under the unified account model.
- `make test-aster-testnet-read`: Aster V3 Testnet Spot/Perp discovery,
  account-state, market, reference-data, and current-OI reads.
- `make test-aster-testnet-spot` and `make test-aster-testnet-runtime-spot`:
  Aster Spot adapter/runtime write and cleanup rows.
- `make test-aster-testnet-perp` and `make test-aster-testnet-runtime-perp`:
  Aster Perp adapter/runtime write, flatten, and cleanup rows.
- `make test-aster-testnet-acceptance`: all Aster read and four write rows under
  `noskipgotest`.
- `make test-nado-testnet-read`: Nado Testnet Spot/Perp discovery, unified
  account-state, market, reference-data, and current-OI reads.
- `make test-nado-testnet-spot` and `make test-nado-testnet-runtime-spot`:
  funded-only/no-borrow Nado Spot adapter/runtime write and cleanup rows.
- `make test-nado-testnet-perp` and `make test-nado-testnet-runtime-perp`:
  Nado Perp adapter/runtime write, flatten, and cleanup rows through ordinary
  adapter `Submit`, with signing and exact request ownership kept inside the
  adapter.
- `make test-nado-testnet-acceptance`: all Nado read and four write rows under
  `noskipgotest`.
- `make test-aster-nado-testnet-acceptance`: serial aggregate for both venues;
  any skip, failed cleanup, or missing row is incomplete acceptance.

See [`docs/developer_guide/spec_exec_testing.md`](developer_guide/spec_exec_testing.md)
for the execution acceptance spec and pass criteria.
See [`docs/adapter-capabilities.md`](adapter-capabilities.md) for the supported
adapter/product/report matrix.
See [`docs/aster-nado-test-traceability.md`](aster-nado-test-traceability.md)
for the approved Aster/Nado spec-case to grouped-test mapping.

## Account-State Runtime Acceptance

The account model is tested as a runtime safety envelope, not only as adapter
translation code. Default tests must prove:

- every `contract.AccountClient` implements the mandatory `AccountState` snapshot;
- reconciliation calls that snapshot directly before account-only position checks;
- the runtime cache exposes canonical account state and exact `Balance.Free` values;
- portfolio account views can read equity, margin, and exposure from the cache;
- account-backed risk requires configured client provenance, one successful
  authoritative reconciliation, and fresh matching account/venue/product state;
- only cash-account Spot risk consumes exact `Balance.Free` plus working-order
  reservations. Unified-margin capacity remains server-authoritative.

`runtime.TestOfflineAccountStateSnapshotReconcilesPortfolioAndRisk` is the
fake-venue end-to-end gate for this behavior. Non-production runtime acceptance
for Binance, OKX, Bybit, Bitget, Gate, Hyperliquid, Lighter, Aster, and Nado must
run the same shape against exchange snapshots before and after order flows.

Runtime ownership is account-id based. A venue may expose multiple account
states in one process, and cache, portfolio, risk, reconciliation, balances, and
positions are keyed by account id. Product-specific venues such as Binance Spot
and USD-M may still use separate nodes, while unified venues such as Lighter can
run Spot and Perp against the same logical `LIGHTER-001` account id. Venue
selectors such as Lighter account indexes, Hyperliquid owner/vault/signer
addresses, OKX `tdMode`, and product scopes are configuration or mode metadata;
they are not canonical runtime account ids. Adapter-owned default account ids
are `BINANCE-001`, `OKX-001`, `BYBIT-001`, `BITGET-001`, `GATE-001`,
`LIGHTER-001`, `HYPERLIQUID-001`, `ASTER-001`, and `NADO-001`, unless a caller
explicitly overrides the logical id.

Risk gates are strict for spot orders: a missing, stale, or mismatched
authoritative account state rejects instead of falling back to raw cache balances.

## Reference-Data Acceptance

Funding/reference-data is a market-data contract, not an execution contract.
Implemented perp venues must expose current funding, mark price, and index price
or oracle price through `contract.DerivativeReferenceDataClient`.
`SubscribeReference` must drive `contract.ReferenceDataEvent` into
`runtime.Cache.DerivativeReference`, and adapters may use native WebSocket
streams or REST snapshot events depending on venue support.

Current open interest is intentionally query-only in phase one. It is exposed
through `contract.OpenInterestClient` and `runtime.TradingNode.OpenInterest`,
but it must not be represented as a market event or cached. The shared
`adapter/internal/runtimeaccept.CheckReferenceDataReadOnly` helper proves this
shape in Demo/Testnet: it starts a market-only runtime, subscribes reference
data, waits for fresh funding/mark/index-or-oracle in cache, queries current OI,
and asserts `OpenInterestCached` remains false.

## Live Tests

Live read tests require an explicit command:

```sh
make test-live-read
```

Production live write tests require venue-specific flags in addition to
credentials. Examples:

```sh
OKX_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/okx
BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/binance/perp
BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES=1 go test ./sdk/nado -run 'TestPlace|TestWs'
```

Live write tests may create, modify, cancel, or close real exchange state. They
must never run from `make test`. Binance Demo acceptance is separate from
production live writes: it uses Demo credentials and product-qualified make
targets. Nado's raw SDK example is Testnet-only, bypasses the adapter safety
envelope by design, and is not a release-acceptance target.

## Binance Demo Writes

Binance Demo mode uses the shared Binance Demo credential contract and Demo
endpoint selection. Create the key pair from Binance Demo/Testnet API
Management; do not use a production key pair. Some upstream Binance endpoint
names still use the word testnet, but this project exposes the validation
environment as Demo. The implemented write acceptance Demo flow covers USD-M perps and
the first Spot vertical slice. Future dated futures or options Demo flows should
add product-qualified targets while reusing `BINANCE_DEMO_*` credentials when
Binance supports them. The command shape is:

```sh
BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 \
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
BINANCE_DEMO_ORDER_QTY=0.001 \
go test -run TestBinanceDemoExecAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 \
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
BINANCE_DEMO_ORDER_QTY=0.001 \
go test -run TestBinanceSpotDemoExecAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m
```

`BINANCE_DEMO_MAX_NOTIONAL_USDT` is optional and defaults to `100`.
Direct `go test` write invocations require
`BOLTER_ENABLE_BINANCE_DEMO_WRITES=1`; the credentialed Make leaves set this
flag command-locally. Credentials alone never activate writes during the
ordinary Go suite.

`make test-binance-demo-perp` runs the same USD-M perp adapter-level target.
`make test-binance-demo-runtime-perp` runs the runtime-level Demo target through
`runtime.TradingNode`. `make test-binance-demo-spot-data` runs read-only Spot
Demo data acceptance behind `BOLTER_ENABLE_LIVE_READ_TESTS=1`.
`make test-binance-demo-spot` runs Spot Demo place/cancel/fill/cleanup
acceptance. `make test-binance-demo-runtime-spot` runs the Spot cash path
through `runtime.TradingNode`. `make test-binance-demo-acceptance` runs all
implemented Binance Demo targets, matching the NT-style split between adapter
contract acceptance and runtime acceptance.
`make test-binance-demo` is a current alias for the complete Demo acceptance
gate:

```sh
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
make test-binance-demo-acceptance
```

`BINANCE_API_KEY`, `BINANCE_SECRET_KEY`, and old `BINANCE_PERP_TESTNET_*`
variables must not be used as fallbacks in Demo mode. Demo write flows must clean
up orders/positions or Spot base-balance deltas in `defer` and print venue order
IDs plus remaining exposure/balance concerns if cleanup fails. If direct access
to Demo endpoints is blocked, pass a command-local `PROXY=...`; inherited proxy
state is not part of the framework contract.

## OKX Demo Writes

OKX Demo mode uses OKX simulated trading credentials. Create the key, secret,
and passphrase from the OKX Demo Trading environment and expose them only under
the Demo variable names:

- `OKX_DEMO_API_KEY`
- `OKX_DEMO_API_SECRET`
- `OKX_DEMO_API_PASSPHRASE`
- optional `OKX_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `OKX_DEMO_SPOT_SYMBOL`, default `ETH-USDT`
- optional `OKX_DEMO_PERP_SYMBOL`, default `ETH-USDT-SWAP`
- optional `OKX_DEMO_HOST_PROFILE`, default `global`; supported values are
  `global`, `eea`, and `custom`
- optional `OKX_DEMO_REST_BASE_URL` and `OKX_DEMO_WS_BASE_URL` when
  `OKX_DEMO_HOST_PROFILE=custom`

Production `OKX_API_*` variables are never accepted as Demo fallback
credentials. Global OKX Demo uses REST `https://openapi.okx.com` and WebSocket
`wss://wspap.okx.com:8443/ws/v5/{public,private,business}` with
`x-simulated-trading: 1`; the EEA host profile uses `https://eea.okx.com` and
`wss://wseeapap.okx.com:8443/ws/v5/{public,private,business}`. Endpoint
overrides are for regional/network troubleshooting, not for widening product
scope.

The implemented OKX acceptance scope is deliberately narrow:

- Spot: pure cash only, no margin, no leverage, no Spot positions. Runtime Spot
  exposure is balance/fill sourced and final cleanup sells any test base-asset
  delta below one size step.
- Perp: USDT-linear `*-USDT-SWAP` only. Inverse/coin-margined swaps, dated
  futures, options, spreads, and MMP/mass-cancel surfaces are out of scope for
  this phase.

Run the full OKX Demo gate with:

```sh
OKX_DEMO_API_KEY=... \
OKX_DEMO_API_SECRET=... \
OKX_DEMO_API_PASSPHRASE=... \
make test-okx-demo-acceptance
```

Direct `go test` write invocations require `BOLTER_ENABLE_OKX_DEMO_WRITES=1`;
the Make leaves set it command-locally. Official `global` and `eea` profiles do
not accept endpoint overrides for writes. A `custom` write profile additionally
requires `BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES=1`, TLS endpoints, and a
non-production WebSocket host; REST requests still enforce
`x-simulated-trading: 1`.

The adapter-level tests load Demo data, place/cancel a resting post-only order,
fill a bounded IOC order, and clean up residual Spot base deltas or Perp
exposure. Runtime-level tests construct `runtime.TradingNode`, call
`node.Resync` before and after writes, submit through `node.Exec`, and assert
cache/portfolio/metrics observations. Runtime Demo checks also assert
`node.Health()` reaches `running`, command/reconciliation latency metrics are
present, no open orders remain, and final reconciliation is flat. Tests skip
only while the explicit write flag is absent (or under `-short`). Once enabled,
missing credentials, funding, existing open orders/exposure, network/proxy,
venue rejection, implementation, and cleanup
failures are reported separately in their failure messages.

The shared `adapter/internal/runtimeaccept` harness treats account-id
consistency as acceptance evidence: account state, balances, cache mirrors,
portfolio reads, risk probes, order returns, order status reports, fill reports,
position reports, reconciliation counters, health, metrics, and lifecycle logs
must all refer to the same logical account id.

## Bybit Demo Acceptance

Bybit acceptance uses explicit Bybit Demo credentials and never falls back to
production or Testnet credentials. The first-stage aggregate targets use
Bybit's Demo Trading environment because Bybit Testnet derivative writes can be
blocked by identity/product-access requirements even when Spot writes are
available. Bybit is treated as a unified-account venue: Spot cash, USDT-linear
Perp, and USDC-linear Perp share the canonical logical `BYBIT-001` account id.
Bybit UTA 1.0, UTA 1.0 Pro, UTA 2.0, and UTA 2.0 Pro account states are
accepted as phase-one unified account preflight inputs for this Spot/linear
phase; Classic and unknown account configurations fail closed before runtime
trading.

Expose credentials and selectors under:

- `BYBIT_DEMO_API_KEY`
- `BYBIT_DEMO_API_SECRET`
- optional `BYBIT_DEMO_SYMBOL`, default `BTCUSDT`
- optional `BYBIT_DEMO_USDT_PERP_SYMBOL`, default `BTCUSDT`
- optional `BYBIT_DEMO_USDC_PERP_SYMBOL`, default `BTCPERP`
- optional `BYBIT_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `BYBIT_DEMO_MAX_NOTIONAL_USDC`, default `100`

Run the full Bybit gate with:

```sh
BYBIT_DEMO_API_KEY=... \
BYBIT_DEMO_API_SECRET=... \
make test-bybit-acceptance
```

Direct `go test` write invocations require `BOLTER_ENABLE_BYBIT_DEMO_WRITES=1`;
the Make leaves set it command-locally. Credentials alone never activate Demo
writes during the ordinary Go suite.

`BYBIT_DEMO_API_KEY` and `BYBIT_DEMO_API_SECRET` must be created after
switching a mainnet Bybit account into Demo Trading. Keys created for Bybit
Testnet or Testnet demo are rejected by `https://api-demo.bybit.com`.
Acceptance first calls the read-only `/v5/user/query-api` endpoint so an
invalid or wrong-scope key fails before account-state reconciliation and order
lifecycle checks. That preflight requires a non-read-only unified-account key
(`uta != 0`), Spot trade permission for Spot rows, and `ContractTrade:
Position` for every row because unified account-state reconciliation reads
linear positions even when validating the Spot lifecycle.

The SDK keeps Bybit's default private-REST receive window at 5000 ms. Demo
acceptance uses an internal 15000 ms override for both API-key preflight and
adapter REST traffic because the configured non-production proxy path has
exceeded five seconds. The overridden value is included identically in the
signature payload and `X-BAPI-RECV-WINDOW`; normal SDK and production adapter
construction retain the narrower default.

Bybit Make acceptance targets fail when a selected test skips. The current
2026-07-14 run passed Spot adapter/runtime and USDT Perp adapter. The USDT Perp
runtime row first exposed an expired 5000 ms request window; after the scoped
repair, both proxy and direct retries timed out before TCP connect, so USDT
runtime and USDC rows still require fresh post-fix evidence. No request reached
the venue during the blocked retry. Future reruns remain no-skip gates and
require real Demo Trading credentials, sufficient USDT/USDC demo balance, and
clean venue state.

## Bitget Demo Acceptance

Bitget acceptance uses explicit Demo/paper-trading credentials and never falls
back to production credentials. The first-stage aggregate targets use the
`demo` name because Bitget is a CEX and its non-production write surface is the
paper-trading profile. Bitget is treated as a
unified-account venue: Spot cash, USDT-linear Perp, and USDC-linear Perp share
the canonical logical `BITGET-001` account id. Only UTA/unified account
configurations are accepted for this phase; classic or unknown configurations
fail closed.

Expose credentials and selectors under:

- `BITGET_DEMO_API_KEY`
- `BITGET_DEMO_SECRET_KEY`
- `BITGET_DEMO_PASSPHRASE`
- optional `BITGET_DEMO_REST_BASE_URL`, default `https://api.bitget.com`
- optional `BITGET_DEMO_PUBLIC_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/public`
- optional `BITGET_DEMO_PRIVATE_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/private`
- optional `BITGET_DEMO_SYMBOL`, default `BTCUSDT`
- optional `BITGET_DEMO_USDT_PERP_SYMBOL`, default `BTCUSDT`
- optional `BITGET_DEMO_USDC_PERP_SYMBOL`, default `BTCPERP`
- optional `BITGET_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `BITGET_DEMO_MAX_NOTIONAL_USDC`, default `100`

Run the full Bitget gate with:

```sh
BITGET_DEMO_API_KEY=... \
BITGET_DEMO_SECRET_KEY=... \
BITGET_DEMO_PASSPHRASE=... \
make test-bitget-acceptance
```

Direct `go test` write invocations require
`BOLTER_ENABLE_BITGET_DEMO_WRITES=1`; the Make leaves set it command-locally.
Credentials alone never activate paper-trading writes during the ordinary Go
suite. Credentialed writes use the official PAP endpoint set by default;
custom write endpoints additionally require
`BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES=1` and TLS for REST and both WebSocket
connections.

`BITGET_TESTNET_*` variables remain accepted as legacy aliases for existing
local `.env` files, but new configuration should use `BITGET_DEMO_*`. Bitget
Demo defaults to the official paper-trading profile: REST requests use
`paptrading: 1` and private/public streams use `wspap.bitget.com`. Like Bybit,
historical pre-convergence G010 evidence exercised the adapter/runtime entrypoints after they
verified live market data, authoritative account-state snapshots, runtime
reconciliation into cache/portfolio, risk fail-closed behavior, private stream
startup, and a bounded resting-cancel plus IOC fill/close cleanup ladder. It
does not certify this tree. G008 reruns the NT-style noskip gate and requires
real credentials and clean venue state.

## Gate Testnet Writes

Gate acceptance uses explicit Gate Testnet credentials and never falls back to
production credentials. Gate is a CEX, but the current official non-production
write surface for this adapter is Testnet rather than a separate Demo Trading
profile. The first-stage scope is Spot cash plus USDT-linear futures/perps with
the canonical logical `GATE-001` account id. Spot margin, delivery futures,
options, and USDC-linear futures are out of scope until an official
non-production validation path is proven.

Expose credentials and selectors under:

- `GATE_TESTNET_API_KEY`
- `GATE_TESTNET_API_SECRET`
- optional `GATE_TESTNET_REST_BASE_URL`, default
  `https://api-testnet.gateapi.io/api/v4`
- optional `GATE_TESTNET_SPOT_WS_URL`, default
  `wss://ws-testnet.gate.com/v4/ws/spot`
- optional `GATE_TESTNET_USDT_FUTURES_WS_URL`, default
  `wss://ws-testnet.gate.com/v4/ws/futures/usdt`
- optional `GATE_TESTNET_SPOT_SYMBOL`, default `ETH_USDT`
- optional `GATE_TESTNET_USDT_PERP_SYMBOL`, default `BTC_USDT`
- optional `GATE_TESTNET_MAX_NOTIONAL_USDT`, default `100`

Run the full Gate gate with:

```sh
GATE_TESTNET_API_KEY=... \
GATE_TESTNET_API_SECRET=... \
make test-gate-testnet-acceptance
```

The Makefile sets `BOLTER_ENABLE_GATE_TESTNET_WRITES=1` command-locally and
wraps each target with noskip validation. `GATE_TESTNET_REST_BASE_URL`,
`GATE_TESTNET_SPOT_WS_URL`, and `GATE_TESTNET_USDT_FUTURES_WS_URL` may be
overridden for read diagnostics, but credentialed writes are enabled only when
the complete resolved profile matches the known official Testnet host set.
Unknown custom and known production hosts fail closed before writes.
Adapter-level tests load Testnet
instruments, verify order books and account-state snapshots, then run the
resting-cancel plus IOC fill/close lifecycle. Runtime-level tests construct
`runtime.TradingNode`, reconcile before and after writes, require
account-state-backed risk, assert cache/portfolio/metrics observations, start
the relevant private streams, and require no open venue orders at cleanup.

## Hyperliquid Testnet Writes

Hyperliquid acceptance uses Hyperliquid Testnet credentials and never falls back
to mainnet. Expose credentials and optional selectors under:

- `HYPERLIQUID_TESTNET_PK`
- optional `HYPERLIQUID_ACCOUNT_ADDRESS`, when trading from an address different
  from the private-key address. For API wallet keys, set this to the owner 0x
  user address; non-0x account aliases are rejected, and the adapter verifies
  the signer through Hyperliquid `userRole`.
- optional `HYPERLIQUID_TESTNET_VAULT`
- optional `HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`, default `100`
- optional `HYPERLIQUID_TESTNET_SPOT_SYMBOL`
- optional `HYPERLIQUID_TESTNET_PERP_SYMBOL`
- optional `HYPERLIQUID_TESTNET_HIP3_SYMBOL` in explicit dex-qualified
  `dex:coin` or `dex:coin-USDC` form

Read-only testnet discovery is gated by `BOLTER_ENABLE_LIVE_READ_TESTS=1` and
does not require write enablement, but Hyperliquid adapter construction still
requires account identity via `HYPERLIQUID_TESTNET_PK` or
`HYPERLIQUID_ACCOUNT_ADDRESS`. Write and runtime tests require the private key
plus `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1`; the Makefile write/runtime
targets set that enable flag command-locally. HIP-3 runtime write acceptance
also requires `HYPERLIQUID_TESTNET_HIP3_SYMBOL`. UI display symbols such as
`TSLA-USDC` can map to multiple HIP-3 dexes and are intentionally not
accepted without a dex qualifier.

Run the full Hyperliquid Testnet gate with:

```sh
HYPERLIQUID_TESTNET_PK=... \
HYPERLIQUID_TESTNET_HIP3_SYMBOL=xyz:TSLA-USDC \
make test-hyperliquid-testnet-acceptance
```

Unlike a raw `go test`, the Hyperliquid Make acceptance targets fail when a
selected acceptance test skips. A skipped write/runtime test means the venue
account, symbol, or funding preflight did not satisfy the spec and the NT-style
acceptance evidence is incomplete.

Spot acceptance performs resting submit/cancel, a bounded IOC fill, and a sell
back toward the authoritative pre-test balance while allowing only the planned
fee reserve or non-sellable dust. Unified and Portfolio margin balances come
from the private `spotState` stream; clearinghouse state contributes its
authoritative positions only. Missing currencies produce zero-balance
tombstones, malformed snapshots create a stream gap, and unknown future account
abstraction modes fail closed.

Standard Perp and HIP-3 adapter/runtime rows perform bounded fills followed by
reduce-only closes, require every final venue position report to be flat, and
leave no test-created open order. Runtime rows construct `runtime.TradingNode`,
reconcile before and after writes, require account-state-backed risk, and prove
oversized requests are rejected before venue handoff. Spot cleanup is judged by
authoritative balance delta and dust rules; it does not require the
fill-derived `Portfolio.NetQty` to be exactly zero.

The 2026-07-14 aggregate passed its first eight leaves. The final HIP-3 runtime
leaf stopped before submit because the configured real-time book was empty;
that same leaf had passed standalone earlier. This is an external liquidity
blocker, not permission to substitute a different symbol or weaken preflight.

## Lighter Testnet Writes

Lighter acceptance uses Lighter Testnet and never falls back to mainnet. Lighter
uses one unified account index selector for Spot and Perp, while runtime tests
use the logical `LIGHTER-001` account id and verify account-state cache,
portfolio, risk, and reconciliation behavior through that account id.

Expose credentials and selectors under:

- `LIGHTER_TESTNET_PRIVATE_KEY`
- `LIGHTER_TESTNET_ACCOUNT_INDEX`
- `LIGHTER_TESTNET_API_KEY_INDEX`
- optional `LIGHTER_TESTNET_MAX_NOTIONAL_USDC`, default `100`
- optional `LIGHTER_TESTNET_SPOT_SYMBOL`, default `ETH-USDC`
- optional `LIGHTER_TESTNET_PERP_SYMBOL`, default `ETH-USDC`

Read-only testnet discovery is gated by `BOLTER_ENABLE_LIVE_READ_TESTS=1` and
requires account/api-key indexes but not the private key. Write and runtime
tests require the private key plus `BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1`; the
Makefile write/runtime targets set that enable flag command-locally.

Run the full Lighter Testnet gate with:

```sh
LIGHTER_TESTNET_PRIVATE_KEY=... \
LIGHTER_TESTNET_ACCOUNT_INDEX=66 \
LIGHTER_TESTNET_API_KEY_INDEX=4 \
make test-lighter-testnet-acceptance
```

The Lighter Make acceptance targets fail when a selected acceptance test skips.
A skipped write/runtime test means the account, symbol, funding, dirty open
orders, or dirty positions did not satisfy the spec and the NT-style acceptance
evidence is incomplete.

The adapter-level tests place and cancel a conservative resting post-only order.
Runtime tests construct `runtime.TradingNode`, reconcile account state before
and after the write flow, require explicit account-id risk, submit through
`node.Exec`, observe cancel state through the runtime cache, assert no REST open
orders remain, and require a flat final cache/portfolio.

The complete five-leaf Lighter Testnet aggregate passed on 2026-07-14. This is
still deliberately resting submit/cancel coverage and must not be reported as a
fill/close proof.

## Aster And Nado Testnet

Aster acceptance uses only the official V3 Testnet profiles. Authenticated rows
require `ASTER_TESTNET_USER_ADDRESS` and
`ASTER_TESTNET_SIGNER_PRIVATE_KEY`; an optional
`ASTER_TESTNET_EXPECTED_SIGNER_ADDRESS` fails fast on signer mismatch. Set
`BOLTER_ENABLE_ASTER_TESTNET_WRITES=1` only for write rows. Optional Spot/Perp
symbols are discovered when absent, but normalized `TEST*` listings are always
rejected. `ASTER_TESTNET_MAX_NOTIONAL_USDT` defaults to `100`.

Nado acceptance uses one official Testnet unified-margin profile and logical
`NADO-001` account. Authenticated rows require `NADO_TESTNET_PRIVATE_KEY`;
`NADO_TESTNET_SUBACCOUNT_NAME` defaults to `default`, and
`NADO_TESTNET_MAX_NOTIONAL_USDT0` defaults to `100`. Spot acceptance is
funded-only/no-borrow. Perp and Spot submissions follow `ValidateSubmit`,
optional configured generic runtime risk, and ordinary adapter `Submit`.
Signing material is transient adapter-local state. The maximum-notional value
is a test safety envelope only; venue capacity remains server-authoritative.
Set `BOLTER_ENABLE_NADO_TESTNET_WRITES=1` only for write rows.

Historical live status (2026-07-14, before this architecture convergence):
read-only Spot/Perp and reference/OI gates and all four adapter/runtime rows for
both venues passed. Those rows are retained only as environment and lifecycle
evidence; they do not certify the current tree. G008 must rerun the Aster and
Nado aggregates on the frozen candidate. Nado's currently tradable Testnet
products require slightly more than the default `100 USDT0`, so run its row as
`NADO_TESTNET_MAX_NOTIONAL_USDT0=110 make test-nado-testnet-acceptance`.

Current Nado acceptance requires documented API surfaces only: local
validation, optional generic runtime risk, adapter-local signing, one-time
`place_order`, private lifecycle evidence, and bounded cleanup. The
undocumented `validate_order` query is not part of the SDK, adapter, runtime,
fixtures, or acceptance criteria.

An earlier Aster Spot run, before the full-fill close-quantity rounding bug was
repaired, left a known bounded test-asset residual. Successful post-fix rows
cleaned their own deltas. Do not sell the historical residual unless its
original balance baseline, current free balance, minimum notional, absence of
unrelated orders, and exact IOC reconciliation are all proven.

Nado Testnet acknowledges `order_update` subscriptions but did not emit order
events during the accepted write matrix. The gate therefore requires private
fill-stream evidence and verifies order lifecycle independently through
`place_order`/cancel responses, REST open orders, archive fills, runtime cache,
and reconciliation. Discovered isolated-only Perp products use 1x opening
margin rounded up to appendix x6 precision; reduce-only closes transfer zero
additional margin.

The SDK still exposes Nado's raw REST/WS `place_order` methods so the low-level
layer faithfully represents the official API. Those methods do not implement
the adapter/runtime safety envelope and are never acceptance substitutes. Their legacy
live integration tests require the separate
`BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES=1` gate; normal Nado write acceptance
uses only the generic adapter/runtime submission path.

Both venues load these values through `internal/testenv`. Optional endpoint
variables are diagnostic assertions, not arbitrary overrides: a configured URL
must equal the selected official Testnet endpoint. Config formatting redacts
private keys and proxy credentials. The default `make test` and offline gates
never require credentials or network access.

## Fixture Rules

- Prefer `httptest` servers and checked-in payload fixtures for default tests.
- Use local websocket servers for stream parsing, subscription routing, and
  reconnect behavior.
- Put fixture tests next to the SDK or adapter that owns the payload.
- Document expected balances, positions, and PnL inline in scenario tests.
- When adding a live read, call `testenv.RequireLiveRead`.
- When adding a live write, call `testenv.RequireLiveWrite`.
