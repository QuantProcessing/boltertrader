# Product Model v2 Design

Status: live-only design artifact. The current runtime target is a high
performance Go trading framework with a limited but closed production workflow:
live adapters, runtime state, risk, reconciliation, reconnect, observability,
and deterministic offline verification through fake live clients.

## Purpose

BolterTrader has a perp-first runtime with a small neutral product taxonomy:
`Spot`, `Perp`, and `Future`. The next product-model version should make the
core domain precise enough for spot, perpetual swap, dated future, and option
support while preserving the repository's central invariant:

- `core` expresses venue-neutral domain concepts only.
- `runtime` depends only on `core/{clock,contract,enums,model}` plus runtime
  packages.
- `adapter/*` owns venue JSON, endpoint shape, and exchange-specific coercion.
- `sdk/*` remains faithful to each official exchange API.
- `runtime/runtimetest` owns offline fake clients for runtime state-flow tests;
  it does not model exchange matching or account economics.

This design follows the same direction as NautilusTrader without copying its
Rust/Python surface directly. The upstream principles we keep are:

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
- `core/model.OrderRequest` is intentionally portable; venue-only order fields
  stay private to adapters.
- `core/contract` has product-neutral market, execution, and account clients.
- The runtime state path supports live-style order, fill, balance, position,
  market, risk, reconciliation, and observer events through a single bus
  goroutine.

The current shape is sufficient for pure-cash spot and USDT-linear perp adapter
workflows. It is not sufficient for options because option correctness depends
on expiry, strike, call/put kind, exercise style, premium treatment, settlement,
assignment/exercise lifecycle, and Greeks.

## Current Runtime Boundaries

Every execution adapter implements one venue-neutral command contract. Runtime
calls `ValidateSubmit`, then an optional explicitly configured generic local
risk checker and its generic exposure reservation, then ordinary `Submit`.
Runtime does not discover venue-specific
validators, prepare exchange payloads, hold adapter leases, query venue
capacity, or select a second submission method. Request conversion, signing,
response identity checks, conclusive venue-rejection classification, and
ambiguous transport handling belong to the adapter and its SDK.

Reconciliation uses mandatory typed coverage for open orders, fills, and
positions. Each adapter returns an owned frozen instrument selector and an
observation watermark captured before its first authoritative request for that
domain. Runtime may infer absence only from matching `CoverageComplete`
evidence and identities frozen locally before the request. Report warnings are
diagnostics only; their spelling never controls activation, cursor, position,
or order-closure behavior.

## Design Principles

1. Product identity is not just a symbol string. Product kind, underlying,
   expiry, strike, settlement, and multiplier are domain fields.
2. Product-specific fields must be typed substructures, not a flat bag of mostly
   zero fields.
3. The common `Instrument` remains the registry entry strategies and runtime use
   to resolve precision, limits, fees, margin, status, and product spec.
4. Venue payload fields stay in adapters. Core can carry portable venue
   identifiers, but it must not mirror exchange JSON or expose an order escape hatch.
5. Missing product support is explicit through capability declarations and
   `contract.ErrNotSupported`, never silent coercion.
6. Runtime behavior expands by live vertical slice: product model, fake venue
   tests, one real adapter, then Demo/testnet or opt-in live proof.

## Product Taxonomy

Proposed enum expansion:

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

## Runtime Acceptance Shape

Every product expansion should prove the same minimal live-only loop:

- Instrument metadata parses into precise `core/model` fields.
- The adapter declares capabilities explicitly and rejects unsupported operations
  with `contract.ErrNotSupported`.
- `runtime/runtimetest` covers the product's state-flow shape using fake live
  order, fill, balance, position, and market events.
- At least one real adapter path proves bounded submit/cancel/fill or read-only
  behavior under venue-specific test gates.
- Reconciliation never invents terminal order causes. If venue evidence is
  incomplete, local state remains explicit and observable as unknown/ambiguous.

## Observability Requirement

Product-model changes must expose the signals required to operate the live
system:

- order submit latency and result classification
- stream lag and reconnect count
- reconciliation report counters
- cache drift repairs
- reject and unsupported-capability counters
- venue/product labels on every metric

Observability is part of the product contract, not a polish pass.
