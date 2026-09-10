# Architecture

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/architecture.md)

This page owns BolterTrader's layer and dependency boundaries. Submission
semantics belong in [Execution and Risk](execution-risk.md); state convergence
belongs in [State and Reconciliation](state-reconciliation.md); account and
instrument identity belongs in
[Accounts and Instruments](accounts-instruments.md).

## Layers and dependency direction

| Layer | Owns | May import |
| --- | --- | --- |
| `sdk/<venue>/` | One venue's native API: transport, signing, endpoint profiles, and wire types | The standard library and its own transport dependencies |
| `exchange/` | Venue-neutral typed clients and models over the SDKs, plus construction through `exchange/factory` | `sdk/**` from `exchange/internal/**` only, `credential`, `routeprofile` |
| `gateway/` | The shared capability plane: accounts, encrypted credentials, catalog, capability and limit resolution, durable order identity, reconciliation, and private event delivery | `gateway/**`, `credential`, `exchange`, `exchange/factory`, `routeprofile`, `secretstore`, wire DTOs |
| `api/exchange-gateway/v1` | Versioned wire DTOs and schemas | Standard library |
| `clients/gateway` | Application HTTP/WS client | Wire DTOs, HTTP/WS and decimal libraries |
| `services/` | Product behavior over the gateway's HTTP surface: the command ledger, projections, browser sessions, and forwarding | Its own tree, `clients/gateway`, wire DTOs, `internal/**`, `credential`, `routeprofile`, `secretstore` |
| `web/` | The only frontend | The auth edge's `/api/v1` surface |

Dependencies run strictly downward. A layer never imports a layer above it, and
no layer imports a sibling's internals.

Two layers are entry points in their own right rather than mere implementation
detail of the one above: [the unified exchange API](../exchange/README.md)
embeds in any Go process, and [the Exchange Gateway](../guides/exchange-gateway.md)
serves any product, not only the one in this repository.

## Enforced boundaries

Boundary tests hold the table above to the code. They are the reason a boundary
violation fails `make test` rather than being noticed in review.

- `gateway/contracts/import_boundary_test.go` walks every non-test `.go` file
  under `gateway/` and rejects any repository import that is not `gateway/...`
  or the shared wire package and these packages: `credential`, `exchange`,
  `exchange/factory`, `routeprofile`, `secretstore`. The gateway therefore
  cannot reach a venue SDK even accidentally.
- `clients/gateway/boundary_test.go` prevents both the client and wire package
  from importing services, SDKs or Gateway runtime implementations.
- `services/monitor/run_test.go` restricts the independent monitor and command
  to the Gateway client, wire types and its own application package.
- `exchange/boundary_test.go` rejects any `/sdk/`, `/adapter/`, or `/runtime/`
  import in the public `exchange` and `exchange/factory` files, and rejects
  public escape symbols named `rawParams`, `nativeParams`, `sdkClient`, or
  `nativeClient`. SDK wiring lives under `exchange/internal/**`, so callers
  cannot reach a native handle through the normalized API.
- `exchange/docs_manifest_test.go` and `exchange/docs_system_contract_test.go`
  hold the operation matrices and the venue-difference table to
  `exchange/testdata/public_surface_manifest.json`, so a capability claim in
  the documentation cannot drift from the code that backs it.

The services layer uses HTTP; the monitor also has an explicit import gate. No
production file under `services/` imports `gateway/**` or `exchange/**` at all.
A product service reaches the gateway over HTTP, which is what makes the
gateway replaceable, independently deployable, and usable by a product that
does not live in this repository.

## Process and network topology

`make up` starts eight containers: PostgreSQL, three migrators that run to
completion and exit, and four resident services. Every published port binds
`127.0.0.1` only.

```text
browser ──► web (Caddy)
              │  same origin: /api/* is reverse-proxied
              ▼
         services/authedge ──┬──► gateway            /internal/v1/accounts…, catalog, market reads
                             └──► services/trading   manual commands and projections
                                        │
                                        └──► gateway /v1
                                                 │
                                            exchange ──► sdk ──► venue
```

The browser talks to exactly one origin. Because the bundle and `/api/*` share
it, the session cookie stays `HttpOnly` and `SameSite=Strict` and no CORS
surface exists. The auth edge terminates that session and forwards by path
prefix, injecting a service token and the caller's identity header; it performs
no response aggregation. The gateway's account lifecycle is reachable only on
its internal plane, because it accepts credential material.

Postgres runs once. The gateway, the trading service, and the auth edge each
own separate schemas within it and never read each other's tables.

## Where venue differences belong

Differences that cannot be expressed as portable policy stay as low as possible:

- `sdk/<venue>` owns the wire representation, signing, and endpoint profiles.
- `exchange/internal/<venue>` owns native symbols and asset indexes, supported
  order fields, account and position mode discovery, stream-gap interpretation,
  report coverage, and error mapping into normalized errors.
- `exchange` exposes only what every implemented venue can express. A dimension
  that one venue cannot honour becomes `exchange.ErrUnsupported`, not a
  venue-specific branch in a caller.

Above `exchange`, a venue name is data — part of an instrument identifier or a
capability record. It must never select a code path. The
[venue and account-mode differences](../reference/exchange-venues-and-account-modes.md)
page is the centralized statement of what actually differs.

## Where shared capability belongs

The gateway is deliberately thick and product services are deliberately thin.
Anything more than one product would need lives in the gateway: credential
storage, account lifecycle, catalog, capability and limit resolution, durable
order identity, reconciliation, private event delivery, and normalized public
market facts. Funding, mark, index, and OI are collected once into Gateway
memory and streamed internally; they are not fetched again for each product or
browser request. Full order books remain per-instrument streams rather than
entering that cross-market projection.

One process-wide HTTP transport budgets actual venue requests, including catalog,
funding, account and connectivity traffic. Public-read coalescing does not count
as a separate quota. SDKs and the transport share only the small
`internal/venuehttp` protocol package; normalized exchange errors retain HTTP
status and retry delay without exposing native error bodies. A transport failure
after possible order submission remains uncertain and requires reconciliation.
See [HTTP errors and request budgets](../design/sdk-errors-rate-limits-2026-09-10.md)
for local ceilings and their limits.

Public market consumers use `MarketWebSocket` and `PerpMarketWebSocket`;
public funding reads use `PerpMarketREST`. The complete account-client interfaces
compose these facets with private operations. `PublicMarketConnector.WebSocket`
returns a restricted concrete view, so a type assertion cannot recover order
submission, cancellation or private account subscriptions. Optional derivative
reference streams and subscription replay remain available. Gateway's public
market source depends only on these public facets. See
[Public market capability boundaries](../design/public-market-boundary-2026-09-10.md)
for the Go API change. Independent applications now use `/v1/market` for the
public catalog, candles, shared snapshots and four market stream topics, using
the same Source, Hub and hotset as internal consumers. See the
[application market API](../design/market-api-2026-09-10.md) for coverage, identity,
freshness and recovery semantics. These routes retain Gateway bearer auth and
do not require an exchange account. The listener authenticates requests before
checking write readiness. This personal deployment has no token permission tiers.
See [simple accounts](../design/simple-accounts-2026-09-10.md).

Trading owns manual order intent, command recovery and accounting.
It consumes Gateway account and catalog facts without creating venue clients or
a second market cache.
The market-capability matrix is production configuration, not documentation:
it selects the transport, native market-family boundary, cadence, and rate-limit
operation for each dataset. Exactly one feed owner exists for each
`venue + environment + native market family`. Route profiles are projections
of that owner, and hot order-book/BBO/candle topics share one pooled native
socket per owner. Feed owners write field-level `DerivativePatch` values into a
single in-memory fact store; the funding board, Trader mark and position PnL all read those same facts.
When a product service grows a capability that a second product would also
need, that is a signal to move it down into the gateway, not to duplicate it.

## Boundary checks for contributors

Before adding an abstraction, ask which layer can express it without leaking
the layer below:

- If it needs an SDK type, it belongs in `exchange/internal/<venue>`.
- If it needs a venue branch above `exchange`, the normalization is incomplete.
- If a second product would need it, it belongs in `gateway/`, not in a service.
- If it needs a Gateway runtime Go type inside `services/`, the gateway's HTTP surface
  is missing something; extend that surface and the independent client. Shared
  wire DTOs under `api/exchange-gateway/v1` are part of that surface.

Continue with [Accounts and Instruments](accounts-instruments.md), the
[venue contributor guide](../contributing/venues.md), and the service guides for
[the gateway](../guides/exchange-gateway.md),
[the trading service](../guides/trading-service.md),
[the auth edge](../guides/auth-edge.md), and [the web app](../guides/web-app.md).
