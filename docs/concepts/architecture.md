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
| `gateway/` | The shared capability plane: accounts, encrypted credentials, catalog, capability and limit resolution, durable order identity, reconciliation, and private event delivery | `gateway/**`, `credential`, `exchange`, `exchange/factory`, `routeprofile`, `secretstore` |
| `services/` | Product behavior over the gateway's HTTP surface: the command ledger, projections, browser sessions, and forwarding | Its own tree, `internal/**`, `credential`, `routeprofile`, `secretstore` |
| `web/` | The only frontend | The auth edge's `/api/v1` surface |

Dependencies run strictly downward. A layer never imports a layer above it, and
no layer imports a sibling's internals.

Two layers are entry points in their own right rather than mere implementation
detail of the one above: [the unified exchange API](../exchange/README.md)
embeds in any Go process, and [the Exchange Gateway](../guides/exchange-gateway.md)
serves any product, not only the one in this repository.

## Enforced boundaries

Three tests hold the table above to the code. They are the reason a boundary
violation fails `make test` rather than being noticed in review.

- `gateway/contracts/import_boundary_test.go` walks every non-test `.go` file
  under `gateway/` and rejects any repository import that is not `gateway/...`
  or one of exactly five packages: `credential`, `exchange`,
  `exchange/factory`, `routeprofile`, `secretstore`. The gateway therefore
  cannot reach a venue SDK even accidentally.
- `exchange/boundary_test.go` rejects any `/sdk/`, `/adapter/`, or `/runtime/`
  import in the public `exchange` and `exchange/factory` files, and rejects
  public escape symbols named `rawParams`, `nativeParams`, `sdkClient`, or
  `nativeClient`. SDK wiring lives under `exchange/internal/**`, so callers
  cannot reach a native handle through the normalized API.
- `exchange/docs_manifest_test.go` and `exchange/docs_system_contract_test.go`
  hold the operation matrices and the venue-difference table to
  `exchange/testdata/public_surface_manifest.json`, so a capability claim in
  the documentation cannot drift from the code that backs it.

The services layer is bounded by construction rather than by a test: no
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
         services/authedge ──┬──► gateway            /internal/v1/accounts…
                             └──► services/trading   everything else
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
order identity, reconciliation, and private event delivery.

A product service owns only its own product's decisions — for the manual
trading product, the command ledger, previews, reservations, and the
projections its interface needs. When a product service grows a capability that
a second product would also need, that is a signal to move it down into the
gateway, not to duplicate it.

## Boundary checks for contributors

Before adding an abstraction, ask which layer can express it without leaking
the layer below:

- If it needs an SDK type, it belongs in `exchange/internal/<venue>`.
- If it needs a venue branch above `exchange`, the normalization is incomplete.
- If a second product would need it, it belongs in `gateway/`, not in a service.
- If it needs a gateway Go type inside `services/`, the gateway's HTTP surface
  is missing something; extend that surface instead of the import graph.

Continue with [Accounts and Instruments](accounts-instruments.md), the
[venue contributor guide](../contributing/venues.md), and the service guides for
[the gateway](../guides/exchange-gateway.md),
[the trading service](../guides/trading-service.md),
[the auth edge](../guides/auth-edge.md), and [the web app](../guides/web-app.md).
