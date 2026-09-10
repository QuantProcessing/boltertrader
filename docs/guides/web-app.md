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

Caddy relays the single `/api/v1/realtime/ws` WebSocket upgrade bidirectionally.

## Routes

| Route | What it is |
| --- | --- |
| `/login` | Sign in; public trade/funding views also allow anonymous use |
| `/trade` | Ticker strip, chart, order book, order ticket, blotter |
| `/funding` | The cross-venue funding matrix, one column per venue |
| `/portfolio` | Positions, balances, PnL |
| `/activity` | The command chain with timestamps |
| `/settings` | Accounts, credentials, runtime readiness |

The sidebar carries these five pages. `/trade` and `/funding` display
public market data without an account; private account reads and writes require
a session.

The instrument picker changes the market for the chart, book and ticket together.

## Data flow

HTTP bootstraps snapshots and submits explicit commands. All continuous browser
updates share **one** `/api/v1/realtime/ws` connection at Auth Edge. Public topics
cover depth, candles, mark/index prices, funding and venue connectivity;
`account.events` carries private balances and positions; `trading.orders` carries
persisted orders, canonical fills and related ledger/activity projections.
Duplicate consumers share a topic, cancellation releases it, and heartbeat plus
reconnection resubscribes the current set. Vite's development HMR socket is separate.

Depth starts with a native WS snapshot and applies deltas/full replacements.
Gateway never joins a REST order book into that stream. Account entities merge
by identity, timestamp and version; persisted Trading command identities own
order status. A late REST result cannot roll a terminal order back to submitted.
Disconnect retains the last index estimate with a delay marker. Account snapshots
refresh on reconnect, with a 60-second REST repair interval as fallback.

Linear unrealized P&L uses the venue's index price and exact decimal arithmetic.
Mark price remains distinct, including the compact chart line. Missing index data
never substitutes mark price; inverse/unsupported formulas keep the venue's P&L.
Totals stay in their original currencies. The sidebar, light/dark/system themes,
compact metrics and responsive panes follow the workspace design in `design.md`.

The ticket reviews existing account/catalog/market facts locally. Confirm sends
one command request. Its response updates the order immediately; subsequent
persisted changes arrive over the socket. Gateway order/fill events wake Trading's
read-only reconciliation, without resending orders or shortening ambiguous
handoff windows. Credential values are write-only and disappear after submission.

## Verify it

```sh
make test-browser
```

That installs from the lockfile, type-checks and builds, and runs the Vitest
unit suite. It does not start the application, contact a venue, or exercise a
credentialed browser flow.

For an uncredentialed interaction preview, run `npm --prefix web run dev:mock`.
Accounts and fills are local simulations; public market values come from venue
endpoints and require outbound network. Nothing in that mock reads credentials,
signs venue requests or places real orders. See [Testing](../reference/testing.md).
