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
- `make test-binance-demo-acceptance`: complete Binance USD-M Demo acceptance
  gate.

See [`docs/developer_guide/spec_exec_testing.md`](developer_guide/spec_exec_testing.md)
for the execution acceptance spec and pass criteria.

## Live Tests

Live read tests require an explicit command:

```sh
make test-live-read
```

Live write tests require venue-specific flags in addition to credentials.
Examples:

```sh
OKX_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/okx
BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/binance/perp
```

Live write tests may create, modify, cancel, or close real exchange state. They
must remain explicitly gated and must never run from `make test`.

## Binance USD-M Demo Writes

Binance USD-M perp Demo mode uses the shared Binance Demo credential contract
and Demo endpoint selection. Create the key pair from Binance Futures Demo
Trading API Management at `https://testnet.binancefuture.com`; do not use a
production key pair. Some upstream Binance endpoint names still use the word
testnet, but this project exposes the validation environment as Demo. The
implemented write/E2E Demo flow currently covers USD-M perps only; future spot,
dated futures, or options Demo flows should add product-qualified targets while
reusing `BINANCE_DEMO_*` credentials when Binance supports them. The command
shape is:

```sh
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
BINANCE_DEMO_ORDER_QTY=0.001 \
go test -run TestBinanceDemoExecE2E ./adapter/binance/perp/ -count=1 -timeout=3m
```

`BINANCE_DEMO_MAX_NOTIONAL_USDT` is optional and defaults to `100`.

`make test-binance-demo-perp` runs the same USD-M perp adapter-level target.
`make test-binance-demo-runtime-perp` runs the runtime-level Demo target through
`runtime.TradingNode`. `make test-binance-demo-acceptance` runs both, matching
the NT-style split between adapter contract acceptance and runtime acceptance.
`make test-binance-demo` is a current alias for the complete Demo acceptance
gate:

```sh
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
make test-binance-demo-acceptance
```

`BINANCE_API_KEY`, `BINANCE_SECRET_KEY`, and old `BINANCE_PERP_TESTNET_*`
variables must not be used as fallbacks in Demo mode. Demo write flows must clean
up orders/positions in `defer` and print venue order IDs plus remaining exposure
if cleanup fails.

## Fixture Rules

- Prefer `httptest` servers and checked-in payload fixtures for default tests.
- Use local websocket servers for stream parsing, subscription routing, and
  reconnect behavior.
- Put fixture tests next to the SDK or adapter that owns the payload.
- Document expected balances, positions, and PnL inline in scenario tests.
- When adding a live read, call `testenv.RequireLiveRead`.
- When adding a live write, call `testenv.RequireLiveWrite`.
