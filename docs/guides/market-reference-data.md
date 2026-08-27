# Market and reference data

> Canonical language: English. [Chinese mirror](../zh-CN/guides/market-reference-data.md)

Two different things travel under the name "market data", and confusing them is
the usual source of a chart that will not load or a price that will not move.

**Reference data** describes what an instrument *is*: its tick size, its lot
size, its minimum notional, whether the venue supports a given order type. It
changes rarely and is loaded as a Catalog snapshot.

**Market data** describes what an instrument is *doing*: top of book, depth,
mark price, funding. It changes constantly and is never stored.

Both terminate at the same boundary. The browser reaches [the gateway](exchange-gateway.md)
through Auth Edge, and only the gateway holds a venue client. Trading consumes
the same Gateway facts for business projections but is not a browser proxy. No
layer above the gateway knows a native symbol.

## Reference data

The gateway loads and refreshes the public Catalog directly from every trusted
route profile. This discovery uses no account and no credentials; configuring
an account later adds trading eligibility, not instruments. The gateway also
exposes account-scoped `/v1/rules`, `/v1/capabilities`, and `/v1/risk-limits`.

The Gateway exposes what the interface needs as `/api/v1/catalog/venues` and
`/api/v1/catalog/instruments` through browser-shaped internal adapters. Instrument
identifiers are the canonical segmented form throughout — venue, environment,
product, an optional settlement asset, then the canonical symbol — never a
venue's own symbol string. See
[Accounts and instruments](../concepts/accounts-instruments.md).

### Instrument identity boundary

There is one input identity and one output formatter:

- A browser or product API submits only `instrumentId`. It does not submit
  `venue`, `product`, `settlement`, or `symbol` beside it. The Gateway Catalog
  resolves those facts, so a request cannot name Binance in one field and an
  OKX contract in another.
- The Gateway's public instrument directory maps that id to exactly one route
  profile and the venue's `nativeId`/`nativeSymbol`. Native identity stops at
  the Gateway boundary and is never reconstructed from a display symbol.
- Responses keep `instrumentId` for joins, commands and audit. The interface
  renders `baseAsset` + `quoteAsset` where structured facts are present, with a
  shared canonical-id formatter as the fallback. Raw ids remain available as
  titles for diagnostics but are not primary labels.
- Accounts add execution reachability only. They never create an instrument or
  decide whether it is listed. Therefore `account_unreachable` and
  `instrument_identity_unresolved` are separate states.

This contract is shared by the instrument picker, market data, candles,
funding history, manual previews, arbitrage legs, positions, orders and fills.
Adding another symbol parser in one of those consumers is a boundary violation.

`/v1/risk-limits` answers honestly rather than approximately: venue risk limits
are not normalized, so it reports that they are unavailable and says why. It is
a placeholder with a reason attached, not a limit set you can trade against.

Rules are enforced where they are known, which is at the gateway and again at
the venue. The order ticket uses them to validate before it will seal a preview,
so an operator sees "below minimum notional" while editing rather than as a
rejection after confirming.

## Market data

`GET /api/v1/market-data` returns one fact per instrument: best bid and ask with
their sizes, up to twenty levels of depth each side, and for perpetuals the mark
price, the current funding rate with its observation and next-funding times, up
to twenty historical funding points, and open interest. The perpetual fields are
absent rather than empty on spot instruments.

Every decimal is a string, from the venue to the browser. Nothing on this path
parses a price into a float.

Three properties are worth knowing because they explain what you will see under
load:

**It is cached with a TTL.** The cache key is venue, environment, product, and
instrument. A fact that arrives older than the TTL, or dated in the future, is
rejected as stale rather than served.

**Concurrent readers coalesce.** Ten browser tabs asking for the same instrument
at the same moment produce one gateway fetch, not ten. The rest wait on it.

**A failed fetch evicts.** There is no serve-stale-on-error path. If the venue
read fails, the entry is removed and the caller gets the error — a price that is
silently minutes old is worse than a visible gap, because someone will trade on
it.

Reads are also budgeted per venue in a fixed window. Exhausting the window
returns a rate-limit error rather than queueing, and the remaining budget is
observable, so a stack that is being hammered says so instead of degrading
quietly.

## Candles

`GET /api/v1/candles` serves the chart. It takes the same canonical
`instrumentId` plus an interval and a bound, and returns at most five hundred
candles — the same bound the gateway applies, so a longer page did not come from
a gateway that agrees about the limit.

Each candle carries open/high/low/close/volume as strings, its open and close
times, and a `complete` flag. The last candle of a live series is incomplete and
will change; drawing it as final is what makes a chart appear to disagree with
the ticker.

The interval is deliberately not part of the market-data cache key. Folding it
in would make one instrument occupy eight cache entries whose freshness rules
disagree with each other.

## Funding rates

Funding arrives on two surfaces, and they are not the same read.

The funding fields on `/api/v1/market-data` — the current rate, its observation
and next-funding times, and the twenty historical points above — belong to one
public instrument snapshot. They use the same credential-free route as its book
and candles and are unrelated to whether an account is configured.

`GET /api/v1/funding-board` and `GET /api/v1/funding-history` need no account at
all. The gateway answers them from venue clients it builds without credentials,
so they resolve before a single account has been declared. The board is the
whole cross-venue matrix — one column per venue, product, and settlement asset —
each column carrying its current rate, its settlement interval, and whether that
venue publishes history. The history is one canonical `instrumentId` over one
window, `7d` or `30d` and nothing else. The Gateway resolves that id to the exact
route profile and native address before calling the venue; an open range would
mean paginating a venue call, and this read exists to be a single call.

Rates from different venues are not comparable as published, because venues do
not settle on the same schedule. Anything that ranks them normalizes to a daily
figure first. That arithmetic, and what it is for, is in
[the trading service](trading-service.md).

Two absences here are load-bearing. A venue with no history surface reports the
series unavailable rather than returning an empty one — "we keep none" and
"there were none" are different answers, and only one of them is a reason to
stop asking. And the per-account record of what funding actually *cost* is a
third surface again, resolving on four product rows only; see
[Unsupported](../reference/unsupported.md).

## Live updates

Neither of the above is polled by the interface. Gateway serves public venue
topics at `/api/v1/market/realtime/ws` and private change notifications at
`/api/v1/account/realtime/ws`; Trading's `/api/v1/realtime/ws` carries only
business projections. Latest-value topics start with a snapshot. Order-book
depth is also WS-only: it starts with a WebSocket-derived snapshot and then
uses native `delta` or self-contained `full` events. A gap emits
`resync_required` before the native stream reopens with a new snapshot. Account
events intentionally have no cursor replay; reconnecting clients reread
authoritative REST projections.

## What is available where

Not every venue exposes every read, and the differences are recorded rather
than discovered at runtime:

- [REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
- [WebSocket operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
- [Venues and account modes](../reference/exchange-venues-and-account-modes.md)

An `N/A` in those matrices is a deliberate deferral, not an accident. See
[Unsupported](../reference/unsupported.md).
