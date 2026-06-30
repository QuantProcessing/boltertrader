# BolterTrader Long-Term Development Design

## Purpose

BolterTrader is intended to become a Go-native trading development framework.
The bottom SDK layer should faithfully express each exchange's official API. A
middle adapter layer should expose a venue-neutral client contract. The runtime
should own complex trading state: orders, fills, positions, balances, PnL, risk,
reconnect, reconciliation, and eventually settlement/exercise workflows. Strategy
authors should face a stable, clear, testable runtime API instead of exchange SDKs.

The current codebase has a strong first milestone: a venue-neutral `core`,
runtime state machinery, Binance/OKX perpetual adapters, and a perp-focused
backtest venue. The next problem is generalization. The project must evolve from
"linear perp runtime with good boundaries" into a product-aware, venue-neutral
trading system that supports spot, perpetual swaps, dated futures, and options
without leaking venue-specific behavior into strategies.

This document is the long-range development design. It defines the architecture
direction, phase gates, testing strategy, review strategy, and stop criteria for
each stage. Detailed implementation plans should be written per phase, not for
the whole program at once.

## References

- NautilusTrader architecture: https://nautilustrader.io/docs/latest/concepts/architecture/
- NautilusTrader execution concepts: https://nautilustrader.io/docs/latest/concepts/execution/
- Current BolterTrader README architecture notes: `README.md`

## Current State

The repository already contains these important foundations:

- `core/{enums,model,contract,clock}` defines the runtime-facing domain and
  client interfaces.
- `runtime` imports only core packages and does not import SDKs or adapters.
- `runtime/backtest` implements the same contract interfaces as live adapters.
- `adapter/binance/perp` and `adapter/okx/perp` translate SDK behavior into the
  contract.
- `sdk/*` contains many exchange-specific clients and tests.
- Runtime has early versions of cache, portfolio, execution engine, risk,
  reconciliation, observability, bar aggregation, and deterministic backtest
  stepping.

The main constraints visible in the current code:

- The product model is perp-first. `InstrumentKind` lacks option support and
  `Instrument` does not yet model expiry, strike, option right/style, delivery,
  settlement method, or product-specific margin semantics.
- Portfolio and backtest accounting assume signed average-cost positions, which
  works for linear perps but is not enough for spot inventory, dated futures
  delivery, or options premium/exercise/assignment.
- `contracttest` mostly checks submit synchrony and instrument parsing. It does
  not yet enforce a full venue-neutral behavior contract.
- Default `go test ./...` is not fully offline deterministic because some
  `sdk/okx` tests touch live API behavior or live websocket subscriptions.

## Design Principles

1. Core before adapters.
   Product semantics belong in `core/model` and `core/contract` before more
   venue adapters are added. Adapters should translate, not define domain truth.

2. Product-aware, venue-neutral runtime.
   Runtime components may branch on product type when the trading domain truly
   differs, but must not branch on exchange SDK types or venue payload shapes.

3. Backtest/live parity remains a hard invariant.
   A strategy must run against backtest, sandbox, and live clients through the
   same runtime API and event semantics.

4. Behavior contracts are executable.
   Every adapter and simulated venue must pass shared conformance tests. Written
   docs are not enough to define correctness.

5. Incremental vertical slices.
   Avoid designing an abstract universal exchange model in isolation. Each model
   expansion must be validated by one real product/venue slice and one simulated
   venue slice.

6. Deterministic by default.
   Default tests must not depend on network, credentials, current exchange
   listings, wall-clock timing, or live market state.

7. Review gates are part of delivery.
   Each phase ends only when architecture, tests, docs, and code review all
   agree on the same behavior.

## Target Architecture

The long-term architecture should stay close to the current shape while making
the product boundaries explicit:

```text
strategy/
  Strategy callbacks and stable Context helpers.
  No SDK, adapter, or venue-native payload access.

runtime/
  Node lifecycle, event bus, data engine, execution engine, risk engine,
  portfolio/accounting, reconciliation, observability, and replay.
  Imports only core packages.

runtime/backtest/
  Simulated venues implementing core contracts. Matching, fees, margin,
  settlement, exercise, latency, and slippage live here or in policy packages
  below this boundary.

core/
  Venue-neutral product, order, event, account, position, instrument, and error
  model. Decimal math throughout.

adapter/<venue>/<product-or-account-mode>/
  Translation from SDK-native requests/responses/streams into core contracts.
  Owns venue quirks, symbol mapping, auth transport, websocket reconnect, and
  payload golden tests.

sdk/<venue>/
  Faithful official API clients. SDK packages can expose venue-native types and
  should avoid pretending to be portable.
```

### Runtime Components

- `DataEngine`: subscriptions, market snapshots, bars, order book snapshots,
  quote/trade events, and historical data query normalization.
- `ExecutionEngine`: client ID generation, order intent persistence, order state
  transitions, submit/cancel/modify, duplicate handling, and execution event
  routing.
- `RiskEngine`: pre-trade and runtime risk gates, kill switch, trading state
  transitions, exposure limits, and product-aware checks.
- `Portfolio` / `Accounting`: cash, inventory, positions, realized/unrealized
  PnL, fees, funding, settlement, premium, exercise, and assignment.
- `Reconciler`: authoritative snapshot correction for orders, balances,
  positions, fills, and product-specific lifecycle events.
- `Cache`: queryable runtime state with product-aware indexes but no venue SDK
  knowledge.
- `MessageBus`: one serialization point for live event mutation and deterministic
  stepping for backtest/replay.
- `Observability`: structured event logs, metrics snapshots, audit hooks, and
  diagnostics for reconnect/reconcile/risk decisions.

## Core Model Direction

### Instruments

`Instrument` should evolve from a mostly shared contract descriptor into a
product-aware descriptor:

- Shared fields: neutral ID, venue identity forms, base/quote/settle,
  tick/step/min quantity/min notional, contract multiplier, precision, trading
  status, and supported order capabilities.
- Spot spec: base asset, quote asset, lot size, quote order quantity support,
  fee asset behavior, and margin eligibility if applicable.
- Perp spec: underlying, settle asset, linear/inverse flag, funding schedule,
  margin mode support, position mode support, multiplier, and mark/index price
  feeds.
- Future spec: underlying, expiry, delivery/settlement method, linear/inverse
  flag, multiplier, final settlement price source, and margin behavior.
- Option spec: underlying, expiry, strike, call/put right, exercise style,
  premium currency, settlement method, multiplier, mark/vol/Greeks support, and
  assignment/exercise behavior.

### Orders

`OrderRequest` should keep a portable subset while becoming capability-aware:

- Common: instrument, client ID, side, order type, time in force, quantity,
  price, trigger price, reduce-only where meaningful, and venue escape hatch.
- Product/account extensions: cash/quantity mode for spot, position side for
  derivatives where supported, margin mode, close-position semantics, and option
  exercise/assignment operations as separate lifecycle commands rather than
  normal orders.
- Capability checks should reject unsupported combinations before sending them
  to venues when enough metadata is available.

### Account and Portfolio

The account model should distinguish:

- Cash balance: currency-level wallet and available funds.
- Inventory: spot asset holdings and cost basis.
- Derivative position: signed or legged exposure with entry, mark, leverage,
  margin, realized/unrealized PnL.
- Option position: contracts held/written, premium, mark value, Greeks if
  available, exercise/assignment status.
- Cash movements: fees, realized PnL, funding, deposits/withdrawals if later
  needed, settlement, exercise, assignment, and transfers.

The runtime should expose a unified portfolio view, but accounting internals
should be product-specific enough to avoid forcing spot, futures, and options
into a single signed-position abstraction.

## Contract Direction

The current three-client split should remain:

- `MarketDataClient`
- `ExecutionClient`
- `AccountClient`

The behavior contract should expand in these areas:

- Full order state machine with legal transitions and terminal states.
- Partial fills and cumulative fill quantities.
- Idempotent client IDs and duplicate request handling.
- Cancel/modify semantics for known, unknown, terminal, and venue-rejected
  orders.
- Venue-wide order reports for reconciliation.
- Fill/trade reconciliation after websocket gaps.
- Snapshot semantics for balances, positions, inventory, and open orders.
- Optional capabilities for reconnect, health, rate-limit state, account mode,
  and product-specific operations.
- Standard error classification: validation, not supported, rejected,
  rate-limited, auth, transient transport, and inconsistent venue state.

Optional capabilities should be modeled as small interfaces so basic clients do
not need fake methods for unsupported product features.

## Testing Strategy

Testing must become a first-class architecture layer.

### Test Pyramid

1. Unit tests
   Pure model, enum, state machine, risk, accounting, cache, and conversion
   tests. No network.

2. Golden fixture tests
   SDK and adapter payload conversion tests using checked-in JSON/websocket
   fixtures from official API examples or captured sanitized payloads.

3. Contract conformance tests
   Shared tests in `core/contract/contracttest` that every adapter and simulated
   venue must pass.

4. Scenario tests
   Product-level accounting and execution flows: spot buy/sell, perp open/close
   with funding, future expiry, option premium/exercise/assignment, reconnect
   and reconciliation gaps.

5. Deterministic replay tests
   Feed fixed market/event streams and assert exact final orders, balances,
   positions, PnL, and strategy callbacks.

6. Race and lifecycle tests
   `go test -race ./runtime/...` plus targeted tests for stop/start/reconnect,
   channel close, context cancellation, and no event mutation outside the runtime
   serialization point.

7. Live smoke tests
   Env-gated, minimal, and excluded from default CI. These verify credentials,
   live endpoint shape, and basic subscriptions only.

### Default Commands

The long-term default commands should be:

```sh
go test ./...
go test -race ./runtime/...
go test ./core/... ./runtime/... ./adapter/...
```

Live tests should require explicit environment variables such as:

```sh
BOLTER_LIVE_TESTS=1 go test -run Live ./adapter/...
```

### Test Data Policy

- No default test may depend on current exchange listings.
- No default test may require credentials.
- Live tests must skip clearly when disabled.
- Fixtures should be small, readable, and tied to an adapter package.
- Product scenario tests should document expected balances and PnL inline.

## Review Strategy

Every phase should have four review passes:

1. Architecture review
   Confirms boundaries, invariants, product model, and non-goals.

2. Contract/test review
   Confirms the behavior is executable through conformance and scenario tests.

3. Implementation review
   Looks for correctness, race risk, error handling, API stability, and minimal
   scope.

4. Documentation review
   Confirms README/docs/examples describe the behavior users actually get.

No phase is complete until all four review passes are satisfied.

## Development Phases

### P0: Engineering Baseline and CI Hygiene

Goal: make the repository safe to evolve for years.

Deliverables:

- Default `go test ./...` is offline deterministic.
- Live/network tests are env-gated and skipped by default.
- Add or document standard commands for unit, race, adapter, and live tests.
- Add architecture, testing, and roadmap docs.
- Establish review checklist for core/runtime/adapter changes.

Acceptance gate:

- `go test ./...` passes locally without network or credentials.
- `go test -race ./runtime/...` passes.
- Review confirms no live test is accidentally part of the default suite.

### P1: Core Product Model v2

Goal: make `core` capable of expressing spot, perp, dated future, and option
products without venue leakage.

Deliverables:

- Product-aware instrument specs.
- Option kind and option metadata.
- Settlement, expiry, delivery, multiplier, underlying, and premium semantics.
- Account/inventory/position/cash movement models.
- Model validation helpers where useful.

Acceptance gate:

- Unit tests cover every product kind and invalid combinations.
- Decimal precision tests remain green.
- No runtime or adapter imports enter `core`.
- Review confirms fields are domain concepts, not venue payload mirrors.

### P2: Contract v2 and Conformance Suite

Goal: turn the middle client layer into an executable behavior contract.

Deliverables:

- Expanded `contracttest` for order state, fills, cancel/modify, snapshots,
  reconnect, reconciliation, idempotency, errors, and optional capabilities.
- Standard error taxonomy.
- Capability interfaces for product-specific or venue-optional features.
- Contract documentation for adapter authors.

Acceptance gate:

- Backtest venue passes conformance tests.
- Binance/OKX perp adapters pass conformance tests after migration.
- Review confirms adding a new adapter begins with tests, not hand-written
  runtime exceptions.

### P3: Runtime Kernel Hardening

Goal: make runtime state reliable under live conditions.

Deliverables:

- Explicit node lifecycle and trading state.
- Central order state machine.
- Clearer data/execution/risk/accounting/reconcile boundaries.
- Event deduplication and ordering rules.
- Reconnect and reconcile lock behavior.
- Structured observability hooks for lifecycle, orders, fills, risk, and
  reconciliation.

Acceptance gate:

- Deterministic replay tests cover event gaps, duplicates, reconnect, and
  terminal order transitions.
- Race tests pass.
- Review confirms all state mutation is still serialized through the runtime
  event path or deterministic backtest stepping.

### P4: Accounting, Portfolio, and Risk Generalization

Goal: make runtime accounting and risk correct across product classes.

Deliverables:

- Spot cash/inventory accounting.
- Perp/future derivative accounting with funding and settlement hooks.
- Future expiry settlement flow.
- Option premium, mark value, exercise, assignment, and optional Greeks support.
- Product-aware risk limits and kill-switch behavior.

Acceptance gate:

- Golden accounting scenarios pass for spot, perp, future, and option.
- Risk tests cover cash insufficiency, margin insufficiency, reduce-only,
  max exposure, and halted/reconciling trading states.
- Review confirms no product class is forced into an incorrect abstraction.

### P5: Backtest and Sandbox v2

Goal: make simulation a first-class venue family that validates live parity.

Deliverables:

- Product-specific simulated venues or policy modules.
- Matching policy, fee policy, slippage policy, latency policy, and margin policy
  boundaries.
- Spot, perp, future, and option scenario support.
- Sandbox mode: live market data with simulated execution/accounting.
- Deterministic replay and benchmark fixtures.

Acceptance gate:

- Same strategy runs against backtest and fake-live clients with equivalent
  runtime event semantics.
- Replay tests have exact expected final state.
- Review confirms runtime has no backtest-only special cases.

### P6: Adapter Expansion Program

Goal: expand venue coverage without weakening core contracts.

Recommended order:

1. Migrate Binance and OKX perp to contract v2.
2. Implement one spot vertical slice, preferably Binance spot or OKX spot.
3. Implement dated futures for one venue.
4. Implement the first option vertical slice.
5. Add more venues only after conformance and scenario tests are stable.

Deliverables per adapter:

- Official API mapping notes.
- Golden request/response/stream fixtures.
- Contract conformance tests.
- Env-gated live smoke tests.
- Capability declaration.

Acceptance gate:

- Adapter passes shared conformance tests.
- Adapter-specific conversion tests cover venue quirks.
- Review confirms venue divergence stays in the adapter.

### P7: Strategy API and Production Readiness

Goal: make the framework pleasant and safe for strategy authors.

Deliverables:

- Stable `strategy.Context` API.
- Helpers for common order workflows: buy, sell, close, cancel, cancel all,
  flatten, and query open exposure.
- Multi-strategy identity and client ID namespace support.
- Stable examples for backtest and live wiring.
- Metrics, logs, audit trails, and diagnostics docs.
- Public API compatibility tests for strategy-facing packages.

Acceptance gate:

- Example strategies compile and run against backtest.
- Strategy code never imports SDK or adapter packages.
- Review confirms public API naming is stable enough to document.

## Milestone Grouping

M1: Perp Runtime Production Hardening

- P0
- P2 for current perp behavior
- P3
- Perp portion of P4/P5

M2: Spot + Perp General Runtime

- P1 spot/perp model
- P4 spot accounting/risk
- P5 spot simulation
- First spot adapter

M3: Futures + Options Multi-Asset Runtime

- P1 future/option model
- P4 future/option accounting/risk
- P5 future/option simulation
- First future and option adapters

## Non-Goals for the Next Few Phases

- Do not add many new venues before contract v2 exists.
- Do not optimize for high-frequency colocated execution before correctness and
  replayability are stable.
- Do not build a GUI or dashboard before runtime state and observability are
  dependable.
- Do not force complex venue-native features into portable APIs. Use explicit
  capability interfaces and venue escape hatches where portability is false.

## Risk Register

- Over-generalization risk: avoid by validating every abstraction with a real
  venue slice and simulated venue slice.
- Venue leakage risk: prevent with core import rules and adapter review.
- Test brittleness risk: prevent by keeping live tests out of default CI and
  using fixtures for API payloads.
- Accounting correctness risk: mitigate with golden scenarios and cash/position
  invariants.
- Runtime race risk: mitigate with one serialization point, race tests, and
  lifecycle tests.
- Strategy API churn risk: keep early helpers small and version public surfaces
  only after product semantics settle.

## Phase Template

Every phase should be planned with this template:

1. Goal
2. Non-goals
3. Core invariants
4. Model or contract changes
5. Runtime changes
6. Adapter changes
7. Backtest/sandbox changes
8. Tests to write first
9. Review checklist
10. Acceptance gate
11. Documentation updates

## Next Step

Write the first implementation plan for P0: Engineering Baseline and CI Hygiene.
That plan should be small, concrete, and immediately executable. It should fix
the current default test determinism issue, add the baseline docs, and establish
the command/review workflow that later phases rely on.
