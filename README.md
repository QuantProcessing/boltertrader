# BolterTrader

A Go-native trading framework. The bottom layer faithfully expresses each
exchange's official API; a middle layer exposes one venue-neutral client
contract; the runtime hosts the complex state — orders, fills, positions,
balances, PnL, risk, reconciliation, reconnect — so a strategy author faces a
stable, testable trading API.

Inspired by [NautilusTrader](https://nautilustrader.io/), built from scratch in
idiomatic Go.

## Design axiom: live-only runtime boundary

The single hard constraint: **the runtime depends only on
`core/{enums,model,contract,clock}` — never on an SDK or adapter.** Live adapters
wrap exchange SDKs behind the same three `contract` interfaces, while
`runtime/runtimetest` provides fake clients for offline verification. Time flows
through a `Clock` interface: a `RealClock` in production and a `SimulatedClock`
in deterministic tests.

```
strategy/            strategy authors implement callback interface, act via Context
   │
runtime/             hosts all stateful machinery; imports ONLY core/*
   ├─ bus/           single-goroutine event fan-in (the serialization point)
   ├─ cache/         authoritative orders / positions / balances / market snapshot
   ├─ portfolio/     average-cost realized PnL + fees + unrealized PnL
   ├─ exec/          ExecutionEngine: client-id assignment, submit, pre-trade risk
   ├─ data/          bar aggregation from trades
   ├─ risk/          pre-trade checks + kill switch
   ├─ reconcile/     correct cache from venue REST snapshots
   ├─ observ/        observability hooks + metrics snapshot
   ├─ runtimetest/   fake live clients for offline runtime verification
   └─ node.go        TradingNode wires it all together
   │
core/                venue-neutral domain (decimal everywhere; no float64)
   ├─ enums/         Side / OrderType / TimeInForce / OrderStatus / PositionSide / ...
   ├─ model/         InstrumentID, Instrument, OrderRequest, Order, Fill, Position, ...
   ├─ contract/      MarketDataClient / ExecutionClient / AccountClient + typed events
   └─ clock/         Clock interface, RealClock, SimulatedClock
   │
adapter/<venue>/     translate an SDK into the contract (Binance, OKX)
   │
sdk/<venue>/         faithful official-API clients (13 venues, pre-existing)
```

## Key invariants

- **Decimal everywhere in `core/`.** Prices and sizes are
  `shopspring/decimal`; `float64` appears only at adapter JSON boundaries.
- **One serialization point.** All state mutation happens on the bus goroutine
  — no scattered locking on the event path.
- **Venue divergence is absorbed in adapters.** Symbol-string vs asset-index,
  string vs struct order types, blocking vs async submit, hedge vs net — all
  handled below the contract. The one deliberate model-level concept is
  `PositionSide` (hedge mode is portable on Binance & OKX).
- **Non-portable knobs use an escape hatch**: `OrderRequest.Venue`.

## Quickstart — offline runtime test

Use `runtime/runtimetest` when you want a fast, deterministic runtime check with
no network. The fake clients do not match orders or model exchange accounting;
tests explicitly push the same order, fill, balance, position, quote, and trade
events that live adapters push.

```go
clk := clock.NewSimulatedClock(start)
market := runtimetest.NewFakeMarket()
exec := runtimetest.NewFakeExec()
account := runtimetest.NewFakeAccount()

node := runtime.NewNode(
    runtime.Clients{Market: market, Execution: exec, Account: account},
    clk, "offline",
    runtime.WithStrategy(myStrategy),
)

ctx, cancel := context.WithCancel(context.Background())
defer cancel()
go node.Run(ctx)

market.EmitTrade(model.TradeTick{InstrumentID: inst, Price: dec("100"), Quantity: dec("1"), Timestamp: clk.Now()})
order, _ := node.Exec.Submit(ctx, model.OrderRequest{InstrumentID: inst, Side: enums.SideBuy, Type: enums.TypeLimit, Quantity: dec("1"), Price: dec("100")})
exec.EmitOrder(model.Order{Request: order.Request, VenueOrderID: order.VenueOrderID, Status: enums.StatusFilled})
exec.EmitFill(model.Fill{InstrumentID: inst, ClientID: order.Request.ClientID, VenueOrderID: order.VenueOrderID, Side: enums.SideBuy, Price: dec("100"), Quantity: dec("1")})
```

## Quickstart — live (Binance USD-M perp)

```go
adapter, _ := perp.New(ctx, perp.Config{APIKey: key, APISecret: secret})
defer adapter.Close()
journalStore, _ := journal.OpenFile(".boltertrader/live.journal", journal.FileOptions{})
defer journalStore.Close()

node := runtime.NewNode(
    runtime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
    clock.NewRealClock(), "live",
    runtime.WithStrategy(myStrategy),
    runtime.WithBars(inst, time.Minute, "1m"),
    runtime.WithRisk(riskEngine, adapter.Market.InstrumentProvider()),
    runtime.WithJournal(journalStore),
)

node.Resync(ctx)          // reconcile cache from REST
adapter.Start(ctx)        // private user-data stream
adapter.Market.SubscribeTrades(ctx, inst)
node.Run(ctx)             // blocks
```

See [`cmd/livedemo`](cmd/livedemo/main.go) for a full env-gated live wiring and
[`strategy/strategies`](strategy/strategies/) for example strategies.

For live trading, keep `WithJournal` on a file-backed journal. Adapters expose
their logical runtime account id; `WithAccountID` is an optional expected-id
guard. The demo accepts `BT_ACCOUNT_ID` and optional `BT_JOURNAL_PATH`; when
`BT_ACCOUNT_ID` is set it must match the adapter-provided account id before the
runtime starts.

## Writing a strategy

Embed `strategy.Base` and override the callbacks you need; act through the
`Context` (never an adapter), which keeps the strategy portable across venues:

```go
type MyStrat struct{ strategy.Base }

func (s *MyStrat) OnBar(c *strategy.Context, bar model.Bar) {
    if someSignal(bar) {
        c.Buy(bar.InstrumentID, dec("0.01"), bar.Close) // limit; zero price = market
    }
}
func (s *MyStrat) OnFill(c *strategy.Context, f model.Fill) {
    log.Println("filled", f.Quantity, "@", f.Price, "PnL", c.Portfolio.RealizedPnLNetFees())
}
```

## Status

Adapters: **Binance USD-M perp**, **Binance Spot**, **OKX USDT-linear SWAP**,
**OKX Spot cash**, **Hyperliquid Spot cash**, **Hyperliquid Perp**, and
**Hyperliquid HIP-3 Perp**, **Lighter Spot cash**, and **Lighter Perp** for the
supported live/Testnet subset. The explicit support matrix is in
[`docs/adapter-capabilities.md`](docs/adapter-capabilities.md). Adding a venue
means writing one adapter; no runtime or strategy change.

## Testing

```sh
make test              # go test ./...
make test-race         # runtime race checks
make test-core         # core/runtime/strategy packages
make test-adapter      # adapter packages
make test-sdk          # SDK packages without live endpoints
make test-capabilities # adapter capability matrix docs check
make test-p6-offline   # P6 offline gate: core + adapter + sdk + matrix
```

Live read tests are opt-in:

```sh
make test-live-read
```

Live write tests are venue-specific and may create, modify, cancel, or close
real exchange state:

```sh
OKX_API_KEY=... OKX_API_SECRET=... OKX_API_PASSPHRASE=... OKX_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/okx/
BINANCE_API_KEY=... BINANCE_SECRET_KEY=... BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS=1 go test -run Live ./sdk/binance/perp/
```

Binance Demo write acceptance tests use the shared Binance Demo credential contract.
Create the keys from Binance Demo/Testnet API Management, not from a production
API key. The implemented Demo acceptance covers USD-M perps and the first Spot
vertical slice; future dated futures or options Demo flows should add their own
product-qualified targets while reusing the same `BINANCE_DEMO_*` credential
contract when Binance supports it:

```sh
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
go test -run TestBinanceDemoExecAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
BINANCE_DEMO_SYMBOL=ETH-USDT \
go test -run TestBinanceSpotDemoExecAcceptance ./adapter/binance/spot/ -count=1 -timeout=3m
```

`BINANCE_DEMO_MAX_NOTIONAL_USDT` is optional and defaults to `100`.

OKX Demo write acceptance tests use OKX simulated trading credentials, not production
`OKX_API_*` credentials. Create the API key, secret, and passphrase from OKX's
Demo Trading environment. The implemented OKX Demo acceptance covers pure-cash
Spot and USDT-linear SWAP perps only; Spot margin, inverse swaps, dated futures,
options, spreads, and production live writes are separate targets.

```sh
OKX_DEMO_API_KEY=... \
OKX_DEMO_API_SECRET=... \
OKX_DEMO_API_PASSPHRASE=... \
OKX_DEMO_SPOT_SYMBOL=ETH-USDT \
OKX_DEMO_PERP_SYMBOL=ETH-USDT-SWAP \
make test-okx-demo-acceptance
```

`OKX_DEMO_MAX_NOTIONAL_USDT` is optional and defaults to `100`.
`OKX_DEMO_HOST_PROFILE` defaults to `global`; set it to `eea` for OKX's EEA
Demo hosts, or `custom` with `OKX_DEMO_REST_BASE_URL` and
`OKX_DEMO_WS_BASE_URL` for explicit endpoint overrides. The Demo tests skip
unless all three `OKX_DEMO_*` credentials are present.

Product-qualified OKX targets are:

```sh
make test-okx-demo-spot
make test-okx-demo-runtime-spot
make test-okx-demo-perp
make test-okx-demo-runtime-perp
```

The adapter-level tests place/cancel a resting order, fill a bounded IOC order,
and clean up created Spot base deltas or Perp exposure. Runtime-level tests run
through `runtime.TradingNode`, call `node.Resync` before and after writes, and
assert runtime cache/portfolio observations. If direct access to OKX Demo hosts
is unavailable, pass a command-local `PROXY=...`; inherited shell proxy
variables are not part of the test contract.

Bybit Demo acceptance uses Bybit Demo Trading credentials generated after
switching a mainnet Bybit account into Demo Trading, not Bybit Testnet,
Testnet demo, or production trading API keys. The implemented Bybit Demo gate
covers Spot cash, USDT-linear Perp, and USDC-linear Perp through adapter and
runtime rows. Demo REST uses `https://api-demo.bybit.com`; private streams use
`wss://stream-demo.bybit.com/v5/private`.

```sh
BYBIT_DEMO_API_KEY=... \
BYBIT_DEMO_API_SECRET=... \
make test-bybit-acceptance
```

`BYBIT_DEMO_MAX_NOTIONAL_USDT` and `BYBIT_DEMO_MAX_NOTIONAL_USDC` are optional
and default to `100`. Product-qualified Bybit targets are:

```sh
make test-bybit-demo-spot
make test-bybit-demo-runtime-spot
make test-bybit-demo-usdt-perp
make test-bybit-demo-runtime-usdt-perp
make test-bybit-demo-usdc-perp
make test-bybit-demo-runtime-usdc-perp
```

Bybit Testnet keys are a separate credential scope and are rejected by the Demo
Trading REST host. If direct access to Bybit Demo hosts is unavailable, pass a
command-local `PROXY=...`.

Bitget Demo acceptance uses Bitget's paper-trading profile. It is named Demo in
BolterTrader because Bitget is a CEX; the actual REST requests use
`paptrading: 1` and the WS endpoints use `wspap.bitget.com`.

```sh
BITGET_DEMO_API_KEY=... \
BITGET_DEMO_SECRET_KEY=... \
BITGET_DEMO_PASSPHRASE=... \
make test-bitget-acceptance
```

`BITGET_DEMO_MAX_NOTIONAL_USDT` and `BITGET_DEMO_MAX_NOTIONAL_USDC` are optional
and default to `100`. Existing `BITGET_TESTNET_*` variables are accepted only as
legacy aliases for local `.env` files. Product-qualified Bitget targets are:

```sh
make test-bitget-demo-spot
make test-bitget-demo-runtime-spot
make test-bitget-demo-usdt-perp
make test-bitget-demo-runtime-usdt-perp
make test-bitget-demo-usdc-perp
make test-bitget-demo-runtime-usdc-perp
```

Hyperliquid Testnet acceptance covers Spot, standard Perp, and configured HIP-3
Perp. Read-only discovery is gated by `BOLTER_ENABLE_LIVE_READ_TESTS=1`; write
and runtime targets require `HYPERLIQUID_TESTNET_PK` and are explicitly enabled
by the Makefile with `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1`. HIP-3 also
requires `HYPERLIQUID_TESTNET_HIP3_SYMBOL` in explicit dex-qualified form,
such as raw venue `dex:coin` or runtime-neutral `dex:coin-USDC`. UI display
symbols such as `TSLA-USDC` can map to multiple HIP-3 dexes and are ambiguous.
Read-only Hyperliquid adapter construction still requires account identity:
provide either `HYPERLIQUID_TESTNET_PK` or `HYPERLIQUID_ACCOUNT_ADDRESS`.
When using a Hyperliquid API wallet, set `HYPERLIQUID_ACCOUNT_ADDRESS` to the
owner 0x user address; the adapter resolves the signer relationship through
Hyperliquid `userRole` and rejects non-0x account aliases.

```sh
HYPERLIQUID_TESTNET_PK=... \
HYPERLIQUID_TESTNET_HIP3_SYMBOL=xyz:TSLA-USDC \
make test-hyperliquid-testnet-acceptance
```

The Hyperliquid Make targets wrap `go test -json` and fail if any selected
acceptance test skips. This keeps the full gate honest: missing funding, dirty
open orders, dirty positions, or missing HIP-3 symbol config are incomplete
acceptance runs, not green runs. Runtime targets require
`AccountStateReporter` snapshots, the adapter-provided logical account id (or a
matching `runtime.WithAccountID` guard), and `risk.RequireAccountState()` before
any risk-increasing order is allowed.

Product-qualified Hyperliquid targets are:

```sh
make test-hyperliquid-testnet-spot-read
make test-hyperliquid-testnet-spot
make test-hyperliquid-testnet-runtime-spot
make test-hyperliquid-testnet-perp-read
make test-hyperliquid-testnet-perp
make test-hyperliquid-testnet-runtime-perp
make test-hyperliquid-testnet-hip3
make test-hyperliquid-testnet-runtime-hip3
```

Lighter Testnet acceptance covers Spot and Perp under one unified account index.
Read-only discovery is gated by `BOLTER_ENABLE_LIVE_READ_TESTS=1`; write and
runtime targets require `LIGHTER_TESTNET_PRIVATE_KEY`,
`LIGHTER_TESTNET_ACCOUNT_INDEX`, `LIGHTER_TESTNET_API_KEY_INDEX`, and are
enabled by the Makefile with `BOLTER_ENABLE_LIGHTER_TESTNET_WRITES=1`.

```sh
LIGHTER_TESTNET_PRIVATE_KEY=... \
LIGHTER_TESTNET_ACCOUNT_INDEX=66 \
LIGHTER_TESTNET_API_KEY_INDEX=4 \
make test-lighter-testnet-acceptance
```

Product-qualified Lighter targets are:

```sh
make test-lighter-testnet-read
make test-lighter-testnet-spot
make test-lighter-testnet-runtime-spot
make test-lighter-testnet-perp
make test-lighter-testnet-runtime-perp
```

Spot Demo data acceptance is read-only and uses the live-read gate:

```sh
BOLTER_ENABLE_LIVE_READ_TESTS=1 make test-binance-demo-spot-data
```

`make test-binance-demo-perp`, `make test-binance-demo-runtime-perp`,
`make test-binance-demo-spot-data`, and `make test-binance-demo-spot` run the
product-qualified Demo targets. `make test-binance-demo` is an alias for the
complete Demo acceptance gate:

```sh
BINANCE_DEMO_API_KEY=... \
BINANCE_DEMO_API_SECRET=... \
make test-binance-demo-acceptance
```

The write tests skip unless the Demo key pair is present. If direct access to
Binance Demo endpoints is unavailable, pass a command-local `PROXY=...`; proxy
configuration is not part of the strategy/runtime API. The old
`BINANCE_PERP_TESTNET_*` variables are not accepted as the public Demo
validation contract.

See [`docs/testing-strategy.md`](docs/testing-strategy.md) and
[`docs/review-checklist.md`](docs/review-checklist.md) for the standard gates.
