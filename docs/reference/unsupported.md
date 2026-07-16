# Unsupported, Deferred, and SDK-Only Surfaces

[简体中文](../zh-CN/reference/unsupported.md)

> English is canonical. **Owner:** this page owns cross-venue unsupported,
> deferred, and SDK-only inventory. The
> [capability matrix](../adapter-capabilities.md) owns the detailed static runtime
> rows; venue pages own product-specific order, configuration, and stream rules.

Use the exact status meanings in the [glossary](glossary.md). A limitation here
describes BolterTrader's normalized surface, not every feature available from the
venue itself.

## Deferred and absent product slices

- Bybit dated linear futures are deferred. The current derivative registry has
  USDT-linear and USDC-linear Perp/SWAP rows, not a dated-Future row.
- Gate USDC-linear Perp/Futures is deferred. The current Gate derivative slice is
  USDT-linear.
- The current static matrix contains 21 Spot and Perp product rows. Do not infer
  a runtime Future or Option product from a core enum, an SDK model, or a venue's
  external product catalog.

## Cross-venue command boundaries

- Strategy `Context` grants ordinary `Submit` and client-ID `Cancel`. It does not
  grant `Modify`, `CancelAll`, execution reports, account mutations, or direct
  adapter/SDK access.
- Static `Modify` is absent for both Aster rows, both Nado rows, Binance Spot, and
  both Gate rows. Other rows advertising it still remain subject to their venue
  page's order and product restrictions.
- `CancelAll`, `SetLeverage`, and `SetMarginMode` are adapter-level methods, not
  portable strategy commands. Their availability is product-specific, and an
  inapplicable implementation returns `contract.ErrNotSupported`.
- Lighter currently exposes leverage and margin-mode code paths, but their
  adapter/SDK parameter contract is not documented as usable, Spot scope is not
  rejected locally, and Testnet acceptance does not cover them. Do not rely on
  those mutations until the contract and product guard are corrected and
  verified.
- Runtime has no generic prepared-order, lease, venue-capacity, or
  admission-reservation protocol. The portable path uses side-effect-free
  `ValidateSubmit`, optional configured venue-neutral risk/reservation, durable
  intent, and one ordinary adapter `Submit`.

See [Strategies](../guides/strategies.md) and
[Execution and risk](../concepts/execution-risk.md) for the portable API and
submission semantics.

## Cross-venue data and report boundaries

- Every current Spot row has unsupported normalized position reports. Spot
  balance or inventory state does not become a derivative position report.
- Aster, Nado, and Gate expose venue-specific bounded/current fill sources; Bybit
  exposes bounded execution history; Bitget exposes bounded 90-day trade
  history. Binance, OKX, and Lighter have no concrete normalized fill-report
  method. Hyperliquid's static rows declare fill reports unsupported, while its
  Spot, Perp, and HIP-3 clients fetch `UserFills` and apply bounded local filters;
  mass status still does not advertise an authoritative fill ledger.
- Every current order-report row carries the open-only caveat: absence from a
  complete open-order result cannot establish the terminal reason or reconstruct
  missing fills.
- No current static row promises adapter receive/emit latency timestamps. Runtime
  bus, application, callback, and command timing remains separately available.
- Hyperliquid Spot has no normalized public book, quote, or trade subscription.
  Authenticated private order/fill and spot-state plumbing is conditional on a
  successful adapter startup; allocated channels or configuration alone do not
  establish readiness.
- Lighter implements REST book/bar queries and Perp derivative-reference
  streaming. Its normalized book, quote, and trade subscriptions are
  unsupported.
- Current open interest is an optional direct `OpenInterestClient` query where
  implemented. It is not a runtime-cache or subscription surface.

Bars, concrete stream kinds, report bounds, account mutations, order types, and
time-in-force rules are product-specific. Consult the
[venue index](../venues/README.md), the capability matrix, and
[Market and reference data](../guides/market-reference-data.md) rather than
inferring them from a coarse capability category.

## SDK-only venue families

The four rows below exhaust the repository's top-level SDK families that do not
have a static runtime capability row. They are low-level integration surfaces,
not strategy/runtime availability and not Demo/Testnet or production
certification.

| SDK family | Product shape represented in the SDK | Low-level market surface | Low-level order/account surface | Low-level stream surface |
| --- | --- | --- | --- | --- |
| Backpack | Spot plus perpetual/futures-oriented models | Markets, ticker, depth/order book, trades, funding rates, mark prices, and klines | Account settings, balances, open orders, positions, fill history, single/batch placement (plus an `ExecuteOrder` compatibility wrapper), and single/all cancellation | Generic raw public/private subscriber; depth, order, fill, and position payload models |
| EdgeX | Linear Perp | Exchange metadata, ticker, depth, klines, long/short ratio, and current/paged/historical funding | Signed place/cancel/cancel-all, order queries, account, assets/collateral, position transactions, and leverage | Public metadata, ticker, kline, book, trade, and funding; private order, fill, position, and balance |
| GRVT | Perp-oriented fixtures on a generic multi-product-shaped model | Instruments, book, mini/full ticker, trades, klines, and historical funding | Signed create/cancel/cancel-all, open orders, leverage, account summary, and funding-account summary | Public mini/full ticker, book, trades, and klines; private order, fill, position, and fund movements; authenticated WebSocket create/cancel/cancel-all |
| StandX | Perp | Symbol information/statistics, overview, depth, price, recent trades, and funding history | Create, single/multiple cancel, leverage, margin mode, positions, balances, open orders, and user trades | Public price, depth, and trades; authenticated orders, positions, balance, trades, and WebSocket create/cancel commands |

SDK presence establishes neither normalized adapter completeness nor external
verification. Promotion requires the [adapter contribution contract](../contributing/adapters.md),
a concrete static runtime row, matching tests, and product-scoped evidence.

## Related references

- [Capability matrix](../adapter-capabilities.md)
- [Testing and evidence](testing.md)
- [Configuration](configuration.md)
- [Architecture](../concepts/architecture.md)
