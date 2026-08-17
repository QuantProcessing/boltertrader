# Unsupported surfaces

> Canonical language: English. [Chinese mirror](../zh-CN/reference/unsupported.md)

This page is the inventory of what BolterTrader does not do. It uses the status
words from the [Glossary](glossary.md) exactly: **unsupported** means the
normalized surface is absent, **deferred** means it is deliberately outside the
current slice, and **SDK-only** means low-level venue code exists with no product
row above it. None of the three says a venue lacks the feature.

The two operation matrices are the authority for per-row detail. This page states
the boundaries that hold across rows, and does not restate the cells.

## Removed on purpose

Three capabilities existed in an earlier design and were deleted, not deferred.
They are named here because the words still circulate.

| Removed | What replaced it |
| --- | --- |
| The strategy runtime — a long-running process that owned a bus, a cache, and strategy orchestration | Product services over the gateway's HTTP surface. The trading service hosts two of them |
| Backtesting | Nothing. There is no historical simulation surface anywhere in this tree |
| The funding product — a service that traded funding-rate spreads | The rewrite landed. A credential-free funding board reads the rates, and the arbitrage product in [the trading service](../guides/trading-service.md) trades the spread as a two-leg cross-venue hedge |

If you find a page, comment, or issue that mentions a strategy `Context`, a
`runtime/` package, or a per-venue `adapter/` package, it predates the current
[architecture](../concepts/architecture.md).

## Absent on every product row

The gateway's capability matrix carries these five flags as false on all twenty
rows. They are gateway-level absences, not venue limitations:

| Capability | Status |
| --- | --- |
| Amend / modify an order | Unsupported. Cancel and replace instead. No amend operation exists in the REST matrix either |
| Set leverage | Unsupported through the gateway. The exchange layer has `SetLeverage` for perps, but no product row exposes it |
| Set margin mode | Unsupported |
| Set position mode | Unsupported |
| Transfer funds | Unsupported. Nothing in this tree moves money between accounts or wallets |

`SetLeverage` deserves a note: on Nado the exchange layer returns success with an
effective leverage of zero, because the venue has no leverage setter — the
backend risk engine decides. A success there means "accepted and ignored", which
is exactly the kind of ambiguity a gateway row should not advertise.

## Deferred product slices

Twenty product rows exist, all of them spot or perp.

- **No dated futures.** Bybit's dated linear futures and every other venue's
  dated contracts are outside the slice.
- **No options.** No venue, no row.
- **Gate USDC-linear perp** is deferred; Gate's perp row is USDT-linear.
- **Aster perp (`ATP`) has no funding surface.** It is the only perp row without
  one.
- **Nado perp (`NDP`) has no funding-rate history.** It quotes a current rate and
  no past ones, so it is the one funding-board column whose history series is
  reported unavailable rather than empty. Every other perp column resolves one.
- **Account funding payments** — the per-account history of what funding
  actually cost — resolve on four rows only: `BNP`, `LIP`, `BYU`, `GTU`. The
  other perp rows expose the funding rate without the payment history. This is a
  different surface from the credential-free rate history above: one is what an
  account was charged, the other is what the venue published.

Do not infer a product from an enum, an SDK model, or a venue's public catalog.
A row exists when it is in the matrix.

## Reads that cannot prove absence

Two limits apply to every row, and both matter when an order's fate is
uncertain.

**Exact lookup is not universal.** `LookupExactOrder` — fetch one order by its
identifier and get an authoritative answer — is admitted on eleven rows: `BNS`,
`BNP`, `OXS`, `OXP`, `LIS`, `LIP`, `HLS`, `HLP`, `BYS`, `BYU`, `BYC`. On the
other nine (`BGS`, `BGU`, `BGC`, `GTS`, `GTU`, `ATS`, `ATP`, `NDS`, `NDP`) it is
absent, and the only reconstruction available is a scan of open and recent
orders.

Even where it exists, the strategy differs. Most rows read back by client or
venue order ID. Lighter's two rows scan the active list plus the first inactive
page, and cannot confirm absence — an order that has aged out of that page is
unproven, not gone.

**No row proves absence by pagination.** Walking history to its end does not
establish that an order never existed. This is the open-only caveat: a complete
open-order read proves an order is no longer open, and nothing more. It cannot
supply the terminal reason and it cannot reconstruct fills. When a submission is
ambiguous, [Operations and recovery](../guides/operations-recovery.md) is the
procedure — not a retry.

**Cancel is located by order ID.** Cancelling by client order ID alone is not
part of the twenty-row guarantee, and Nado additionally requires a lowercase
`0x`-prefixed 32-byte order digest rather than a numeric identifier.

## Rows that are not write-ready

Nineteen of the twenty rows accept writes by default. **Lighter spot (`LIS`)**
does not: it is registered, readable, and excluded from default write
promotion, pending live evidence of two-sided liquidity and an exact lookup.
That evidence was once collectable on demo or testnet; with those environments
gone it can only be gathered against the live book, at real cost. Treat it as a
read row until that evidence exists.

## Streams

The [WebSocket matrix](exchange-ws-v1-operation-matrix.md) is narrow in one
predictable way and one unpredictable way.

Predictably: the ten spot rows have no position, mark-price, or funding-rate
subscription, because spot has none of those things. Spot balance state is not a
position report, and no reconciliation turns it into one.

Unpredictably: **Nado perp (`NDP`) admits `WatchMarkPrice` at compile time but
returns an unsupported error at runtime**, with a nil subscription. It is the one
place in the matrices where an `A` cell does not mean a working stream, and it is
called out here because a compile-time check will not catch it.

Candle intervals are fixed across rows: `1m`, `5m`, `15m`, `30m`, `1h`, `4h`,
`12h`, `1d`. Balance subscriptions are account-wide rather than per instrument.
See [Market and reference data](../guides/market-reference-data.md).

## SDK-only venue families

Four venue families have SDK code and no product row. They are low-level
integration surfaces: no normalized coverage, no gateway row, no evidence.

| SDK family | Product shape in the SDK | Market surface | Order and account surface | Stream surface |
| --- | --- | --- | --- | --- |
| Backpack | Spot plus perpetual/futures-oriented models | Markets, ticker, depth, trades, funding rates, mark prices, klines | Account settings, balances, open orders, positions, fill history, single and batch placement, single and bulk cancellation | Raw public and private subscriber; depth, order, fill, position payloads |
| EdgeX | Linear perp | Exchange metadata, ticker, depth, klines, long/short ratio, current and historical funding | Signed place, cancel, cancel-all, order queries, account, collateral, position transactions, leverage | Public metadata, ticker, kline, book, trade, funding; private order, fill, position, balance |
| GRVT | Perp-oriented, on a multi-product-shaped model | Instruments, book, ticker, trades, klines, historical funding | Signed create, cancel, cancel-all, open orders, leverage, account and funding-account summaries | Public ticker, book, trades, klines; private order, fill, position, fund movements; authenticated WebSocket create and cancel |
| StandX | Perp | Symbol information and statistics, overview, depth, price, recent trades, funding history | Create, single and bulk cancel, leverage, margin mode, positions, balances, open orders, user trades | Public price, depth, trades; authenticated orders, positions, balance, trades, WebSocket create and cancel |

These four are why "SDK-only" is a status word. Thirteen SDK families exist;
nine reach the exchange layer's factory and become gateway rows. The gap is the
point of the layering, not an oversight.

Promoting one is a defined path: [Adding a venue](../contributing/venues.md)
walks it from the SDK through `exchange/factory`, a gateway product row, both
matrices, and a Make target.

## Related

- [Glossary](glossary.md) — what each status word permits
- [REST operation matrix](exchange-rest-v1-operation-matrix.md)
- [WebSocket operation matrix](exchange-ws-v1-operation-matrix.md)
- [Venues and account modes](exchange-venues-and-account-modes.md)
- [Testing](testing.md) — what a green run proves
