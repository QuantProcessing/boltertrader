# Bitget

[Chinese mirror](../zh-CN/venues/bitget.md) · This English page is canonical.

## Scope and status

The current adapter implements Spot plus USDT- and USDC-settled linear
Perp/SWAP. Public non-production verification uses **Bitget Demo/PAP**: REST uses
the Demo trading header and public/private WebSockets use the PAP host family.
The retained `test-bitget-testnet-*` Make names are compatibility aliases to
Demo targets, not a second environment.

All products share logical account ID `BITGET-001` by default. `Config.AccountID`
overrides it. Account state accepts the venue account modes `UNIFIED`, `UTA`, or
`HYBRID`; other modes are outside the phase-one adapter. Credential names, the
write gate, and endpoint-override safety belong to
[Configuration](../reference/configuration.md). Static rows belong to the
[capability matrix](../adapter-capabilities.md).

## Trading and account behavior

Submit, Cancel, symbol/category-scoped CancelAll, and Modify are implemented for
Spot, USDT Perp, and USDC Perp. Only Market and Limit orders are normalized.
Limit supports GTC, IOC, FOK, and GTX/post-only. Modify omits a zero
price/quantity field, treating it as unchanged.

Perp orders use crossed margin. In one-way (`one_way_mode`/`single_hold`) mode,
use `PosNet`; `ReduceOnly` is sent directly. In hedge
(`hedge_mode`/`double_hold`) mode, use `PosLong` or `PosShort`; a reduce-only
long-leg order must sell and a reduce-only short-leg order must buy. Hedge-leg
requests encode the leg instead of the one-way reduce-only flag. For Spot, use
`PosNet` and `ReduceOnly=false`; the adapter does not currently reject every
derivative-only Spot combination locally.

Perp leverage mutation is implemented for the selected settlement category and
uses crossed margin. Spot leverage and margin-mode mutation are unsupported.
Spot has no position-report surface; USDT and USDC Perp positions are account
snapshots.

## Market, reference, and private streams

All implemented products expose REST order books and bars, plus public `books`,
`ticker`, and `trade` subscriptions. Perp additionally exposes current
funding/mark/index snapshots, ticker-based reference streaming, and direct
current-OI queries. Spot has no derivative reference/OI surface. OI is query-only
and is not stored in runtime cache.

`Adapter.Start` subscribes the private `UTA/order`, `UTA/fill`, `UTA/position`,
and `UTA/account` topics. Category plus symbol forms the private instrument
identity; unresolved in-scope records raise a reconciliation gap instead of
silently crossing Spot/Perp scopes.

## Reports, ambiguity, and cleanup

Broad order reports are open-order snapshots. Exact single-order lookup uses the
venue order endpoint when instrument and order/client identity are supplied.
Fill reports scan bounded trade history with a hard 90-day floor and split the
request into venue-sized windows; mass status caps fills at 1,000 records and
marks saturated coverage partial. Derivative terminal-order hydration is also
bounded and retains exact-order fallback on saturation. Perp position reports
are settlement- and instrument-scoped; Spot positions are unsupported.

Parsed business rejections are definitive venue rejections. Transport,
deadline, and non-definitive errors remain ambiguous until exact order/account
evidence resolves them. The lifecycle harness cleans only validation-owned
orders in the selected product, uses authoritative selected-asset balance
evidence for Spot, and requires selected Perp exposure to return flat.

## Non-production verification targets

```sh
make test-bitget-demo-spot
make test-bitget-demo-runtime-spot
make test-bitget-demo-usdt-perp
make test-bitget-demo-runtime-usdt-perp
make test-bitget-demo-usdc-perp
make test-bitget-demo-runtime-usdc-perp
make test-bitget-demo-reference-data-read
make test-bitget-demo-acceptance
make test-bitget-acceptance
```

Use the `*-demo-*` names in new instructions. The product aggregate
`test-bitget-acceptance` still resolves to the same Demo targets. The
reference-data target is read-only; lifecycle targets perform bounded PAP writes
and must run serially. Target existence means implemented, not a current
`Demo/Testnet-certified` pass. Certification requires the dated, named, zero-skip evidence
defined by [Testing and evidence](../reference/testing.md).

See [Operations and recovery](../guides/operations-recovery.md) before a write
run.
