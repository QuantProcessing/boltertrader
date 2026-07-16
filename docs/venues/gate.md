# Gate

[Chinese mirror](../zh-CN/venues/gate.md) · This English page is canonical.

## Scope and status

The current adapter implements Spot cash and USDT-linear Perp/SWAP. Public
non-production verification uses the known **official Gate Testnet** REST, Spot
WS, and USDT Futures WS profile. USDC Perp is explicitly deferred: the boundary
test proves that it stays closed and is not a write-acceptance target.

Both products share logical account ID `GATE-001` by default;
`Config.AccountID` overrides it. Credential names, write gate, and endpoint
safety are owned by [Configuration](../reference/configuration.md). See the
[capability matrix](../adapter-capabilities.md) for the static product rows and
[Unsupported and deferred features](../reference/unsupported.md) for the
cross-venue boundary list.

## Trading and account behavior

Submit and Cancel by venue order ID are implemented for Spot and USDT Perp.
CancelAll and Modify are unsupported for both products. Only Market and Limit
orders are normalized; Limit accepts GTC, IOC, FOK, and GTX, mapped to Gate POC.

Spot quantity is an asset amount. Use `PosNet` and `ReduceOnly=false`; the current
Spot conversion does not send those derivative fields and does not reject every
non-default combination locally, so they are not implemented Spot semantics.

USDT Perp quantity is an integer contract count: the adapter rounds the neutral
quantity to an integer and signs it from the order side. `ReduceOnly` is sent.
Before each futures Submit the adapter refreshes Gate's position mode:

- single mode requires `PosNet`;
- dual mode derives `PosLong`/`PosShort` from side and reduce-only intent and
  rejects a mismatched request;
- split or unknown modes are unsupported.

Neither leverage nor margin-mode mutation is implemented, including for USDT
Perp. Spot account state is cash-shaped; a scope containing Perp is
margin-shaped with USDT balances, margin summary, and account-backed positions.

## Market, reference, and private streams

Spot and USDT Perp implement REST order books and bars plus concrete public book,
ticker/quote, and trade subscriptions on their product-specific WebSockets.
USDT Perp implements current funding/mark/index and current OI through REST.
`SubscribeReference` emits a REST snapshot and the capability is polling, not a
reference WebSocket stream. Spot has no derivative reference/OI surface, and OI
is not runtime-cached.

Private Spot topics are orders, user trades, and balances. Private USDT Futures
topics are orders, user trades, positions, and balances; the adapter first reads
the futures account to obtain the user ID and position mode. Product-specific
disconnects emit reconciliation gaps.

## Reports, ambiguity, and cleanup

Broad order reports are open-order snapshots. Exact terminal lookup is available
by venue order ID; client-ID-only lookup remains open-order based. Fill reports
use Spot/Futures “my trades,” apply the requested time window after retrieval,
and are bounded to 100 records per mass-status query. Perp positions come from
the USDT account snapshot/report; Spot positions are unsupported.

Parsed business rejections are definitive venue rejections. Context, transport,
and non-definitive failures remain ambiguous. Reconcile the selected product,
symbol, and exact order identity before retry or cleanup. Acceptance cleanup is
limited to validation-owned orders; Spot additionally proves authoritative base
balance within its residual guard, while Perp requires selected exposure to
return flat.

## Non-production verification targets

```sh
make test-gate-testnet-read
make test-gate-testnet-spot
make test-gate-testnet-runtime-spot
make test-gate-testnet-usdt-perp
make test-gate-testnet-runtime-usdt-perp
make test-gate-testnet-usdc-perp-deferred
make test-gate-testnet-reference-data-read
make test-gate-testnet-acceptance
make test-gate-acceptance
```

The read/reference and deferred-boundary targets do not write orders. Spot and
USDT Perp lifecycle targets perform bounded Testnet writes and must run serially.
The aggregate includes both live product pairs, the read check, and the deferred
USDC boundary check. Implemented targets are not automatically
`Demo/Testnet-certified`;
use the dated, named, zero-skip evidence rules in
[Testing and evidence](../reference/testing.md).

Read [Operations and recovery](../guides/operations-recovery.md) before any
credentialed write target.
