# Writing Strategies

[Chinese mirror](../zh-CN/guides/strategies.md) · This English page is canonical.

## Ownership and scope

This page owns task-oriented use of `runtime/strategy`. It explains the portable
strategy surface, callback ordering, and order submission from a strategy. Node
wiring belongs in [Running a Runtime Node](runtime-node.md); venue-specific order
support belongs in the [capability matrix](../adapter-capabilities.md).

## The strategy boundary

Strategies receive a `strategy.Context`; they do not retain adapters or SDK
clients. The context exposes the runtime clock, cache, portfolio, exactly two
order operations through `Context.Orders`—`Submit` and client-ID `Cancel`—and an
optional current-open-interest query. It does not grant `Modify`, `CancelAll`,
execution reports, account controls, or direct adapter access. Keeping venue
objects outside strategy code is what makes a strategy portable.

`Context.Buy` and `Context.Sell` are convenience methods over `Submit`: a zero
price creates a market request and a nonzero price creates a GTC limit request.
They do not add another execution permission.

Embed `strategy.Base` and override only the callbacks you need:

- `OnStart` and `OnStop` for lifecycle work;
- `OnBar`, `OnQuote`, and `OnTrade` for normalized market data;
- `OnFill` after a fill has been applied to cache and portfolio;
- `OnDerivativeReference` through the optional
  `strategy.DerivativeReferenceHandler` interface.

Callbacks are serialized with live and recovered runtime events. Startup-recovered
fills are applied before `OnStart` and delivered through `OnFill` immediately
after it, so strategy state should be initialized in `OnStart` before reacting to
those callbacks.

## Submit and cancel from a callback

The following is an excerpt, not a standalone program, from the current
[`runtime/runtimetest/exec_tester.go`](../../runtime/runtimetest/exec_tester.go).
It shows the two-method order authority granted to a strategy: submit a normalized
request, retain its runtime client ID, and cancel by that same client ID.

```go
resting, err := c.Orders.Submit(c.Ctx, model.OrderRequest{
	InstrumentID: s.instID,
	ClientID:     restingClientID,
	Side:         enums.SideBuy,
	Type:         enums.TypeLimit,
	TIF:          enums.TifGTX,
	Quantity:     s.qty,
	Price:        s.restingPrice,
	PositionSide: s.posSide,
})
if err != nil {
	s.fail(fmt.Errorf("runtime submit resting order: %w", err))
	return
}
s.recordRestingOrder(restingClientID, resting.VenueOrderID)
if err := c.Orders.Cancel(c.Ctx, restingClientID); err != nil {
	s.fail(fmt.Errorf("runtime cancel resting order: %w", err))
	return
}
```

Set a stable `ClientID` when the surrounding workflow needs to correlate an
ambiguous result or deferred cleanup. If it is empty, the execution engine assigns
one, and the returned order carries the canonical request identity. Never replace
an ambiguous submission with a new client ID; resolve the original identity as
described in [Operations and Recovery](operations-recovery.md).

## What `Submit` does

Use `Context.Orders.Submit` for explicit TIF, reduce-only, client-ID, or
position-side fields. Before the venue call, the runtime enforces its lifecycle
gate and declared operation support, asks the adapter for side-effect-free local
validation, runs optional configured venue-neutral risk, and durably records the
intent. The adapter then performs the ordinary synchronous acknowledged `Submit`.

There is no venue-specific prepared-order, lease, or capacity-admission protocol
in the strategy API. A returned error can still be ambiguous if venue handoff may
have occurred. Check [Execution and Risk](../concepts/execution-risk.md) for the
full contract and [Unsupported and Deferred Features](../reference/unsupported.md)
before relying on an optional order field.

## Reading state safely

Use `Context.Cache` for normalized current state and `Context.Portfolio` for
account-aware exposure and valuation. `Context.OpenInterest` is a direct optional
query; it calls the runtime-injected `contract.OpenInterestClient` and current OI
is not written to the runtime cache. Always handle `contract.ErrNotSupported`
because data and order surfaces vary by venue/product. See
[Market and Reference Data](market-reference-data.md) for presence flags and
freshness rules.

## Next steps

- [Run and reconcile a node](runtime-node.md)
- [Use market and reference data](market-reference-data.md)
- [Recover from ambiguous outcomes](operations-recovery.md)
- [Review venue-specific behavior](../venues/README.md)
