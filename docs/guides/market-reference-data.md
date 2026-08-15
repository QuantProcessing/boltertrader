# Market and reference data

> Canonical language: English. [Chinese mirror](../zh-CN/guides/market-reference-data.md)

Two different things travel under the name "market data", and confusing them is
the usual source of a chart that will not load or a price that will not move.

**Reference data** describes what an instrument *is*: its tick size, its lot
size, its minimum notional, whether the venue supports a given order type. It
changes rarely and is loaded as a Catalog snapshot.

**Market data** describes what an instrument is *doing*: top of book, depth,
mark price, funding. It changes constantly and is never stored.

Both cross the same boundary. The browser asks the trading service, the trading
service asks [the gateway](exchange-gateway.md), and only the gateway holds a
venue client. No layer above the gateway knows a native symbol.

## Reference data

The gateway loads the Catalog per account at startup and refreshes it. It
exposes `/v1/instruments`, `/v1/rules`, `/v1/capabilities`, and
`/v1/risk-limits`.

The trading service republishes what the interface needs as
`/api/v1/catalog/venues` and `/api/v1/catalog/instruments`. Instrument
identifiers are the canonical segmented form throughout — venue, environment,
product, an optional settlement asset, then the canonical symbol — never a
venue's own symbol string. See
[Accounts and instruments](../concepts/accounts-instruments.md).

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

`GET /api/v1/candles` serves the chart. It takes the same instrument
coordinates plus an interval and a bound, and returns at most five hundred
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
and next-funding times, the twenty historical points above — belong to one
instrument on one account. They come with the rest of that instrument's
snapshot, through that account's own venue client, and they spend that account's
rate-limit budget.

`GET /api/v1/funding-board` and `GET /api/v1/funding-history` need no account at
all. The gateway answers them from venue clients it builds without credentials,
so they resolve before a single account has been declared. The board is the
whole cross-venue matrix — one column per venue, product, and settlement asset —
each column carrying its current rate, its settlement interval, and whether that
venue publishes history. The history is one symbol on one venue over one window,
`7d` or `30d` and nothing else; an open range would mean paginating a venue
call, and this read exists to be a single call.

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

Neither of the above is polled by the interface. One server-sent stream,
`/api/v1/realtime/stream`, carries updates for whatever is subscribed. The
gateway's own `/v1/public/ws` is what feeds it underneath, and that stream has
no cursor replay by design: every connection starts from a fresh authoritative
snapshot, and a venue gap emits `resync_required` and closes rather than
silently skipping.

## What is available where

Not every venue exposes every read, and the differences are recorded rather
than discovered at runtime:

- [REST operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
- [WebSocket operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
- [Venues and account modes](../reference/exchange-venues-and-account-modes.md)

An `N/A` in those matrices is a deliberate deferral, not an accident. See
[Unsupported](../reference/unsupported.md).
