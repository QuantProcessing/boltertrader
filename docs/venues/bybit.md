# Bybit

[Chinese mirror](../zh-CN/venues/bybit.md) · This English page is canonical.

## Scope and status

The current adapter implements Spot plus USDT- and USDC-settled linear
Perp/SWAP. Dated linear futures are deliberately filtered and deferred. Public
verification uses **Bybit Demo Trading on a mainnet account**, not Bybit Testnet
and not Testnet demo credentials.

All products share one configured unified account identity, default
`BYBIT-001`. The account-state path rejects non-unified account modes. Demo
preflight also requires a writable unified-account API key, SpotTrade permission
for Spot, and ContractTrade Position permission for account reconciliation;
Perp additionally requires ContractTrade Order permission. See
[Configuration](../reference/configuration.md) for credential names and the
write gate, and the [capability matrix](../adapter-capabilities.md) for static
rows.

## Trading and account behavior

Submit, Cancel, symbol/category-scoped CancelAll, and Modify are implemented for
all three product rows. Only Market and Limit orders are normalized. Limit
supports GTC, IOC, FOK, and GTX, which maps to Bybit `PostOnly`. Modify sends only
nonzero changed price/quantity fields; a zero field is omitted.

`PosNet`, `PosLong`, and `PosShort` map to Bybit position indexes 0, 1, and 2,
and `ReduceOnly` is passed through. These fields must match the venue account's
one-way or hedge mode. For Spot, use `PosNet` and `ReduceOnly=false`; the adapter
does not currently add a Spot-specific local rejection for derivative-only
fields, so their presence is not an implemented Spot semantic.

The account client always reads the `UNIFIED` wallet and exposes a margin-shaped
account state, including when the configured product scope is Spot-only. Linear
USDT and USDC positions are account snapshots; Spot has no normalized position
report. Perp leverage mutation sets buy and sell leverage together. Spot
leverage and per-symbol margin-mode mutation are unsupported.

## Market, reference, and private streams

All implemented products provide REST order books and bars, with public
`orderbook.50`, `tickers`, and `publicTrade` subscriptions normalized as book,
quote, and trade events. Linear Perp adds current funding/mark/index snapshots,
ticker-based reference streaming, and a direct current-OI query. Spot has no
derivative reference or OI surface, and OI is not runtime-cached.

`Adapter.Start` subscribes private `order`, `execution`, `position`, and `wallet`
topics. Material execution records that cannot be normalized emit a gap signal so
runtime reconciliation remains authoritative. Reconnect gaps are also surfaced.

## Reports, ambiguity, and cleanup

Broad order reporting uses Bybit realtime/open records. Exact client/order
identity first checks realtime records and then filtered order history. Fill
reports use execution history, exclude funding rows, and are bounded by the
query; mass status caps recovered fills at 1,000 records. For derivative fills,
terminal-order hydration scans a seven-day window with a 1,000-record bound and
leaves exact-order fallback required when saturated. Perp position reports query
USDT/USDC settlement scopes; Spot positions are unsupported.

Parsed command/business rejections are definitive venue rejections. Transport,
timeout, and other non-definitive failures remain ambiguous. Do not retry an
ambiguous write until exact order and account evidence resolves it.

The acceptance lifecycle requires no selected-scope open order before writing.
It tracks exact validation identities, proves selected-symbol open-order cleanup,
uses authoritative base-balance evidence for Spot, and requires the selected
Perp exposure to return flat. This is not whole-account cleanup.

## Non-production verification targets

```sh
make test-bybit-demo-spot
make test-bybit-demo-runtime-spot
make test-bybit-demo-usdt-perp
make test-bybit-demo-runtime-usdt-perp
make test-bybit-demo-usdc-perp
make test-bybit-demo-runtime-usdc-perp
make test-bybit-demo-reference-data-read
make test-bybit-demo-acceptance
make test-bybit-acceptance
```

The final two aggregates currently cover the same three product pairs; the
non-`demo` name is a product aggregate, not a second environment. Reference data
is read-only; lifecycle targets write bounded Demo state and must run serially.
Implemented targets are not automatically `Demo/Testnet-certified`. Use the dated,
zero-skip evidence rules in [Testing and evidence](../reference/testing.md).

See [Operations and recovery](../guides/operations-recovery.md) before any write
run.
