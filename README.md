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
   ├─ backtest/      matching-engine venue + deterministic single-threaded driver
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

```go
clk := clock.NewSimulatedClock(start)
venue := backtest.NewVenue(clk, backtest.Config{
    StartBalance: model.AccountBalance{Currency: "USDT", Total: dec("10000")},
})

node := runtime.NewNode(
    runtime.Clients{Market: venue.Market(), Execution: venue.Execution(), Account: venue.Account()},
    clk, "bt",
    runtime.WithStrategy(myStrategy),
)

node.Start(ctx)
backtest.NewRunner(venue).Run(ctx, node, historicalTicks) // deterministic, single-threaded
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

## Testing

```sh
go test ./...            # offline: deterministic, no network
go test -race ./runtime/...
```

Live integration tests are env-gated and skip without credentials:

```sh
BINANCE_API_KEY=... BINANCE_API_SECRET=... go test -run TestLiveAdapterSmoke ./adapter/binance/perp/
OKX_API_KEY=... OKX_API_SECRET=... OKX_API_PASSPHRASE=... go test -run TestLiveOKXAdapterSmoke ./adapter/okx/perp/
```
