# BolterTrader Documentation

> English is canonical. [简体中文](zh-CN/README.md)

This index is the primary owner of public documentation navigation. Start with
Getting started to run the stack, use Concepts for system behavior, Guides for
each layer's API and operations, and Reference for exact venue support,
configuration, and test semantics.

[Back to the project README](../README.md)

## How the layers fit

BolterTrader is one layered system, not a set of interchangeable products. Each
layer depends only on the one below it:

```text
web                        the only frontend
services/authedge          browser sessions and path forwarding
services/trading           the manual-trading product over the gateway
gateway                    shared capability plane
exchange                   venue-neutral typed clients
sdk                        one package per venue's native API
```

Read [Architecture and boundaries](concepts/architecture.md) for the enforced
import rules. Two layers are also usable on their own:

- [The unified exchange API](exchange/README.md) embeds in any Go process and
  needs none of the layers above it.
- [The Exchange Gateway](guides/exchange-gateway.md) is a resident service that
  centralizes accounts, catalog, durable orders, reconciliation, and private
  events for whatever product sits on top.

## Getting started

- [Prerequisites](getting-started/prerequisites.md)
- [Run the local stack](getting-started/local-stack.md)
- [Place your first order](getting-started/first-order.md)

## Concepts

- [Architecture and boundaries](concepts/architecture.md)
- [Exchange architecture and concepts](concepts/exchange.md)
- [Accounts and instruments](concepts/accounts-instruments.md)
- [Execution and risk](concepts/execution-risk.md)
- [State and reconciliation](concepts/state-reconciliation.md)

## Guides

Exchange module:

- [Standalone unified exchange API](exchange/README.md)
- [Unified exchange API quickstart](guides/exchange-rest-quickstart.md)

Services:

- [Exchange Gateway](guides/exchange-gateway.md)
- [Trading service](guides/trading-service.md)
- [Auth edge](guides/auth-edge.md)
- [Web app](guides/web-app.md)

Operating:

- [Market and reference data](guides/market-reference-data.md)
- [Operations and recovery](guides/operations-recovery.md)

## Reference

- [Venue support index](venues/README.md)
- [Exchange venue and account-mode differences](reference/exchange-venues-and-account-modes.md)
- [Exchange REST V1 operation matrix](reference/exchange-rest-v1-operation-matrix.md)
- [Exchange WebSocket V1 operation matrix](reference/exchange-ws-v1-operation-matrix.md)
- [Unsupported, deferred, and SDK-only surfaces](reference/unsupported.md)
- [Testing and certification](reference/testing.md)
- [Configuration](reference/configuration.md)
- [Glossary](reference/glossary.md)

## Contributing

- [Adding a venue](contributing/venues.md)
- [Documentation contribution rules](contributing/documentation.md)

The English tree is the source of truth. Public Chinese pages mirror its scope,
structure, commands, identifiers, environment variables, status tokens, and
safety warnings; private development artifacts are never mirrored.
