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

Realtime needs no special Caddy buffering rule. The market, account, and
business paths are standard WebSocket upgrades and Caddy relays them
bidirectionally.

## Routes

| Route | What it is |
| --- | --- |
| `/login` | The only route reachable without a session |
| `/trade` | Ticker strip, chart, order book, order ticket, blotter |
| `/funding` | The cross-venue funding matrix, one column per venue |
| `/portfolio` | Positions, balances, PnL |
| `/activity` | The command chain with timestamps |
| `/settings` | Accounts, credentials, runtime readiness |

The top navigation carries these five pages. `/trade` and `/funding` display
public market data without an account; private account reads and writes require
a session.

There is one command palette over the whole shell for jumping between
instruments and routes.

## Data flow

Reads go through `react-query` against the endpoints in `src/api/endpoints.ts`.
Live updates use one shared socket per ownership boundary rather than one socket
per component: Gateway market data at `/api/v1/market/realtime/ws`, Gateway
account notifications at `/api/v1/account/realtime/ws`. Duplicate component subscriptions still
share one logical topic. Depth starts from a WebSocket-derived snapshot and is
rebuilt from native deltas or self-contained full replacements; Gateway never
stores a full book or joins a venue REST book to the stream.
Account entities are overlaid by entity ID, source timestamp, and resource
version, so an older in-flight REST response cannot roll a newer WebSocket
update backward. REST reconciliation runs on a fixed 60-second cadence and is
never triggered by each event or reconnect. Each ownership socket also uses an
application ping/pong watchdog. The shell reports `degraded` when Gateway is
still reachable but a venue market or account stream is recovering; it reports
reconnecting only when the browser socket itself is unavailable. Account
degradation is tracked per account and clears only on that account's health
recovery event.

The ticket computes its review from the account, catalog and public prices
already on screen. **Confirm** sends one command request; there is no preview
request, server-side preview state or expiry countdown. Trading still performs
the authoritative account, route and rule checks in that command request. See
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
