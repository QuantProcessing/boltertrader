# OKX

[Chinese mirror](../zh-CN/venues/okx.md) · This English page is canonical.

## Scope and status

The current tree implements OKX Spot cash and USDT-linear SWAP adapters. Public
non-production verification uses **OKX Demo Trading** in simulated mode. Official
global and EEA Demo host families are supported; custom REST/WS hosts require the
explicit custom-write opt-in enforced by the test environment.

Both adapters default to logical account ID `OKX-001`; `Config.AccountID`
overrides it. Credential names, host-profile variables, the write gate, and
secret rules belong to [Configuration](../reference/configuration.md). The
[capability matrix](../adapter-capabilities.md) owns the static rows.

## Account and product restrictions

Spot supports `TdMode=cash` and `TdMode=cross`. Demo acceptance reads the account
configuration and chooses cash for a simple account or cross otherwise. Spot
rejects reduce-only orders and non-net position sides; leverage and account
margin-mode mutation are unsupported.

USDT SWAP accepts adapter `TdMode=cross` (default) or `isolated` and places it on
each order. Demo acceptance rejects a simple account because it cannot trade the
SWAP lifecycle. `PosNet`, `PosLong`, and `PosShort` map to OKX `posSide`; the
request must match the account's net or long/short position mode. Reduce-only is
passed through. Perp leverage mutation is implemented for the selected
instrument and configured `TdMode`; `SetMarginMode` is unsupported because OKX
margin mode is an order field, not a portable account-level mutation.

## Trading behavior

| Surface | Spot cash | USDT-linear SWAP |
| --- | --- | --- |
| Submit | implemented | implemented |
| Cancel | regular orders and adapter-known algo orders | regular orders and adapter-known algo orders |
| CancelAll | symbol-scoped regular pending orders | symbol-scoped regular pending orders |
| Modify | regular orders; at least one nonzero field | regular orders; supply both positive price and quantity |

Both products implement Market, Limit, StopMarket, StopLimit,
MarketIfTouched, LimitIfTouched, and TrailingStopMarket. Conditional families use
the OKX algo endpoint; trailing orders require nonzero `TrailingOffsetBps` and
may include `ActivationPrice`.

For Limit, supported TIF values are GTC, IOC, FOK, and GTX/post-only. Spot Market
accepts unknown/GTC/IOC and rejects Market+FOK. SWAP Market accepts unknown/GTC;
Market+IOC maps to `optimal_limit_ioc`; Market+FOK is unsupported.

CancelAll enumerates the regular pending-order endpoint and does **not** enumerate
pending algo parents. Cancel known conditional orders individually. Modify also
uses the regular amend endpoint. The Spot adapter omits zero unchanged fields and
rejects a request where both are zero. The current SWAP adapter serializes both
`newSz` and `newPx`, so callers must supply both rather than relying on stale SDK
README examples or zero-as-unchanged behavior.

## Market, reference, and private streams

Both products implement REST order books and bars, and public WebSocket order
book, ticker/top-of-book quote, and trade subscriptions. USDT SWAP additionally
implements current funding, mark, and index snapshots; funding/mark/index
streams; and direct current-open-interest queries. Spot has no derivative
reference or OI surface. OI is query-only and is not runtime-cached.

`Adapter.Start` subscribes private orders for both products and private positions
for SWAP. Those order pushes produce order/fill execution events; SWAP position
pushes produce account events. The current Spot start path does not subscribe an
OKX balance/account channel, so Spot balances remain REST account-state
snapshots. Although the Spot account client has a coarse Account-stream
capability declaration, do not treat balance streaming as concretely wired.

## Reports, ambiguity, and cleanup

Broad order reports are pending/open-order snapshots; mass status also reads
pending algo parents. Historical fill reports and terminal single-order reports
are unsupported. Spot positions are balance-sourced and not a position-report
surface; SWAP positions come from the account client. Absence from complete open
coverage proves only “not open.”

Nonzero OKX per-result `sCode` values are definitive venue rejections. Transport
failures, empty/multiple results, missing IDs, and request/response identity
mismatches remain ambiguous. Reconcile by exact client/order identity before
retrying or cleaning up.

Demo adapter and runtime tests require no pre-existing open order for the selected
instrument. SWAP also requires the selected position to be flat. Spot cleanup is
bounded to tracked IDs and authoritative selected-asset balances; SWAP cleanup is
bounded to tracked IDs and selected exposure.

## Non-production verification targets

```sh
make test-okx-demo-spot
make test-okx-demo-runtime-spot
make test-okx-demo-perp
make test-okx-demo-runtime-perp
make test-okx-demo-reference-data-read
make test-okx-demo-acceptance
```

The reference-data target is read-only; the other product targets perform
bounded Demo writes and must run serially. Their existence means implemented
harnesses, not a current certification claim. A `Demo/Testnet-certified` statement needs
the dated, named, zero-skip evidence defined by
[Testing and evidence](../reference/testing.md).

See [Operations and recovery](../guides/operations-recovery.md) before running
credentialed targets. Package-local SDK README snippets are not the adapter
contract; current adapter config and current SDK request types are authoritative.
