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
| What the offline suite proves, and what it does not | [Testing and evidence](../reference/testing.md) |
| Credential names and write gates | [Configuration](../reference/configuration.md) |
| Venues that exist only as an SDK, and deferred dimensions | [Unsupported, deferred, and SDK-only surfaces](../reference/unsupported.md) |

## Supported venues and product rows

Nine venues reach the gateway, across twenty product rows. Every row reaches the
venue's production endpoints: the non-production environments were removed
because each venue implemented them differently, so there is no credentialed
acceptance target left to run.

| Venue | Product rows |
| --- | --- |
| Aster | `ATS`, `ATP` |
| Binance | `BNS`, `BNP` |
| Bitget | `BGS`, `BGU`, `BGC` |
| Bybit | `BYS`, `BYU`, `BYC` |
| Gate | `GTS`, `GTU` |
| Hyperliquid | `HLS`, `HLP` |
| Lighter | `LIS`, `LIP` |
| Nado | `NDS`, `NDP` |
| OKX | `OXS`, `OXP` |

`sdk/` additionally contains Backpack, EdgeX, GRVT, and StandX. Those venues
have no `exchange/factory` construction path and therefore no product row.

## Reading a support claim

Support is per product row, and three claims are routinely confused:

- **Implemented** means the operation matrix marks the row `A` and the code
  path exists. It says nothing about external behavior today.
- **Offline-conformant** means a recorded zero-skip run of the deterministic,
  credential-free suite passed for that row. It proves the normalized
  implementation against recorded fixtures — not that the venue answers today.
- **Production-ready** is never claimed by this repository. Nothing in this
  repository establishes it, because nothing here runs against a venue.

The existence of a `sdk/<venue>` package, a make target, or a matrix cell is
not evidence of any of the three. Only a zero-skip exit code is, and only for
its own row.
