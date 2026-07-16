# Architecture

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/architecture.md)

This page owns BolterTrader's stable layer and dependency boundaries. Detailed
submission semantics belong in [Execution and Risk](execution-risk.md), while
state convergence belongs in
[State and Reconciliation](state-reconciliation.md).

## Layers and dependency direction

| Layer | Owns | May know |
| --- | --- | --- |
| `core/` | Neutral models, enums, clocks, event envelopes, capabilities, and client contracts | Standard library and exact-decimal primitives |
| `runtime/` | Event processing, cache, portfolio, execution, risk, journal, lifecycle, and reconciliation | Neutral `core/{clock,contract,enums,model}` packages and sibling runtime packages |
| `adapter/<venue>/` | Instrument resolution, account-mode discovery, stateless request validation, normalization, stream/report semantics, and venue error mapping | `core/`, its venue SDK, and shared adapter helpers |
| `sdk/<venue>/` | Official API transport, signing, endpoints, and wire types | The venue API rather than runtime policy |
| `runtime/strategy` | Portable strategy context and callback contracts | Neutral runtime views and contracts |
| `strategy/` and `cmd/` | Example strategies and application composition | Runtime plus the explicitly selected adapter |

Within this repository, production runtime code does not import `adapter/**` or
`sdk/**`. Its project-layer dependencies are the four neutral core packages
listed above and other runtime packages; standard-library and
`shopspring/decimal` imports do not weaken that boundary. A venue name may be
data in an `InstrumentID` or capability declaration, but it must not select a
venue-specific runtime branch.

## Composition boundary

Application code chooses a venue and constructs its SDK and adapter. It passes
only `contract.MarketDataClient`, `contract.ExecutionClient`, and
`contract.AccountClient` values to `runtime.NewNode`. Any client can be absent
for a partial node, such as a market-data-only process.

The runtime obtains normalized instruments through the market client's
`InstrumentProvider`, keeps execution and account capability provenance
separate, and resolves one logical account identity from the configured neutral
clients. Strategies receive a runtime context, not an adapter or SDK handle.

## State ownership and serialization

The node consumes typed market, execution, and account envelopes through one
runtime bus. Live event mutation and recovered mutation converge through
runtime-owned cache, portfolio, fill, and callback paths. Direct reconciliation
is serialized against event application, so an authoritative snapshot cannot
race a live mutation into strategy-visible state.

The main state owners are deliberately distinct:

- cache owns normalized current state;
- portfolio owns account-aware exposure and valuation views;
- the execution journal owns command intent and outcome recovery;
- reconciliation owns scoped comparison with authoritative reports;
- lifecycle owns whether commands are active, restricted, or halted.

These components exchange neutral models. None receives an SDK response type.

## Where venue differences belong

Adapters own differences that cannot be expressed as generic policy: native
symbols or asset indexes, supported order fields, account and position modes,
request signing, endpoint profiles, stream-gap interpretation, report coverage,
and exact response identity. SDKs own their wire representation. Runtime owns
only portable orchestration after those differences have been translated into
core contracts.

The runtime submission surface is the ordinary `ExecutionClient` contract. An
adapter may prepare or sign a request internally while implementing `Submit`,
but runtime has no prepared-order, venue pre-trade lease, or venue-capacity
admission protocol. See [Execution and Risk](execution-risk.md) for the one
canonical sequence.

## Boundary checks for contributors

Before adding a runtime abstraction, ask whether every implemented venue can
express it without exposing SDK types or venue-specific lifecycle. If not, keep
the behavior in the adapter and expose only the smallest neutral capability.

Continue with [Accounts and Instruments](accounts-instruments.md), the
[adapter contributor guide](../contributing/adapters.md), and the detailed
[capability matrix](../adapter-capabilities.md).
