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
5. Deterministic replay tests feed fixed event streams and assert exact final
   orders, fills, positions, balances, and PnL.
6. Race and lifecycle tests cover runtime goroutine boundaries, cancellation,
   reconnect loops, and shutdown.
7. Live smoke tests are opt-in and excluded from default verification.

## NT-Style Acceptance Ladder

BolterTrader follows NautilusTrader's testing shape: keep normal CI
deterministic, then run venue-backed spec acceptance explicitly when credentials
and exchange state are available.

- `make test-core`: local core/runtime/strategy behavior.
- `make test-adapter`: venue-neutral adapter contracts.
- `make test-sdk`: SDK request/response/stream behavior without live writes.
- `make test-binance-demo-perp`: adapter-level Binance USD-M Demo execution.
- `make test-binance-demo-runtime-perp`: runtime-level Binance USD-M Demo
  execution through `runtime.TradingNode`.
- `make test-binance-demo-spot-data`: read-only Binance Spot Demo data
  acceptance.
- `make test-binance-demo-spot`: adapter-level Binance Spot Demo execution.
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

See [`docs/developer_guide/spec_exec_testing.md`](developer_guide/spec_exec_testing.md)
for the execution acceptance spec and pass criteria.
See [`docs/adapter-capabilities.md`](adapter-capabilities.md) for the supported
adapter/product/report matrix.

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
acceptance. `make test-binance-demo-acceptance` runs all implemented Binance
Demo targets, matching the NT-style split between adapter contract acceptance
and runtime acceptance.
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

## Fixture Rules

- Prefer `httptest` servers and checked-in payload fixtures for default tests.
- Use local websocket servers for stream parsing, subscription routing, and
  reconnect behavior.
- Put fixture tests next to the SDK or adapter that owns the payload.
- Document expected balances, positions, and PnL inline in scenario tests.
- When adding a live read, call `testenv.RequireLiveRead`.
- When adding a live write, call `testenv.RequireLiveWrite`.
