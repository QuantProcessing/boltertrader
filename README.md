# BolterTrader

> English is canonical. [简体中文](README.zh-CN.md)

BolterTrader is a Go-native, self-hosted trading system for one operator and
their own venue accounts. It is one stack, layered: venue SDKs are normalized
into a venue-neutral `exchange` API, the Exchange Gateway turns that into a
resident capability plane with durable order identity, and thin product services
sit on top of the gateway rather than beside it.

## Run it

Docker and Docker Compose are the only prerequisites for the stack itself; Go
1.26 or later (per `go.mod`) is needed to build or test outside containers.

```sh
make up
```

That generates credentials and `.env.stack` on first run, builds and starts
every container, waits for them to become healthy, and then runs the checks
below. The first run prints a generated `operator` password once — it is also
kept in `.secrets/stack/bootstrap_password`. Open the app at
<http://127.0.0.1:18090>.

```sh
make doctor   # what is up, what is not, and which port each service is on
make logs     # last 200 lines from every service
make down     # stop the stack (add -hard to drop the postgres volume too)
```

Nothing here reads the repository's `.env`: that file holds venue API
credentials for the acceptance targets, and the stack's own variables — ports,
uid, secret paths — are generated into `.env.stack` instead. Every published
port binds `127.0.0.1` only.

## Architecture in one minute

```text
web            React 19 + Vite, served by Caddy, same-origin with the auth edge
  │
  ├── services/authedge     session termination and path forwarding
  │      │                  (the only thing the browser talks to)
  │      ├──────────────────────────┐
  │      ▼                          ▼
  │   gateway                  services/trading
  │   accounts, credentials,   manual order orchestration, command ledger,
  │   catalog, capabilities,   portfolio, reservations, activity
  │   orders, market data           │
  │      ▲──────────────────────────┘
  ▼
exchange       venue-neutral clients and models over 9 venues
  │
sdk            13 venues, each a faithful representation of its native API
```

Each layer owns one thing, and the boundary is enforced by
`gateway/contracts/import_boundary_test.go`:

- `sdk/<venue>/` represents the venue API faithfully. An SDK package alone does
  not imply gateway support for that venue.
- `exchange/` normalizes venues into one typed client and model set. Callers
  own lifecycle, caching, and durability; it can also be embedded directly in a
  Go process without the rest of the stack.
- `gateway/` is the shared capability plane above `exchange/`: accounts,
  encrypted credentials, catalog, capability and risk-limit resolution, durable
  order identity, reconciliation, and private event delivery. It does not
  import venue SDK packages.
- `services/trading` and `services/authedge` are products over the gateway's
  `/v1` surface. They hold no venue integration of their own, and each owns a
  separate postgres schema in the same instance.
- `web/` is the single frontend. It calls only the auth edge, on the same
  origin, so the session cookie stays `HttpOnly` and `SameSite=Strict`.

The auth edge is deliberately thin: it terminates the browser session, then
forwards by path prefix to the gateway or to trading, injecting a service token
and a principal header and stripping whatever the browser sent for those. It
does not aggregate responses — the frontend fans out with TanStack Query.

## Verification

```sh
make test                 # Go, credential-free, short mode
make test-compose         # compose config, image builds, hardening, smoke, persistence
cd web && npm run build && npm test && npm run test:e2e
```

Every suite above is offline, credential-free, and deterministic; none of them
reaches a venue. There is no credentialed target left to opt into —
`TestMakefileRecipesNeverEnableCredentialedWrites` reads every Makefile recipe
and fails the one that arms a write. To click through the product without a
venue, run `npm --prefix web run dev:mock`, which answers the whole API surface
from `web/src/mocks/`.

## Current support

The exchange gateway implements 20 product rows across Aster, Binance, Bitget,
Bybit, Gate, Hyperliquid, Lighter, Nado, and OKX. `sdk/` additionally carries
Backpack, EdgeX, GRVT, and StandX, which is SDK-only and not a gateway product.
Support is product-specific, and an implemented row is not production
readiness: a green suite proves the normalized implementation still agrees with
recorded fixtures, nothing wider. The statuses that may be claimed are defined
in the [Glossary](docs/reference/glossary.md).

- [Unified exchange API](docs/exchange/README.md)
- [Exchange Gateway](docs/guides/exchange-gateway.md)
- [Venue guides](docs/venues/README.md)
- [Testing and evidence semantics](docs/reference/testing.md)
- [Documentation index](docs/README.md)

## Safety

There is no non-production environment to fall back on. A credential configured
here reaches the real venue, and the size you pass is the size that trades, so
issue a dedicated key, withhold withdrawal rights, and start small. No test in
this repository places an order — the walkthroughs that did were deleted along
with the Demo/Testnet environments they ran against. Orders come from the
running product, with your credentials. When one goes wrong, inspect exact
validation IDs and authoritative product-scoped state; never blindly cancel or
flatten unrelated exposure.

Credentials are write-only end to end: submitted through the app, encrypted at
rest by the gateway, and never returned by any read path.

## Contributing

Start with the [public documentation contract](docs/contributing/documentation.md).
Development plans, raw validation evidence, credentials, and local execution
artifacts are not public documentation.
