# Venue Support

[Chinese mirror](../zh-CN/venues/README.md) · This English page is canonical.

## What this page is

This is a router, not a source of truth. Venue behavior is not documented
per venue any more: it is documented per dimension, on pages that are frozen
against the code by contract tests. Use the table below to find the owner of
whatever venue fact you need.

| Question | Owner |
| --- | --- |
| Which normalized REST products and methods exist | [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md) |
| Which normalized WebSocket products and methods exist | [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md) |
| Account system, logical identity, position mode, margin mode, leverage, order caveats | [Exchange venue and account-mode differences](../reference/exchange-venues-and-account-modes.md) |
| Which command certifies a product row against Demo/Testnet | [Testing and certification](../reference/testing.md) |
| Credential names, environments, and write gates | [Configuration](../reference/configuration.md) |
| Venues that exist only as an SDK, and deferred dimensions | [Unsupported, deferred, and SDK-only surfaces](../reference/unsupported.md) |

## Certified venues and product rows

Nine venues reach the gateway, across twenty product rows. Each row has its own
acceptance target; `make test-gateway-demo-testnet` runs all twenty serially.

| Venue | Non-production environment | Product rows | Acceptance targets |
| --- | --- | --- | --- |
| Aster | Testnet | `ATS`, `ATP` | `test-gateway-aster-spot-testnet`, `test-gateway-aster-usdt-perp-testnet` |
| Binance | Demo | `BNS`, `BNP` | `test-gateway-binance-spot-demo`, `test-gateway-binance-usd-perp-demo` |
| Bitget | Demo | `BGS`, `BGU`, `BGC` | `test-gateway-bitget-spot-demo`, `test-gateway-bitget-usdt-perp-demo`, `test-gateway-bitget-usdc-perp-demo` |
| Bybit | Demo | `BYS`, `BYU`, `BYC` | `test-gateway-bybit-spot-demo`, `test-gateway-bybit-usdt-perp-demo`, `test-gateway-bybit-usdc-perp-demo` |
| Gate | Testnet | `GTS`, `GTU` | `test-gateway-gate-spot-testnet`, `test-gateway-gate-usdt-perp-testnet` |
| Hyperliquid | Testnet | `HLS`, `HLP` | `test-gateway-hyperliquid-spot-testnet`, `test-gateway-hyperliquid-perp-testnet` |
| Lighter | Testnet | `LIS`, `LIP` | `test-gateway-lighter-spot-testnet`, `test-gateway-lighter-perp-testnet` |
| Nado | Testnet | `NDS`, `NDP` | `test-gateway-nado-spot-testnet`, `test-gateway-nado-usdt0-perp-testnet` |
| OKX | Demo | `OXS`, `OXP` | `test-gateway-okx-spot-demo`, `test-gateway-okx-usdt-perp-demo` |

`sdk/` additionally contains Backpack, EdgeX, GRVT, and StandX. Those venues
have no `exchange/factory` construction path and therefore no product row.

## Reading a support claim

Support is per product row, and three claims are routinely confused:

- **Implemented** means the operation matrix marks the row `A` and the code
  path exists. It says nothing about external behavior today.
- **Demo/Testnet-certified** means a recorded zero-skip run of that row's
  acceptance target passed. It proves only the products, lifecycle, and
  terminal assertions that run names — not neighbouring rows, account modes,
  or stream dimensions.
- **Production-ready** is never claimed by this repository. No non-production
  run establishes it.

The existence of a `sdk/<venue>` package, a make target, or a matrix cell is
not evidence of any of the three. Only a zero-skip exit code is, and only for
its own row.
