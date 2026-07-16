# BolterTrader

> English is canonical. [简体中文](README.zh-CN.md)

BolterTrader is a Go-native trading framework that keeps exchange differences in
SDKs and adapters while giving strategies one venue-neutral runtime. The runtime
owns stateful behavior such as orders, fills, positions, balances, portfolio,
risk, reconciliation, and reconnect handling; it does not import exchange SDKs
or adapters.

## Choose a starting path

- New to the repository: read the [prerequisites](docs/getting-started/prerequisites.md).
- No credentials: run the [offline runtime walkthrough](docs/getting-started/offline-runtime.md).
- CEX non-production validation: use [Binance Spot Demo](docs/getting-started/cex-demo.md).
- DEX non-production validation: use [Hyperliquid Perp Testnet](docs/getting-started/dex-testnet.md).
- Looking for a topic or venue: open the [documentation index](docs/README.md).

## Architecture in one minute

```text
strategy
   ↓ venue-neutral Context
runtime
   ↓ core contracts only
adapter/<venue>
   ↓ official-API translation
sdk/<venue>
```

The boundary is deliberate:

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
