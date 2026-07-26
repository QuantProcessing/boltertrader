# BolterTrader

> English is canonical. [简体中文](README.zh-CN.md)

BolterTrader is a Go-native trading system with three peer product paths over
the same venue integrations: a direct normalized `exchange` API, a self-hosted
Exchange Gateway, and a single-strategy `runtime`. Choose by deployment and
state-ownership needs; none of these paths deprecates or wraps the others.

## Choose a starting path

- New to the repository: read the [prerequisites](docs/getting-started/prerequisites.md).
- No credentials: run the [offline runtime walkthrough](docs/getting-started/offline-runtime.md).
- CEX non-production validation: use [Binance Spot Demo](docs/getting-started/cex-demo.md).
- DEX non-production validation: use [Hyperliquid Perp Testnet](docs/getting-started/dex-testnet.md).
- Embedding venue-neutral clients in a Go process: use the standalone
  [Unified exchange API](docs/exchange/README.md).
- Centralizing accounts, Catalog, durable order identity, restart recovery, and
  private events in a resident service: use the
  [Exchange Gateway](docs/guides/exchange-gateway.md).
- Hosting one stateful strategy: use the
  [runtime node guide](docs/guides/runtime-node.md).
- Looking for a topic or venue: open the [documentation index](docs/README.md).

## Architecture in one minute

| Product path | Use it for | It owns |
| --- | --- | --- |
| Direct `exchange` | In-process normalized REST and WebSocket clients | Typed clients and venue normalization; the caller owns lifecycle, caching, and durability |
| Exchange Gateway | Remote REST/private-WebSocket access through a resident service | Account clients, credentials, Catalog, durable order identity, reconciliation, and event delivery |
| `runtime` | One strategy's stateful execution | Strategy context, cache, portfolio, risk, lifecycle, and reconciliation |

The boundary is deliberate:

- `exchange/` is the normalized driver and model layer used directly or by the
  Gateway.
- `gateway/` is a service above `exchange/`; it does not import `runtime/`,
  adapters, strategies, or venue SDK packages.
- `core/` defines venue-neutral decimal models, contracts, enums, and clocks.
- `runtime/` owns portable state and may depend only on `core/`.
- `adapter/<venue>/` absorbs venue products, identifiers, signing, response
  semantics, account profiles, and unsupported surfaces.
- `sdk/<venue>/` represents the venue API faithfully; an SDK package alone does
  not imply runtime-adapter support.
- `strategy/` acts through a narrow `Context`, never through an adapter or SDK.

Order submission follows one generic runtime path:

```text
ValidateSubmit → optional configured venue-neutral risk/reservation → Submit
```

Venue capacity is server-authoritative. The runtime does not have a venue-
specific prepared-submit, lease, or capacity-admission path. See
[execution and risk](docs/concepts/execution-risk.md) for the full contract.

## Offline verification

Go 1.26 or later is required by `go.mod`.

```sh
make test
make test-capabilities
```

The default suite is credential-free and uses short mode. Exchange-backed reads
and writes are opt-in; the documented Demo/Testnet Make targets set their write
gates command-locally and fail if a selected acceptance test skips.

## Current support

The runtime matrix currently contains 21 product rows across Aster, Nado,
Binance, OKX, Bybit, Bitget, Gate, Hyperliquid, and Lighter. Support is product-
specific: an implemented row or low-level SDK is not the same as current
Demo/Testnet certification or production readiness.

- [Runtime adapter capability matrix](docs/adapter-capabilities.md)
- [Venue guides](docs/venues/README.md)
- [Unsupported, deferred, and SDK-only surfaces](docs/reference/unsupported.md)
- [Testing and certification semantics](docs/reference/testing.md)

## Safety

The canonical external walkthroughs use non-production environments and bounded
orders, but they can still create exchange state. Run them serially with funded,
clean Demo/Testnet accounts. Only a zero exit proves the terminal condition
documented for that selected product. After a nonzero or ambiguous exit, inspect
exact validation IDs and authoritative product-scoped state; never blindly
cancel or flatten unrelated exposure.

## Contributing

Start with [adapter contribution rules](docs/contributing/adapters.md) or the
[public documentation contract](docs/contributing/documentation.md). Development
plans, raw validation evidence, credentials, and local execution artifacts are
not public documentation.
