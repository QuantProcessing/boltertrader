# The web app

> Canonical language: English. [Chinese mirror](../zh-CN/guides/web-app.md)

The web app is the operator interface. It is a single-page React application
served as static files, and it talks to exactly one origin.

## Stack

React 19 with Vite 7 and TypeScript. Routing, data, and tables come from
TanStack: `react-router` for the routes, `react-query` for every request and
cache, `react-table` for the blotter and portfolio grids. Charts are
`lightweight-charts`. Styling is Tailwind 4 with shadcn-style components over
Radix primitives.

Nothing in the app is a trading engine. It renders what the trading service
projects and submits what the operator confirms; every rule about what may be
placed lives behind the API.

## One origin

Caddy serves the built assets and reverse-proxies `/api/*` to the auth edge at
`authedge:18091`. There are two rules and no fallthrough between them: an
`/api` path the edge does not route comes back as the edge's own `404`, not as
`index.html` with a `200`. A JSON client that receives HTML reports a parse
error instead of a missing route, which is a much worse bug to chase.

Everything else falls through to `index.html`, because a client-side router owns
those paths — reloading on `/portfolio` must be a page, not a `404`.

The single origin is what lets the session cookie stay `HttpOnly` and
`SameSite=Strict`, and it is why there is no CORS surface to configure. Caddy
keeps the browser's `Host` header so the edge sees the origin the cookie was set
for. See [the auth edge](auth-edge.md).

Realtime needs no special Caddy buffering rule: `/api/v1/realtime/ws` is a
standard WebSocket upgrade and Caddy relays it bidirectionally.

## Routes

| Route | What it is |
| --- | --- |
| `/login` | The only route reachable without a session |
| `/trade` | Ticker strip, chart, order book, order ticket, blotter |
| `/funding` | The cross-venue funding matrix, one column per venue |
| `/funding/arbitrage` | Ranked spreads, the funding-history chart, the hedge ticket, open pairs |
| `/portfolio` | Positions, balances, PnL |
| `/activity` | The command chain with timestamps |
| `/settings` | Accounts, credentials, runtime readiness |

The top navigation carries all six; everything the operator does is one click
from anywhere. Without a session, every route redirects to `/login`.

`/funding` is the one page that renders before any account exists — the board is
public data and needs no credential — so it is readable on a fresh install.
`/funding/arbitrage` is not: it ranks pairs across the accounts the operator
selects, and with none selected it has nothing to rank.

There is one command palette over the whole shell for jumping between
instruments and routes.

## Data flow

Reads go through `react-query` against the endpoints in `src/api/endpoints.ts`.
Live updates arrive on one multiplexed WebSocket — `/api/v1/realtime/ws` — held
open by a singleton topic manager and fanned out to mounted consumers. Market,
funding, arbitrage, connectivity, and account events are logical topics on that
one connection rather than component-owned sockets or polling loops.

Placement is the two-step ticket: **Review order** requests a sealed preview and
starts its expiry countdown, **Confirm** submits that sealed object. The button
says so when a seal expires. The rationale is in
[the trading service](trading-service.md).

Credential values are write-only in the interface. They are submitted once and
never returned — they disappear from the form, from browser storage, and from
every subsequent response.

## Verify it

```sh
make test-browser
```

That installs from the lockfile, type-checks and builds, and runs the Vitest
unit suite. It does not start the application, contact a venue, or exercise a
credentialed browser flow.

A separate live spec exists that places and cancels a real resting order. There
is no non-production venue behind it any more — it reaches a real exchange and
spends real funds. It is not part of `make test-browser`; run it deliberately or
not at all. The normal application has no mock mode: anonymous read-only views
use live public data through the running stack.
