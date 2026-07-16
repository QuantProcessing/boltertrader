# Binance

[Chinese mirror](../zh-CN/venues/binance.md) · This English page is canonical.

## Scope and status

The current tree implements separate Binance adapters for Spot cash and USD-M
Perp. The public non-production path is **Binance Demo** for both products. This
page describes method-level behavior; the [capability matrix](../adapter-capabilities.md)
owns the product rows, and the [glossary](../reference/glossary.md) owns the
meaning of implemented, capability-advertised, and `Demo/Testnet-certified`.

Both adapters default to the logical account ID `BINANCE-001`; `Config.AccountID`
overrides it. Credentials, the explicit write gate, and secret-handling rules are
owned by [Configuration](../reference/configuration.md). If two credential sets
are used in one process, give them distinct logical account IDs.

## Trading behavior

| Surface | Spot cash | USD-M Perp |
| --- | --- | --- |
| Submit | implemented | implemented |
| Cancel | implemented by venue order ID | implemented for regular and known conditional orders |
| CancelAll | implemented, symbol-scoped | implemented, symbol-scoped; covers regular and open conditional orders |
| Modify | unsupported | implemented for regular resting orders |
| Leverage mutation | unsupported | implemented; decimal input is reduced to its integer part |
| Margin-mode mutation | unsupported | `cross` and `isolated` map to Binance margin types |

Spot accepts Market, Limit, StopMarket, StopLimit, MarketIfTouched, and
LimitIfTouched. Limit-family orders accept GTC, IOC, or FOK; plain
`Limit + GTX` maps to Binance `LIMIT_MAKER`. Spot explicitly rejects
`ReduceOnly=true` and any position side other than `PosNet`.

USD-M Perp accepts the same families plus TrailingStopMarket. Limit-family
orders accept GTC, IOC, FOK, or GTX. Trailing orders require a nonzero
`TrailingOffsetBps`; `ActivationPrice` is optional. `ReduceOnly` is sent to the
venue, and `PosNet`, `PosLong`, and `PosShort` map to Binance one-way/hedge
position-side fields. The account's venue-side position mode must agree with the
request.

Perp Modify first reads the existing regular order because Binance amendment
requires side, quantity, and price. A zero `newPrice` or `newQty` means “retain
the existing value.” Conditional/algo-order amendment is not implemented. Spot
Modify is intentionally disabled: Binance cancel-replace creates two venue
incarnations, while runtime order identity is currently single-incarnation.

## Market, reference, and private streams

Both products implement REST order-book snapshots and bars, plus concrete public
book, quote, and aggregate-trade subscriptions:

| Product | Book | Quote | Trade | Derivative reference | Open interest |
| --- | --- | --- | --- | --- | --- |
| Spot | limited depth | book ticker | aggregate trade | not applicable | not applicable |
| USD-M Perp | limited depth | book ticker | aggregate trade | REST funding/mark/index snapshot plus mark-price stream | current direct query |

Current open interest is query-only; runtime does not subscribe or cache it.
`Adapter.Start` opens the private user-data stream. Spot routes execution reports
and account-position/balance updates. Perp routes order updates and account
updates containing balances and positions. Reconnect gaps are surfaced for
authoritative reconciliation.

## Reports, ambiguity, and cleanup

Order-status and mass-status recovery are open-order based. Perp mass status also
includes pending conditional orders. Neither adapter implements historical fill
reports, and neither advertises a terminal single-order report. Therefore, an
order absent from complete open-order coverage is only proven not open; its
terminal cause and fills remain unknown. Spot has no normalized position report;
Perp positions come from the account client snapshot.

Parsed business rejections are marked as definitive venue rejections. Transport
errors and malformed, empty, or identity-mismatched success envelopes remain
ambiguous. Do not blindly retry an ambiguous Submit or compensate without exact
client/order identity and account evidence.

The Spot harness tracks only its validation IDs, selected symbol, and
authoritative base balance; a clean exit allows a residual base delta below one
size step. Perp cleanup is likewise selected-product scoped and requires no
validation-owned open order and a flat selected position. Neither assertion
proves whole-account equality.

## Non-production verification targets

Run external-write targets serially. The exact implemented targets are:

```sh
make test-binance-demo-spot-data
make test-binance-demo-spot
make test-binance-demo-runtime-spot
make test-binance-demo-perp
make test-binance-demo-runtime-perp
make test-binance-demo-reference-data-read
make test-binance-demo-acceptance
```

`test-binance-demo-spot-data` and the reference-data target are read-only;
adapter/runtime lifecycle targets write bounded Demo state. These target names
show that harnesses are implemented. This page does not claim a current
`Demo/Testnet-certified` result: that additionally requires a named candidate, date,
scope, zero skips, and terminal assertions as defined in
[Testing and evidence](../reference/testing.md).

Before a write run, read [Operations and recovery](../guides/operations-recovery.md)
and the canonical [Binance Spot Demo walkthrough](../getting-started/cex-demo.md).
