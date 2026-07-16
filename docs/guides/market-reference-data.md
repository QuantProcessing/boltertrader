# Market and Reference Data

[Chinese mirror](../zh-CN/guides/market-reference-data.md) · This English page is canonical.

## Ownership and scope

This page owns task-oriented use of normalized market data, derivative reference
data, and current open interest. Exact venue/product availability belongs in the
[capability matrix](../adapter-capabilities.md) and individual
[venue pages](../venues/README.md).

## Market data surfaces

`contract.MarketDataClient` provides instrument discovery and request/stream
surfaces for books, quotes, trades, and bars. These dimensions are independent:
a venue may implement snapshots without streams, or a coarse stream capability
without wiring every book/quote/trade subscription. Handle
`contract.ErrNotSupported` per operation rather than inferring support from a
single broad flag.

Resolve the exact `model.InstrumentID` through the market client's
`InstrumentProvider`. Then choose the narrow operation you need:

- `OrderBook` and `Bars` are synchronous REST-style requests;
- `SubscribeBook`, `SubscribeQuotes`, and `SubscribeTrades` publish typed values
  on `Events()`;
- capabilities describe the advertised surface, but the concrete call remains
  authoritative and can return `contract.ErrNotSupported`.

Normalized market envelopes feed the runtime cache and strategy callbacks. The
runtime applies its own receive/apply timestamps; those timestamps do not mean an
adapter supplied venue latency timestamps.

## Derivative reference data

Perpetual products may implement `contract.DerivativeReferenceDataClient`:

- `ReferenceSnapshot` queries the current normalized funding/reference snapshot;
- `SubscribeReference` delivers `ReferenceDataEvent` values on the normal market
  event channel;
- the runtime caches derivative reference snapshots and optionally calls
  `OnDerivativeReference` on strategies implementing the handler.

Reference streaming is distinct from order-book, quote, and trade streaming.
Lighter currently wires derivative-reference streaming for Perp, while its
book/quote/trade subscription methods remain unsupported.

A derivative reference snapshot can be partial. Check `snapshot.Fields` before
reading funding, mark, index, oracle, premium, or funding-time values; a zero
decimal is not proof that a field was absent. `FieldTimes` carries per-field
freshness after the runtime merges partial updates. Use
`Cache.DerivativeReference(instrumentID)` for the latest merged snapshot.

## Current open interest

`contract.OpenInterestClient` is an optional direct-query surface. Call
`node.OpenInterest` or `strategy.Context.OpenInterest` and handle
`contract.ErrNotSupported`. Current OI is intentionally query-only and is not
stored in `runtime.Cache`.

Inspect `OpenInterestSnapshot.Fields` before using quantity, notional, or unit;
venues do not all populate the same combination. `Timestamp` is venue/reference
time and `ReceivedAt` is local receipt time. A fresh derivative-reference cache
entry says nothing about the freshness of a separately queried OI snapshot.

The following is an excerpt, not a standalone program, from the current read-only
acceptance helper
[`adapter/internal/runtimeaccept/reference_data.go`](../../adapter/internal/runtimeaccept/reference_data.go).
It demonstrates the direct query, presence checks, and the deliberate absence of
an OI cache accessor.

```go
oi, err := node.OpenInterest(ctx, id)
if err != nil {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest: %w", label, err)
}
if oi.InstrumentID != id {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest instrument=%s, want %s", label, oi.InstrumentID, id)
}
if !oi.Fields.Has(model.OpenInterestHasQuantity) {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing quantity field: %+v", label, oi)
}
if oi.Timestamp.IsZero() || oi.ReceivedAt.IsZero() {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: open interest missing timestamps: %+v", label, oi)
}
if _, ok := any(node.Cache).(interface {
	OpenInterest(model.InstrumentID) (model.OpenInterestSnapshot, bool)
}); ok {
	return ReferenceDataReadReport{}, fmt.Errorf("%s: runtime cache unexpectedly exposes open-interest storage", label)
}
```

## Choosing a path

Use the static matrix as a cross-venue index, then read the venue page for dynamic
configuration and caveats. Validate real endpoints only through the named
read-only or Demo/Testnet targets in the [testing reference](../reference/testing.md).
Do not turn a reference-data read into an order/account probe; the repository's
read-only acceptance helper intentionally performs no submit, cancel, modify, or
order-report operation.

- [Capability matrix](../adapter-capabilities.md)
- [Configuration reference](../reference/configuration.md)
- [Unsupported and deferred features](../reference/unsupported.md)
