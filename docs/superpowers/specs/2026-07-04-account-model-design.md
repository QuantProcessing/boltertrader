# BolterTrader Account Model Design

Date: 2026-07-04
Status: Approved design baseline
Scope: live-only account, portfolio, risk, cache, and reconciliation model

## Goal

BolterTrader should become a high-performance Go live-trading framework with a limited but complete operating loop. The account model must support effective risk checks, portfolio queries, reconciliation, and observability without turning the runtime into a backtest engine or exchange-account simulator.

The design follows NautilusTrader's core layering:

- `AccountState` is a venue-reported or runtime-calculated account event.
- `Account` is the semantic account state machine.
- `Cache` stores accounts, orders, positions, and market snapshots.
- `Portfolio` is a read model over accounts, positions, prices, and fills.
- `Risk` consumes `Account` and `Portfolio` queries, not raw balances.

The design intentionally does not copy all NautilusTrader features. It keeps the live-only surface small, deterministic, observable, and efficient.

## Non-Goals

- No backtesting account simulation.
- No sandbox exchange accounting.
- No local replication of exchange-specific portfolio-margin engines.
- No full support for every account mode on every exchange in the first implementation.
- No SDK or adapter types in `core`, `runtime`, or `strategy`.
- No hidden fallback from unknown account semantics to permissive trading.

## Current State

The current runtime cache stores orders, positions, balances, and market snapshots. Balances are keyed by currency and use `AccountBalance{Currency, Total, Available, Locked}`. There is no `Account`, `AccountState`, `AccountType`, `MarginBalance`, account-by-venue index, account health, or account-state event.

The current portfolio is a fill-driven average-cost ledger. It tracks local realized PnL, net fees, fee currencies, average price, net quantity, and mark-to-market PnL for a provided mark price. It does not store venue account equity, free margin, initial margin, maintenance margin, borrow, interest, or account type.

The current reconciler pulls `Balances()` and `Positions()` separately and overwrites cache state. Positions absent from a venue snapshot are cleared. Balances absent from a snapshot are not cleared. This is a shallow cache correction, not account semantic reconciliation.

The current risk engine reads raw cache balances and positions. Spot cash orders check `Balance.Available`; derivative position limits check cached position quantity. This cannot safely handle margin, unified accounts, portfolio margin, borrow, or account health.

## NautilusTrader Reference Points

NautilusTrader's risk engine does not consume an `AccountProfile`. It reads `AccountAny` from cache, switches on concrete account type, then calls account methods such as `balance_free` and `calculate_initial_margin`.

Relevant NT concepts to mirror:

- `AccountState` carries account type, balances, margins, base currency, timestamps, and reported/system-calculated origin.
- `AccountAny` wraps concrete account types such as cash and margin.
- `MarginAccount` stores leverage, per-instrument margin, account-wide margin, and a margin model.
- `Portfolio` updates accounts from `AccountState` and exposes equity, margin, PnL, and exposure queries.
- Live adapters request initial account state during connect and wait for the account to be registered before completing connection.

BolterTrader should mirror this shape, but keep only the live runtime features needed for closed-loop trading.

## Design Summary

The account model has five runtime responsibilities:

1. Discover and validate account semantics at startup.
2. Apply initial and streaming account state into a typed `Account`.
3. Expose fast account queries for risk and strategy code.
4. Let portfolio compute limited, well-defined equity, PnL, exposure, and margin views.
5. Let reconciliation restore account, position, order, and fill truth after startup and reconnect.

The critical correction is that `AccountModeInfo` is not a risk input. It is startup and observability metadata. Risk consumes `Account` and `Portfolio`.

## Core Model

### AccountType

First implementation supports two account types:

```go
type AccountType uint8

const (
    AccountCash AccountType = iota
    AccountMargin
)
```

`Cash` means unlevered cash/inventory accounting. `Margin` is the minimal common type for futures, perps, margin spot, unified margin, and portfolio margin when the venue can report usable balances and margins.

Do not add `UnifiedAccount` or `PortfolioMarginAccount` as concrete account types in the first version. They are mode details represented through `AccountModeInfo`, `MarginBalance`, and reported account fields. If a mode cannot be safely represented, the adapter must fail-start or fail risk-increasing orders.

### AccountState

`AccountState` is the contract between adapters, reconciliation, cache, portfolio, and risk.

```go
type AccountState struct {
    AccountID    string
    Venue        string
    Type         AccountType
    BaseCurrency string

    Balances []AccountBalance
    Margins  []MarginBalance

    ModeInfo AccountModeInfo
    Reported bool
    TsEvent  time.Time
    TsInit   time.Time
}
```

Rules:

- `AccountID` and `Venue` are required.
- `Type` must be `Cash` or `Margin`.
- Empty `BaseCurrency` means multi-currency.
- `Reported=true` means venue-reported truth.
- `Reported=false` means runtime-calculated account state.
- A cash account may have empty `Margins`.
- A margin account may have account-wide margins, per-instrument margins, or both.
- Every account event must be safe to apply idempotently in event order.

### AccountBalance

Rename `Available` to `Free` to avoid cash-only semantics.

```go
type AccountBalance struct {
    Currency string
    Total    decimal.Decimal
    Free     decimal.Decimal
    Locked   decimal.Decimal
    Borrowed decimal.Decimal
    Interest decimal.Decimal
    UpdatedAt time.Time
}
```

Rules:

- For cash accounts without borrowing, `Total`, `Free`, and `Locked` must be non-negative.
- For cash accounts without borrowing, `Total == Free + Locked` is expected unless the adapter marks the value as partial through reconciliation findings.
- For margin accounts, `Free` means free margin or venue-reported available balance for the currency. It does not have to satisfy the cash invariant.
- `Borrowed` and `Interest` default to zero and are populated only when the venue reports them.

### MarginBalance

```go
type MarginBalance struct {
    Currency     string
    InstrumentID *InstrumentID
    Initial      decimal.Decimal
    Maintenance  decimal.Decimal
    UpdatedAt    time.Time
}
```

Rules:

- `InstrumentID != nil` means per-instrument margin.
- `InstrumentID == nil` means account-wide margin for that collateral currency.
- Initial and maintenance margin must be non-negative.
- Missing margins mean "not reported", not "zero required", unless the account type and venue mode guarantee no margin.

### MarginRequirement

`MarginRequirement` is the result of a local margin calculation for one order request.

```go
type MarginRequirement struct {
    Currency    string
    Initial     decimal.Decimal
    Maintenance decimal.Decimal
}
```

The first implementation may return zero maintenance when maintenance cannot be inferred locally. Risk must only use fields that are explicitly supported by the account's margin model.

### AccountModeInfo

`AccountModeInfo` is verified startup and observability metadata. It is not the primary risk input.

```go
type AccountModeInfo struct {
    Venue          string
    AccountID      string
    AccountMode    string
    MarginMode     string
    PositionMode   string
    CollateralMode string
    ProductScope   []enums.InstrumentKind
    Verified       bool
    VerifiedAt     time.Time
    Source         string
    Details        map[string]string
}
```

Examples:

- Binance Spot: `AccountMode=spot`, `MarginMode=none`, `PositionMode=net`, `CollateralMode=cash`.
- Binance USD-M: `AccountMode=futures`, `MarginMode=cross|isolated|mixed`, `PositionMode=one_way|hedge`, `CollateralMode=single_asset|multi_assets`.
- OKX Spot Cash: `AccountMode=spot`, `MarginMode=none`, `PositionMode=net`, `CollateralMode=cash`.
- OKX SWAP: `AccountMode=futures|multi_currency_margin|portfolio_margin`, `MarginMode=cross|isolated|portfolio`, `PositionMode=net|long_short`.

If `Verified` is false, the trading node cannot become trading-ready.

## Runtime Accounting

Add `runtime/accounting`.

### Account Interface

```go
type Account interface {
    ID() string
    Venue() string
    Type() model.AccountType
    BaseCurrency() string
    LastEvent() model.AccountState
    Apply(model.AccountState) error

    Balance(currency string) (model.AccountBalance, bool)
    Balances() []model.AccountBalance
    BalanceTotal(currency string) (decimal.Decimal, bool)
    BalanceFree(currency string) (decimal.Decimal, bool)
    BalanceLocked(currency string) (decimal.Decimal, bool)

    Margins() []model.MarginBalance
    MarginInitial(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool)
    MarginMaintenance(currency string, instrument *model.InstrumentID) (decimal.Decimal, bool)
}
```

The interface is read-heavy and small. Implementations should store maps keyed by currency and instrument to keep risk hot-path lookups O(1).

### CashAccount

Responsibilities:

- Store balances.
- Reject negative cash balances unless explicitly configured to allow borrowing.
- Expose `BalanceFree` for quote/base currency risk checks.
- Ignore margins.

CashAccount does not compute margin requirements.

### MarginAccount

Responsibilities:

- Store balances and margin balances.
- Store default leverage and optional per-instrument leverage.
- Expose account-wide and per-instrument margin.
- Expose `CalculateInitialMargin(req)` for simple supported cases.

Margin-only interface:

```go
type MarginAccount interface {
    Account
    Leverage(id model.InstrumentID) (decimal.Decimal, bool)
    SetLeverage(id model.InstrumentID, leverage decimal.Decimal)
    CalculateInitialMargin(req model.OrderRequest, inst model.Instrument, mark decimal.Decimal) (model.MarginRequirement, error)
}
```

Risk should type-assert to this interface only after `account.Type() == AccountMargin`.

First implementation margin model:

```text
initial margin = notional / leverage
maintenance margin = reported maintenance if available, otherwise not locally inferred
```

This is intentionally simple. For portfolio margin or other opaque venue engines, prefer venue-reported `Free`, `Initial`, `Maintenance`, and account health. If the adapter cannot report enough information for a safe increasing order, risk must reject the order.

## Cache Design

The runtime cache remains the authoritative in-memory state store. It gains account storage.

```go
type Cache struct {
    orders    map[orderKey]model.Order
    positions map[positionKey]model.Position
    market    map[string]*marketState

    accounts       map[string]accounting.Account
    accountByVenue map[string]string
}
```

Balance maps should stop being top-level truth. They can remain as compatibility read helpers if backed by the default account.

New methods:

```go
ApplyAccountState(state model.AccountState) error
Account(accountID string) (accounting.Account, bool)
AccountForVenue(venue string) (accounting.Account, bool)
Accounts() []accounting.Account
Balance(currency string) (model.AccountBalance, bool) // compatibility helper
```

Rules:

- Applying the first state for an account creates a concrete account through `AccountFactory`.
- Applying later states requires matching `AccountID`, `Venue`, `Type`, and `BaseCurrency`.
- If type changes at runtime, treat it as a fatal account-mode change and halt trading.
- `accountByVenue` supports the common one-account-per-venue path. Multiple accounts per venue can be added later by requiring explicit account IDs.

## Adapter Contract

Change `contract.AccountClient`.

Current:

```go
Balances(ctx) ([]AccountBalance, error)
Positions(ctx) ([]Position, error)
```

Target:

```go
AccountState(ctx context.Context) (model.AccountState, error)
Positions(ctx context.Context) ([]model.Position, error)
```

`Balances(ctx)` can remain temporarily as a deprecated compatibility method during migration, but runtime reconciliation should prefer `AccountState`.

Adapter responsibilities:

- Discover account mode during adapter construction or startup.
- Produce verified `AccountModeInfo`.
- Produce initial `AccountState`.
- Emit streaming `AccountState` events when the venue reports balance/margin/account updates.
- Refuse unsupported or unknown modes with clear errors.

Startup rule:

```text
If required account mode or initial account state cannot be verified, the node must fail before entering trading-ready.
```

This mirrors NT's practical behavior where modern live adapters request initial account state and wait for account registration before completing connection.

## Reconciliation

Reconciliation should move from balance snapshots to account-state snapshots.

Target reconciliation flow:

```text
AccountState() -> cache.ApplyAccountState
Positions()    -> cache.UpsertPosition / clear stale venue positions
MassStatus()   -> existing order/fill reconciliation
```

Rules:

- Startup reconciliation must apply an account state before trading starts.
- Reconnect reconciliation must pause risk-increasing submissions while account state is stale.
- Account state failures are fatal during startup.
- Account state failures during reconnect keep the node in reconciling/degraded state until fixed.
- Position absence in a complete snapshot clears stale positions.
- Balance absence in an account state means "not included in this update" unless the adapter marks the account snapshot as complete. First implementation can avoid balance deletion and require adapters to report zero balances when a currency should be cleared.

## Portfolio Design

The existing fill book remains valuable and should stay.

Portfolio becomes an NT-style read model over:

- cached accounts,
- cached positions,
- latest market prices,
- local fill book realized PnL and fees.

Public queries:

```go
Account(accountID string) (accounting.Account, bool)
Equity(accountID string) map[string]decimal.Decimal
MarginsInitial(accountID string) map[string]decimal.Decimal
MarginsMaintenance(accountID string) map[string]decimal.Decimal
RealizedPnL() decimal.Decimal
RealizedPnLNetFees() decimal.Decimal
UnrealizedPnL(id model.InstrumentID, side enums.PositionSide, mark decimal.Decimal) decimal.Decimal
NetExposure(accountID string, id model.InstrumentID) decimal.Decimal
```

Equity rules:

- Cash account: `equity = balances total + marked value of open inventory/positions`.
- Margin account: `equity = balances total + unrealized PnL`.
- If a needed mark price is missing, return partial equity and record an observable missing-price condition.
- For venue-reported portfolio margin, do not locally reproduce the risk engine. Use reported balances, margins, and health where available.

Portfolio must stay read-focused. Account state application belongs in cache/accounting, not in portfolio.

## Risk Design

Risk consumes `Account` and `Portfolio`, not `AccountModeInfo`.

First implementation checks:

```text
No account -> reject
Account stale -> reject risk-increasing order
Cash spot buy -> quote free >= notional + fee buffer
Cash spot sell -> base free >= quantity
Derivative reduce-only -> allow after basic order validation
Margin increasing order -> initial margin <= free balance in margin currency
Unsupported margin model -> reject risk-increasing order
Position limit -> keep current max resulting position checks
Kill switch -> unchanged
Duplicate client ID -> unchanged
```

Risk must distinguish:

- local risk rejection,
- unsupported account mode,
- stale account state,
- missing price,
- missing margin currency,
- venue rejection.

This preserves observability and keeps failures actionable.

## Account Staleness

Add lightweight staleness tracking:

```go
type AccountFreshness struct {
    LastAccountStateAt time.Time
    LastReconciledAt   time.Time
    StaleAfter         time.Duration
}
```

Rules:

- Startup requires a fresh account state.
- Risk-increasing orders require fresh account state.
- Reduce-only or close-only flows can be allowed when account state is stale if positions are known and the order reduces exposure.
- Staleness transitions must emit health and metrics events.

## Observability

Account model changes must be visible.

Metrics and observer events should include:

- account state applied count,
- account state age,
- account type,
- account mode info,
- account state source,
- account staleness,
- account reconciliation latency,
- account reconciliation error,
- risk rejections by reason,
- missing margin model count,
- missing price count,
- equity and free balance snapshots for selected currencies.

Do not log raw credentials or full venue payloads.

## Exchange Mapping

First implementation target:

### Binance Spot

- Type: `Cash`
- Mode: `spot`
- Position mode: `net`
- Collateral: `cash`
- Account state from spot account info / user-data updates.
- Risk supports spot buy/sell with cash inventory.

### Binance USD-M Futures

- Type: `Margin`
- Mode: `futures`
- Position mode: one-way or hedge discovered at startup.
- Margin mode: cross, isolated, or mixed by symbol.
- Collateral: single-asset or multi-assets mode if discoverable.
- Account state from futures account info.
- Risk supports basic initial-margin check for simple linear futures/perps.

### OKX Spot Cash

- Type: `Cash`
- Mode: `spot`
- `tdMode=cash`
- Risk supports cash spot buy/sell.

### OKX USDT-linear SWAP

- Type: `Margin`
- Mode: futures/multi-currency/portfolio depending on account config.
- Position mode discovered from account config.
- `tdMode` remains an adapter order parameter.
- Basic margin risk only when free balance and margin requirements are safely represented.
- Portfolio margin or opaque account modes may be discovered but disabled for risk-increasing trading until explicitly supported.

### Bybit, Gate, Hyperliquid, Lighter

No real adapter requirement in the first implementation. Use `runtime/runtimetest` fake venues to prove their account-state shapes can be represented:

- Bybit Classic / UTA as cash or margin with mode info.
- Gate unified account as margin with account-wide margin and collateral metadata.
- Hyperliquid cross/isolated as margin with mode restrictions.
- Lighter UTA and multi-asset margin as margin with reported account values.

## Performance

Account queries are on the risk hot path. Keep them simple:

- map lookups by account ID, venue, currency, and instrument;
- no SDK calls from risk;
- no reflection;
- no dynamic JSON parsing in runtime;
- no portfolio-margin stress calculation in runtime;
- decimal math only where money/price precision requires it;
- snapshots copied only at API boundaries.

All exchange-specific discovery and parsing stays in adapters.

## Migration Plan Shape

The implementation should be staged:

1. Add core model types and accounting package.
2. Add account storage to cache.
3. Add `AccountState` events and fake venue support.
4. Move reconcile to `AccountState()` first, keep balance compatibility temporarily.
5. Upgrade portfolio queries while preserving current fill-book API.
6. Upgrade risk to consume account methods.
7. Wire Binance and OKX adapters.
8. Add fake venue scenarios for Bybit, Gate, Hyperliquid, and Lighter shapes.
9. Remove deprecated top-level balance truth after tests and adapters are migrated.

## Acceptance Criteria

- A node cannot enter trading-ready without an initial account state.
- Unknown account mode fails startup or disables risk-increasing trading with an explicit unsupported reason.
- Cash spot risk no longer reads ambiguous raw balances; it reads `CashAccount.BalanceFree`.
- Margin risk reads `MarginAccount` and checks initial margin against free balance for supported modes.
- Reconciliation applies account state and remains observable through latency metrics.
- Existing realized PnL and fee behavior remains covered.
- Fake venue tests cover account state application, stale account rejection, cash risk, margin risk, and reconnect account reconciliation.
- Binance Spot, Binance USD-M, OKX Spot, and OKX SWAP map into the new model or fail closed for unsupported modes.

## Final Boundary

BolterTrader should implement a complete live account loop, not a complete exchange-account simulator.

The loop is complete when startup, account state, risk, portfolio queries, reconciliation, and observability all use one coherent account model. It is intentionally limited when a venue's account engine is too opaque to safely reproduce locally.
