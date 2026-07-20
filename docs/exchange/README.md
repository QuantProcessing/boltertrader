# Exchange API

[简体中文](../zh-CN/exchange/README.md) · This English page is canonical.

The `exchange` module is a standalone, normalized, SDK-backed API. It is
independent from `adapter/*` and `runtime/*`: callers construct a typed
`exchange.SpotClient` or `exchange.PerpClient` directly and select a venue,
product, and environment explicitly.

## Start here

- Read [Exchange architecture and concepts](../concepts/exchange.md) for the
  module boundary, construction lifecycle, normalized products, and account
  dimensions.
- Follow the [Unified exchange API quickstart](../guides/exchange-rest-quickstart.md)
  for typed construction and safe non-production examples.

## Exact API inventories

- [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
  owns the exact REST product and method inventory.
- [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
  owns the exact WebSocket product and method inventory.

## Venue and account differences

Use [Exchange venue and account-mode differences](../reference/exchange-venues-and-account-modes.md)
for the centralized nine-venue account, identity, position-mode, margin-mode,
leverage, and order-caveat summary.

## Configuration and testing

- [Configuration](../reference/configuration.md) owns exact environment,
  credential-name, selector, and endpoint-profile rules.
- [Testing and evidence](../reference/testing.md) owns offline gates and the
  Demo/Testnet command inventory.

## Boundary and source of truth

Exchange behavior claims must be grounded in the public exchange contracts,
factory, manifest, exchange tests, and exchange-specific acceptance evidence.
Adapter and runtime documentation may provide background, but it does not
define this module's behavior.

- [Exchange contracts](../../exchange/contract.go)
- [Factory API](../../exchange/factory/factory.go)
- [Public surface manifest](../../exchange/testdata/public_surface_manifest.json)
