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
make test-p6-offline
```

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
5. Account-state acceptance tests cover the NT-style safety envelope: account
   mode discovery, canonical account IDs, balance/free/locked totals, margin
   requirements, cache freshness, portfolio reads, risk fail-closed behavior,
   and reconciliation application from one authoritative snapshot.
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
- `make test-p6-offline`: the full credential-free acceptance gate for core,
  runtime, SDK, adapter, and capability behavior.
- `make test-demo-acceptance`: the aggregate credential-gated Demo acceptance
  gate for Binance, OKX, and Bybit.
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
- `make test-bitget-testnet-spot`: adapter-level Bitget Testnet Spot cash
  acceptance.
- `make test-bitget-testnet-runtime-spot`: runtime-level Bitget Testnet Spot
  cash acceptance through `runtime.TradingNode`.
- `make test-bitget-testnet-usdt-perp`: adapter-level Bitget Testnet
  USDT-linear Perp acceptance.
- `make test-bitget-testnet-runtime-usdt-perp`: runtime-level Bitget Testnet
  USDT-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bitget-testnet-usdc-perp`: adapter-level Bitget Testnet
  USDC-linear Perp acceptance.
- `make test-bitget-testnet-runtime-usdc-perp`: runtime-level Bitget Testnet
  USDC-linear Perp acceptance through `runtime.TradingNode`.
- `make test-bitget-acceptance`: complete Bitget Testnet/simulated-trading
  acceptance gate for Spot, USDT-linear Perp, and USDC-linear Perp.
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

See [`docs/developer_guide/spec_exec_testing.md`](developer_guide/spec_exec_testing.md)
for the execution acceptance spec and pass criteria.
See [`docs/adapter-capabilities.md`](adapter-capabilities.md) for the supported
adapter/product/report matrix.

## Account-State Runtime Acceptance

The account model is tested as a runtime safety envelope, not only as adapter
translation code. Default tests must prove:

- adapters that claim account-state snapshots also implement
  `contract.AccountStateReporter`;
- reconciliation applies the snapshot before balances and positions;
- the runtime cache exposes canonical account state and mirrors balances for
  older consumers during migration;
- portfolio account views can read equity, margin, and exposure from the cache;
- risk checks can require fresh account state and fail closed when free balance
  or account mode is insufficient.

`runtime.TestOfflineAccountStateSnapshotReconcilesPortfolioAndRisk` is the
fake-venue end-to-end gate for this behavior. Non-production runtime acceptance
for Binance, OKX, Bybit, Bitget, Hyperliquid, and Lighter must run the same
shape against exchange snapshots before and after order flows.

Runtime ownership is account-id based. A venue may expose multiple account
states in one process, and cache, portfolio, risk, reconciliation, balances, and
positions are keyed by account id. Product-specific venues such as Binance Spot
and USD-M may still use separate nodes, while unified venues such as Lighter can
run Spot and Perp against the same `LIGHTER:account:<index>` account id.

Risk gates are strict by default for spot orders once this account-state runtime
path is in use: a missing account state rejects instead of silently falling back
to raw cache balances. Legacy balance fallback exists only for adapters/tests
that have not implemented `AccountStateReporter` yet, and those callers must opt
in explicitly with `risk.Engine.AllowLegacyBalanceFallback()`.

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
```

Live write tests may create, modify, cancel, or close real exchange state. They
must never run from `make test`. Binance Demo acceptance is separate from
production live writes: it uses Demo credentials and product-qualified make
targets.

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
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
BINANCE_DEMO_ORDER_QTY=0.001 \
go test -run TestBinanceDemoExecAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
BINANCE_DEMO_ORDER_QTY=0.001 \
go test -run TestBinanceSpotDemoExecAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m
```

`BINANCE_DEMO_MAX_NOTIONAL_USDT` is optional and defaults to `100`.

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

The adapter-level tests load Demo data, place/cancel a resting post-only order,
fill a bounded IOC order, and clean up residual Spot base deltas or Perp
exposure. Runtime-level tests construct `runtime.TradingNode`, call
`node.Resync` before and after writes, submit through `node.Exec`, and assert
cache/portfolio/metrics observations. Runtime Demo checks also assert
`node.Health()` reaches `running`, command/reconciliation latency metrics are
present, no open orders remain, and final reconciliation is flat. Tests skip
cleanly when Demo credentials are absent and classify funding, existing open
orders/exposure, network/proxy, venue rejection, implementation, and cleanup
failures separately in their failure messages.

## Bybit Demo Acceptance

Bybit acceptance uses explicit Bybit Demo credentials and never falls back to
production or Testnet credentials. The first-stage aggregate targets use
Bybit's Demo Trading environment because Bybit Testnet derivative writes can be
blocked by identity/product-access requirements even when Spot writes are
available. Bybit is treated as a unified-account venue: Spot cash, USDT-linear
Perp, and USDC-linear Perp share the canonical `BYBIT:unified` account id.
Bybit UTA 1.0, UTA 1.0 Pro, UTA 2.0, and UTA 2.0 Pro account states are
accepted as official unified-account modes for this Spot/linear phase; Classic
and unknown account modes fail closed before runtime trading.

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

`BYBIT_DEMO_API_KEY` and `BYBIT_DEMO_API_SECRET` must be created after
switching a mainnet Bybit account into Demo Trading. Keys created for Bybit
Testnet or Testnet demo are rejected by `https://api-demo.bybit.com`.
Acceptance first calls the read-only `/v5/user/query-api` endpoint so an
invalid or wrong-scope key fails before account-state reconciliation and order
lifecycle checks. That preflight requires a non-read-only unified-account key
(`uta != 0`), Spot trade permission for Spot rows, and `ContractTrade:
Position` for every row because unified account-state reconciliation reads
linear positions even when validating the Spot lifecycle.

Bybit Make acceptance targets fail when a selected test skips. The current
adapter/runtime entrypoints verify live market data, authoritative unified
account-state snapshots, runtime reconciliation into cache/portfolio, risk
fail-closed behavior, private stream startup, and a bounded resting-cancel plus
IOC fill/close cleanup ladder. Bybit is NT-style execution accepted only after
these noskip Demo targets pass with real Demo Trading credentials, sufficient
USDT/USDC demo balance, and clean venue state.

## Bitget Testnet Acceptance

Bitget acceptance uses explicit Testnet/simulated-trading credentials and never
falls back to production credentials. The first-stage aggregate targets use the
`testnet` name for Bitget's paper trading profile. Bitget is treated as a
unified-account venue: Spot cash, USDT-linear Perp, and USDC-linear Perp share
the canonical `BITGET:unified` account id. Only UTA/unified account mode is
accepted for this phase; classic or unknown account modes fail closed.

Expose credentials and selectors under:

- `BITGET_TESTNET_API_KEY`
- `BITGET_TESTNET_SECRET_KEY`
- `BITGET_TESTNET_PASSPHRASE`
- optional `BITGET_TESTNET_REST_BASE_URL`, default `https://api.bitget.com`
- optional `BITGET_TESTNET_PUBLIC_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/public`
- optional `BITGET_TESTNET_PRIVATE_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/private`
- optional `BITGET_TESTNET_SYMBOL`, default `BTCUSDT`
- optional `BITGET_TESTNET_USDT_PERP_SYMBOL`, default `BTCUSDT`
- optional `BITGET_TESTNET_USDC_PERP_SYMBOL`, default `BTCPERP`
- optional `BITGET_TESTNET_MAX_NOTIONAL_USDT`, default `100`
- optional `BITGET_TESTNET_MAX_NOTIONAL_USDC`, default `100`

Run the full Bitget gate with:

```sh
BITGET_TESTNET_API_KEY=... \
BITGET_TESTNET_SECRET_KEY=... \
BITGET_TESTNET_PASSPHRASE=... \
make test-bitget-acceptance
```

Bitget Testnet defaults to the official paper-trading profile: REST requests use
`paptrading: 1` and private/public streams use `wspap.bitget.com`. Like Bybit,
the current adapter/runtime entrypoints verify live market data, authoritative
account-state snapshots, runtime reconciliation into cache/portfolio, risk
fail-closed behavior, private stream startup, and a bounded resting-cancel plus
IOC fill/close cleanup ladder. Bitget is NT-style execution accepted only after
these noskip Testnet targets pass with real credentials and clean venue state.

## Hyperliquid Testnet Writes

Hyperliquid acceptance uses Hyperliquid Testnet credentials and never falls back
to mainnet. Expose credentials and optional selectors under:

- `HYPERLIQUID_TESTNET_PK`
- optional `HYPERLIQUID_ACCOUNT_ADDRESS`, when trading from an address different
  from the private-key address
- optional `HYPERLIQUID_TESTNET_VAULT`
- optional `HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`, default `100`
- optional `HYPERLIQUID_TESTNET_SPOT_SYMBOL`
- optional `HYPERLIQUID_TESTNET_PERP_SYMBOL`
- optional `HYPERLIQUID_TESTNET_HIP3_SYMBOL` in explicit dex-qualified
  `dex:coin` or `dex:coin-USDC` form

Read-only testnet discovery is gated by `BOLTER_ENABLE_LIVE_READ_TESTS=1` and
does not require `HYPERLIQUID_TESTNET_PK`. Write and runtime tests require the
private key plus `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1`; the Makefile
write/runtime targets set that enable flag command-locally. HIP-3 runtime write
acceptance also requires `HYPERLIQUID_TESTNET_HIP3_SYMBOL`. UI display symbols
such as `TSLA-USDC` can map to multiple HIP-3 dexes and are intentionally not
accepted without a dex qualifier.

Run the full Hyperliquid Testnet gate with:

```sh
HYPERLIQUID_TESTNET_PK=... \
HYPERLIQUID_TESTNET_HIP3_SYMBOL=stocks:TSLA-USDC \
make test-hyperliquid-testnet-acceptance
```

Unlike a raw `go test`, the Hyperliquid Make acceptance targets fail when a
selected acceptance test skips. A skipped write/runtime test means the venue
account, symbol, or funding preflight did not satisfy the spec and the NT-style
acceptance evidence is incomplete.

The adapter-level tests place and cancel a conservative resting order. Runtime
tests construct `runtime.TradingNode`, call `node.Resync` before and after the
write flow, attach the risk engine, submit through `node.Exec`, observe cancel
state through the runtime cache, assert no REST open orders remain, and require a
flat final cache/portfolio. Perp and HIP-3 runtime tests skip when the testnet
account is not flat before the run; the Make acceptance gate reports that skip
as a failed acceptance run.

## Lighter Testnet Writes

Lighter acceptance uses Lighter Testnet and never falls back to mainnet. Lighter
uses one unified account index for Spot and Perp, so runtime tests must pass the
same account id into `runtime.WithAccountID` and verify account-state cache,
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

## Fixture Rules

- Prefer `httptest` servers and checked-in payload fixtures for default tests.
- Use local websocket servers for stream parsing, subscription routing, and
  reconnect behavior.
- Put fixture tests next to the SDK or adapter that owns the payload.
- Document expected balances, positions, and PnL inline in scenario tests.
- When adding a live read, call `testenv.RequireLiveRead`.
- When adding a live write, call `testenv.RequireLiveWrite`.
