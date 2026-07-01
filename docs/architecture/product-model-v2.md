# Product Model v2 Design

Status: design artifact plus first spot vertical slice. Pure cash spot now has
runtime/backtest/accounting support and a Binance Spot adapter/Demo acceptance
path. Dated future and option implementation remain future design work.

## Purpose

BolterTrader currently has a perp-first runtime with a small neutral product
taxonomy: `Spot`, `Perp`, and `Future`. The next product-model version should
make the core domain precise enough for spot, perpetual swap, dated future, and
option support while preserving the repository's central invariant:

- `core` expresses venue-neutral domain concepts only.
- `runtime` depends only on `core/{clock,contract,enums,model}`.
- `adapter/*` owns venue JSON, endpoint shape, and exchange-specific coercion.
- `sdk/*` remains faithful to each official exchange API.

This design follows the same direction as NautilusTrader without copying its
Rust/Python surface directly. The relevant upstream principles are:

- Instruments are first-class specifications for tradable assets/contracts, and
  each concrete product type implements a common instrument contract.
- Option support is explicit: option contracts carry underlying, expiry, strike,
  call/put kind, multiplier, and chain semantics rather than being generic
  symbols.
- Greeks can come from venue streams or from a local calculator; the core schema
  has a stable minimal set, with exotic/model-specific values kept outside the
  native type.
- Execution and risk distinguish local denials, venue rejections, unsupported
  capabilities, halted/reconciling states, and ambiguous outcomes.

Reference material:

- [NautilusTrader instruments](https://raw.githubusercontent.com/nautechsystems/nautilus_trader/develop/docs/concepts/instruments.md)
- [NautilusTrader options](https://raw.githubusercontent.com/nautechsystems/nautilus_trader/develop/docs/concepts/options.md)
- [NautilusTrader Greeks](https://raw.githubusercontent.com/nautechsystems/nautilus_trader/develop/docs/concepts/greeks.md)
- [NautilusTrader execution](https://raw.githubusercontent.com/nautechsystems/nautilus_trader/develop/docs/concepts/execution.md)

## Current State

Current anchors:

- `core/enums.InstrumentKind` supports `KindSpot`, `KindPerp`, and
  `KindFuture`; it has no `KindOption`.
- `core/model.InstrumentID` is `{Venue, Symbol, Kind}`.
- `core/model.Instrument` contains common identity, base/quote/settle,
  venue-identity fields, precision, limits, multiplier, and position-mode
  capability.
- `core/model.OrderRequest` is intentionally portable, with `Venue` as the
  non-portable escape hatch.
- `core/contract` has product-neutral market, execution, and account clients.
- The runtime/backtest path supports the original cross-margin perp flow and a
  first pure-cash spot flow where balances, not derivative positions, are the
  source of inventory.

The current shape is sufficient for the first pure-cash spot vertical slice and
the P1 perp kernel. It is not sufficient for options because option correctness
depends on expiry, strike, call/put kind, exercise style, premium treatment,
settlement, assignment/exercise lifecycle, and Greeks.

## Design Principles

1. Product identity is not just a symbol string. Product kind, underlying,
   expiry, strike, settlement, and multiplier are domain fields.
2. Product-specific fields must be typed substructures, not a flat bag of mostly
   zero fields.
3. The common `Instrument` remains the registry entry strategies and runtime use
   to resolve precision, limits, fees, margin, status, and product spec.
4. Venue payload fields stay in adapters or `OrderRequest.Venue`. Core can carry
   opaque venue identifiers, but it must not mirror exchange JSON.
5. Missing product support is explicit through capability declarations and
   `contract.ErrNotSupported`, never silent coercion.
6. Runtime behavior expands by vertical slice: product model, fake venue tests,
   backtest semantics, one real adapter, then live/testnet proof.

## Product Taxonomy

Proposed P4 enum expansion:

```go
type InstrumentKind uint8

const (
    KindUnknown InstrumentKind = iota
    KindSpot
    KindPerp
    KindFuture
    KindOption
)
```

The four initially supported product classes:

- Spot: cash exchange of base asset for quote asset. No expiry, no leverage by
  default, inventory and cash balances are first-class account state.
- Perp: derivative with no expiry. Has underlying, quote, settle, multiplier,
  linear/inverse/quanto settlement style, funding, mark/index price feeds, and
  margin semantics.
- Future: derivative with fixed expiry. Has underlying, quote, settle,
  multiplier, linear/inverse/quanto settlement style, delivery/cash settlement,
  mark/index feeds, and margin semantics.
- Option: derivative with fixed expiry and optional exercise/assignment
  lifecycle. Has underlying, strike, call/put kind, exercise style, premium
  currency, settlement currency/style, multiplier, Greeks data support, and
  chain membership.

## Instrument Shape

Keep a single registry type so strategies can ask the provider for an
`InstrumentID` and receive all specs needed for risk and runtime decisions.
Product-specific details live under one non-nil `Spec` variant.

Proposed P4 sketch:

```go
type Instrument struct {
    ID                  InstrumentID
    Base, Quote, Settle string

    VenueSymbol  string
    VenueIntCode *int64
    AssetIndex   *int

    PriceTick      decimal.Decimal
    SizeStep       decimal.Decimal
    MinQty         decimal.Decimal
    MaxQty         decimal.Decimal
    MinNotional    decimal.Decimal
    MaxNotional    decimal.Decimal
    PricePrecision int

    ContractMultiplier decimal.Decimal
    PositionMode       PositionModeCap
    Status             InstrumentStatus

    MakerFeeRate decimal.Decimal
    TakerFeeRate decimal.Decimal

    Spec ProductSpec
}

type ProductSpec interface {
    Kind() enums.InstrumentKind
    Validate(id InstrumentID) error
}
```

`ProductSpec` is an interface in design terms. Implementation can use an
interface, tagged struct, or explicit nullable fields after benchmarking and API
review. The important contract is exactly one product spec per instrument.

## Shared Supporting Enums

Proposed P4 domain enums:

```go
type InstrumentStatus uint8
const (
    InstrumentUnknown InstrumentStatus = iota
    InstrumentTrading
    InstrumentPreOpen
    InstrumentHalted
    InstrumentExpired
    InstrumentDelisted
)

type ContractSettlement uint8
const (
    SettlementUnknown ContractSettlement = iota
    SettlementLinear
    SettlementInverse
    SettlementQuanto
)

type DeliveryStyle uint8
const (
    DeliveryUnknown DeliveryStyle = iota
    DeliveryCash
    DeliveryPhysical
)
```

For perps and dated futures, `ContractSettlement` controls notional, PnL, and
margin formulas. `DeliveryStyle` applies to dated futures and options.

## Spot Spec

```go
type SpotSpec struct {
    BaseAsset  string
    QuoteAsset string
    CashAccount bool
    SupportsMarginBorrow bool
}
```

Semantics:

- `Quantity` is base quantity for buy/sell orders.
- Notional is `price * quantity`.
- A buy consumes quote and increases base inventory; a sell consumes base and
  increases quote.
- No `Position` is required for pure cash spot, but runtime can expose a
  position-like inventory projection for strategy convenience.
- Margin spot borrowing is not part of the first spot vertical slice unless the
  adapter declares it.

## Perp Spec

```go
type PerpSpec struct {
    Underlying       model.InstrumentID
    Settlement       ContractSettlement
    FundingInterval  time.Duration
    SupportsFunding  bool
    SupportsMark     bool
    SupportsIndex    bool
    InitialMarginRate decimal.Decimal
    MaintenanceMarginRate decimal.Decimal
}
```

Semantics:

- No expiry.
- PnL and notional use `ContractMultiplier` and `Settlement`.
- Funding is account/portfolio state, not an order fill.
- Mark/index/funding feeds are declared capabilities. Unsupported feeds produce
  `ErrNotSupported`, not zero-valued events.

## Future Spec

```go
type FutureSpec struct {
    Underlying        model.InstrumentID
    Expiry            time.Time
    Settlement        ContractSettlement
    Delivery          DeliveryStyle
    LastTradeTime     time.Time
    InitialMarginRate decimal.Decimal
    MaintenanceMarginRate decimal.Decimal
}
```

Semantics:

- `Expiry` is required and must be after instrument activation.
- `LastTradeTime` is optional; if zero, use `Expiry` for validation.
- After last trade, new exposure is denied; reduce/cancel behavior follows the
  runtime trading-state rules.
- Settlement events are account events, not fills.
- Physical delivery requires an adapter/runtime capability declaration before
  it can be considered implemented.

## Option Spec

```go
type OptionKind uint8
const (
    OptionKindUnknown OptionKind = iota
    OptionCall
    OptionPut
)

type OptionExerciseStyle uint8
const (
    ExerciseUnknown OptionExerciseStyle = iota
    ExerciseEuropean
    ExerciseAmerican
    ExerciseBermudan
)

type OptionPremiumStyle uint8
const (
    PremiumUnknown OptionPremiumStyle = iota
    PremiumUpfront
    PremiumDeferred
)

type OptionSpec struct {
    Underlying       model.InstrumentID
    SeriesID         OptionSeriesID
    Expiry           time.Time
    Strike           decimal.Decimal
    Kind             OptionKind
    ExerciseStyle    OptionExerciseStyle
    PremiumCurrency  string
    SettlementCurrency string
    Delivery         DeliveryStyle
    PremiumStyle     OptionPremiumStyle
    ContractMultiplier decimal.Decimal
    SupportsGreeks   bool
    SupportsExercise bool
    SupportsAssignment bool
}

type OptionSeriesID struct {
    Venue      string
    Underlying string
    Expiry     time.Time
}
```

Required validation:

- `InstrumentID.Kind == KindOption`.
- `Underlying` is non-zero and resolvable by the instrument provider.
- `Expiry` is non-zero.
- `Strike > 0`.
- `Kind` is call or put.
- `ExerciseStyle` is explicit.
- `PremiumCurrency` and `SettlementCurrency` are non-empty.
- `ContractMultiplier > 0`; if the common instrument multiplier is also set,
  both values must agree.

Option semantics:

- Premium is distinct from margin. An upfront premium changes cash balance when
  the opening trade fills. Deferred premium remains a liability/receivable until
  settlement.
- Premium currency is the currency used to pay/receive option price. Settlement
  currency is the currency used for exercise/expiry cash settlement. They may be
  the same but must not be assumed equal.
- A long call has positive underlying exposure as the underlying rises; a long
  put has negative underlying exposure as the underlying falls. Strategy-facing
  Greeks expose this without requiring a strategy to infer it from symbol text.
- European options can exercise only at expiry. American options can be
  exercised any time before expiry. Bermudan options require an exercise window
  schedule before implementation.
- Assignment is an account/execution lifecycle event. It must not be modeled as
  a normal market fill unless the venue explicitly reports an execution that is
  economically equivalent and the adapter preserves the assignment reason.
- Expired out-of-the-money options become terminal with no exercise cash flow.
  Expired in-the-money cash-settled options create settlement cash flow.
  Physically-settled options create underlying inventory/position changes.

## Greeks Model

BolterTrader should support two Greeks paths:

1. Venue-provided Greeks: adapters stream values from venues such as OKX, Bybit,
   Deribit, or Binance where available.
2. Local calculator: a later runtime service computes model Greeks from cached
   option, underlying, rate/yield, and volatility inputs.

Native Greeks schema:

```go
type GreeksConvention uint8
const (
    GreeksConventionUnknown GreeksConvention = iota
    GreeksPremiumAdjusted
    GreeksSpot
    GreeksForward
)

type OptionGreeks struct {
    InstrumentID     model.InstrumentID
    Convention       GreeksConvention
    Delta            decimal.Decimal
    Gamma            decimal.Decimal
    Vega             decimal.Decimal
    Theta            decimal.Decimal
    Rho              decimal.Decimal
    MarkIV           decimal.Decimal
    BidIV            decimal.Decimal
    AskIV            decimal.Decimal
    UnderlyingPrice  decimal.Decimal
    OpenInterest     decimal.Decimal
    Timestamp        time.Time
}
```

Rules:

- `Delta`, `Gamma`, `Vega`, `Theta`, and `Rho` are the stable native Greeks.
- IV, underlying price, and open interest are optional values; zero alone is not
  enough to distinguish missing from true zero. Implementation should use
  nullable decimals or a validity wrapper.
- Exotic values such as vanna, volga, charm, smile surface parameters, or model
  calibration diagnostics belong in custom data or adapter-native payloads.
- The local calculator can add `ModelPrice`, `ImpliedVol`, and `ITMProbability`
  as calculator results, not as required venue-stream fields.

## Option Chain Model

Option chain support should be per-series, not global.

```go
type OptionChainSlice struct {
    SeriesID  OptionSeriesID
    Timestamp time.Time
    Entries   []OptionChainEntry
}

type OptionChainEntry struct {
    Strike decimal.Decimal
    Call   *OptionChainSide
    Put    *OptionChainSide
}

type OptionChainSide struct {
    InstrumentID model.InstrumentID
    Quote        *model.QuoteTick
    Greeks       *OptionGreeks
    OpenInterest decimal.Decimal
}
```

Subscription design:

- `SubscribeOptionGreeks(ctx, id)` streams per-contract Greeks.
- `SubscribeOptionChain(ctx, series, filter)` streams chain slices.
- Chain filters should support fixed strikes, ATM-relative windows,
  ATM-percent bands, and delta bands.
- ATM can be derived from venue-provided forward/underlying price or from an
  explicitly supplied bootstrap price.
- A quote can arrive before Greeks, and Greeks can arrive before a quote. The
  chain aggregator keeps latest state and emits partial entries with nil quote
  or nil Greeks where appropriate.

These APIs should not be added to the base `MarketDataClient` immediately.
Instead, define optional capability interfaces in P5 so non-option venues do
not grow methods they cannot support.

## Contract v2 Shape

Keep the current base contracts stable. Add optional product-aware interfaces
that adapters can declare through `contracttest` capability suites.

```go
type DerivativeMarketDataClient interface {
    MarkPrice(ctx context.Context, id model.InstrumentID) (model.MarkPrice, error)
    IndexPrice(ctx context.Context, id model.InstrumentID) (model.IndexPrice, error)
    FundingRate(ctx context.Context, id model.InstrumentID) (model.FundingRate, error)
    SubscribeMarkPrices(ctx context.Context, id model.InstrumentID) error
    SubscribeFundingRates(ctx context.Context, id model.InstrumentID) error
}

type OptionMarketDataClient interface {
    OptionGreeks(ctx context.Context, id model.InstrumentID) (*model.OptionGreeks, error)
    SubscribeOptionGreeks(ctx context.Context, id model.InstrumentID) error
    SubscribeOptionChain(ctx context.Context, series model.OptionSeriesID, filter model.OptionChainFilter) error
}

type OptionExecutionClient interface {
    Exercise(ctx context.Context, id model.InstrumentID, qty decimal.Decimal) (*model.OptionExerciseReport, error)
}

type AccountReportsClient interface {
    SettlementReports(ctx context.Context) ([]model.SettlementReport, error)
    AssignmentReports(ctx context.Context) ([]model.AssignmentReport, error)
}
```

Event additions should also be optional and product-scoped:

- `MarkPriceEvent`
- `IndexPriceEvent`
- `FundingRateEvent`
- `OptionGreeksEvent`
- `OptionChainEvent`
- `SettlementEvent`
- `ExerciseEvent`
- `AssignmentEvent`

The base event interfaces can remain sum types. The runtime should ignore
events for capabilities it does not host, and contract tests must fail adapters
that declare a capability but emit incomplete data.

## Order and Account Semantics

Order request:

- Keep `OrderRequest` portable for all products: instrument, side, type, TIF,
  quantity, price, trigger price, position side, reduce-only.
- Do not add option-specific order fields until an exchange-supported portable
  concept appears. Exercise is a lifecycle command, not an order type.
- Venue-only flags remain in `OrderRequest.Venue`.

Balances and positions:

- Spot introduces inventory balances as first-class state. A cash account may
  not need a margin `Position`, but strategies need a stable inventory view.
- Perp/future positions continue as signed quantity with entry, mark, unrealized
  PnL, leverage, and margin rates.
- Option positions need premium basis, contract multiplier, expiry, and
  exercise/assignment terminal state. A plain signed quantity is not enough for
  accounting, but it can remain the common position quantity.

Proposed extension:

```go
type Position struct {
    InstrumentID  model.InstrumentID
    Side          enums.PositionSide
    Quantity      decimal.Decimal
    EntryPrice    decimal.Decimal
    MarkPrice     decimal.Decimal
    UnrealizedPnL decimal.Decimal
    Leverage      decimal.Decimal
    UpdatedAt     time.Time

    Product ProductPosition
}

type ProductPosition interface {
    Kind() enums.InstrumentKind
}
```

P4 may choose a tagged struct instead of an interface. The required behavior is
that option positions can carry premium basis and expiry lifecycle data without
polluting spot/perp positions.

## Runtime Impact by Product

Spot runtime:

- Portfolio/runtime expose spot inventory from balances and keep pure spot out
  of derivative `Position` state.
- Risk checks available quote/base balance for buys/sells in cash mode.
- Backtest matching applies cash balance changes and quote/base locks for
  resting orders.
- The Binance Spot adapter translates exchange info, order reports, user-data
  execution reports, and free/locked balances into the neutral contract.

Perp runtime:

- Existing path remains the migration baseline.
- Funding, mark/index, liquidation, margin, and reconciliation continue through
  declared derivative capabilities.

Future runtime:

- Reuse perp margin/PnL mechanics with fixed expiry and settlement lifecycle.
- Deny new exposure after last trade time.
- Settlement events update balances/positions after expiry.

Option runtime:

- Matching and fills update premium basis and option position quantity.
- Greeks are market data, not fills.
- Exercise/assignment/expiry are separate lifecycle events with account effects.
- Option chain aggregation belongs in data runtime, separate from order
  execution.
- Backtest option matching should start with BBO-based matching. Queue/L2 models
  are later capability work.

## Capability Matrix

The contracttest suite should grow product-aware groups:

| Group | Spot | Perp | Future | Option |
| --- | --- | --- | --- | --- |
| Instruments | required | required | required | required |
| Book/quote/trade/bar | required | required | required | required |
| Mark/index | none | declared | declared | underlying or declared |
| Funding | none | declared | none | none |
| Greeks | none | none | none | declared |
| Chain slice | none | none | none | declared |
| Submit/cancel/modify | declared | declared | declared | declared |
| Exercise | none | none | none | declared |
| Settlement reports | none | optional | required | required |
| Assignment reports | none | none | none | declared |
| Account inventory | required | balance only | balance only | premium + position |
| Margin | optional | declared | declared | optional |

Each adapter must declare exactly which groups it supports. Unsupported groups
must be declared skips or return `ErrNotSupported`.

## Validation Rules

Core model tests:

- Every `InstrumentKind` has one valid product spec and rejects mismatched specs.
- `KindOption` requires expiry, strike, call/put kind, exercise style, premium
  currency, settlement currency, and multiplier.
- `KindFuture` requires expiry and settlement.
- `KindPerp` rejects expiry.
- `KindSpot` rejects derivative-only fields.
- Tick/step precision and min/max limits remain exact decimals.
- `InstrumentID.String()` remains stable and unique across kind and venue.

Contract tests:

- Fake product providers can construct spot, perp, future, and option
  instruments with deterministic IDs.
- Capability suites make unsupported product features visible.
- Option chain tests handle quote-before-Greeks and Greeks-before-quote order.
- Exercise/assignment/settlement reports are never represented as ordinary
  market fills unless the adapter also preserves the lifecycle reason.

Runtime tests:

- Spot runtime tests cover cash fills, balance-sourced inventory,
  reconciliation, risk, and runtime snapshots.
- Future P4/P5 tests must be offline first: fake venue, then backtest, then one
  adapter product slice.

## Migration Plan

Phase 1: design only, current story.

- Add this document.
- Do not change `core/model`, `core/contract`, `runtime`, `adapter`, or `sdk`
  for product expansion.

Phase 2: core product model.

- Add `KindOption`.
- Introduce product specs and validation.
- Add unit tests for every valid/invalid product combination.
- Migrate existing perp instruments to `PerpSpec` without changing runtime
  behavior.

Phase 3: contract v2 capabilities.

- Add optional derivative and option interfaces.
- Extend `contracttest` with product-aware capability suites.
- Keep base clients stable.

Phase 4: spot vertical slice.

- Implemented for the first pure-cash scope: backtest spot accounting,
  balance-sourced runtime/risk/reconcile behavior, reusable spot capability
  contracts, Binance Spot adapter, and Binance Spot Demo data/exec acceptance.
- Additional spot venues or margin spot borrowing require their own capability
  declarations and Demo/Testnet proof.

Phase 5: dated futures vertical slice.

- Implement expiry/settlement lifecycle in fake/backtest.
- Add one real adapter path with explicit settlement reports where available.

Phase 6: options vertical slice.

- Implement option instruments, Greeks events, option chain aggregation, premium
  accounting, and exercise/assignment reports in fake/backtest.
- Add one real adapter path only after the fake/backtest contract suite passes.

## Non-Goals for the Current Wave

- No `KindOption` code change.
- No dated future or option runtime behavior.
- No dated future or option adapter wrapping.
- No margin spot borrowing.
- No local Greeks calculator implementation.
- No option chain runtime manager.
- No exercise, assignment, or settlement event implementation.
- No changes to default offline test behavior beyond existing P1 hardening.

## Acceptance Checklist

- Spot, perp, future, and option are defined as domain concepts.
- Option design includes expiry, strike, call/put, exercise style, premium
  currency, settlement currency, multiplier, Greeks, chain membership,
  exercise/assignment, and expiry lifecycle.
- Core fields are separated from adapter-only payloads.
- Contract v2 is expressed as optional capability interfaces, not forced base
  methods on every venue.
- The document explicitly limits product expansion beyond pure-cash spot:
  future and option work still requires staged design, contract, runtime, and
  adapter proof.
