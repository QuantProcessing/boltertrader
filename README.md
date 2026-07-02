# BolterTrader

A Go-native trading framework. The bottom layer faithfully expresses each
exchange's official API; a middle layer exposes one venue-neutral client
contract; the runtime hosts the complex state — orders, fills, positions,
balances, PnL, risk, reconciliation, reconnect — so a strategy author faces a
stable, testable trading API.

Inspired by [NautilusTrader](https://nautilustrader.io/), built from scratch in
idiomatic Go.

## Design axiom: backtest/live parity

The single hard constraint, held from the first commit: **the runtime depends
only on `core/{enums,model,contract,clock}` — never on an SDK or adapter.** A
live adapter (wrapping an exchange SDK) and an in-process backtest matching
engine both implement the same three `contract` interfaces, so the *identical*
strategy and runtime code runs live and in backtest. Time flows through a
`Clock` interface: a `RealClock` live, a `SimulatedClock` in backtest.

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
   ├─ backtest/      perp-realistic matching venue (fees, funding, margin,
   │                 liquidation) + deterministic single-threaded driver
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
  (live) or the backtest driver goroutine — no scattered locking on the event
  path.
- **Venue divergence is absorbed in adapters.** Symbol-string vs asset-index,
  string vs struct order types, blocking vs async submit, hedge vs net — all
  handled below the contract. The one deliberate model-level concept is
  `PositionSide` (hedge mode is portable on Binance & OKX).
- **Non-portable knobs use an escape hatch**: `OrderRequest.Venue`.

## Quickstart — backtest

The backtest venue models a **linear, cross-margin perpetual account**: maker/taker
fees, average-cost positions, leverage and initial-margin gating (orders past free
margin are rejected), funding settlements, and maintenance-margin liquidation. All
of it lives inside the simulated venue — the runtime only ever sees the same
balance/position/fill events a live adapter pushes, so parity holds. Capital
effects engage only when `StartBalance` funds the account.

```go
clk := clock.NewSimulatedClock(start)
venue := backtest.NewVenue(clk, backtest.Config{
    MakerFeeRate:    dec("0.0002"),                 // 2 bps maker
    TakerFeeRate:    dec("0.0004"),                 // 4 bps taker
    Slippage:        backtest.BpsSlippage(dec("1")), // 1 bp taker slippage
    DefaultLeverage: dec("10"),
    MaintMarginRate: dec("0.005"),                  // enables liquidation at 0.5%
    StartBalance:    model.AccountBalance{Currency: "USDT", Total: dec("10000"), Available: dec("10000")},
    OnLiquidation:   func(l backtest.Liquidation) { log.Println("liquidated:", l.WalletAfter) },
})

node := runtime.NewNode(
    runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
    clk, "bt",
    runtime.WithStrategy(myStrategy),
)

node.Start(ctx)

// Trade-only replay:
backtest.NewRunner(venue).RunTrades(ctx, node, historicalTicks) // deterministic, single-threaded

// Or replay a mixed, time-sorted stream of trades + funding + mark prices:
//   events := []backtest.SimEvent{
//       backtest.Trade(tick),
//       backtest.Funding(inst, dec("0.0001"), fundingTime),
//       backtest.Mark(inst, dec("100"), markTime),
//   }
//   backtest.NewRunner(venue).Run(ctx, node, events)

node.Stop()
fmt.Println("PnL:", node.Portfolio.RealizedPnLNetFees())
```

## Quickstart — live (Binance USD-M perp)

```go
adapter, _ := perp.New(ctx, perp.Config{APIKey: key, APISecret: secret})
defer adapter.Close()

node := runtime.NewNode(
    runtime.Clients{Market: adapter.Market, Execution: adapter.Execution, Account: adapter.Account},
    clock.NewRealClock(), "live",
    runtime.WithStrategy(myStrategy),
    runtime.WithBars(inst, time.Minute, "1m"),
    runtime.WithRisk(riskEngine, adapter.Market.InstrumentProvider()),
)

node.Resync(ctx)          // reconcile cache from REST
adapter.Start(ctx)        // private user-data stream
adapter.Market.SubscribeTrades(ctx, inst)
node.Run(ctx)             // blocks
```

The *same* `myStrategy` runs in both. See [`cmd/livedemo`](cmd/livedemo/main.go)
for a full env-gated live wiring and
[`strategy/strategies`](strategy/strategies/) for example strategies.

## Writing a strategy

Embed `strategy.Base` and override the callbacks you need; act through the
`Context` (never an adapter), which keeps the strategy identical live and in
backtest:

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

Adapters: **Binance USD-M perp**, **OKX perp**. Both pass the shared
`core/contract/contracttest` suite. Adding a venue means writing one adapter; no
runtime or strategy change.

Backtest: **perp-realistic** for linear USDT-margined contracts — maker/taker
fees, slippage, average-cost PnL, leverage/cross-margin with order rejection,
funding settlements, and maintenance-margin liquidation. Scope not yet covered:
inverse/coin-margined contracts, isolated margin, order-book-level matching, and
partial-fill/queue/latency models.

## Testing

```sh
make test              # go test ./...
make test-race         # runtime race checks
make test-core         # core/runtime/strategy packages
make test-adapter      # adapter packages
make test-sdk          # SDK packages without live endpoints
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
