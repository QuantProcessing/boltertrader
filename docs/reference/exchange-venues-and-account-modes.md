# Exchange Venue and Account-Mode Differences

[简体中文](../zh-CN/reference/exchange-venues-and-account-modes.md) · This English page is canonical.

## Reading this page

This is the centralized owner for exchange-specific account, identity,
position-mode, margin-mode, leverage, and order caveats. It summarizes
differences without duplicating the exact REST or WebSocket operation matrices.

## Shared invariants

All rows use typed factory configs, explicit environments, normalized decimal
models, and the product-specific public interfaces. The exchange module does
not switch venue account systems, position modes, or margin modes implicitly.
There is no public `SetAccountMode`, `SetPositionMode`, or `SetMarginMode`.
Perp exposes only `SetLeverage`, and each row documents whether that operation
preserves an existing margin scope, uses a fixed scope, or delegates leverage
to the venue backend.

## Venue matrix

| Venue | Rows | Account system | Logical account identity | Position mode | Margin mode | Leverage | Order caveats |
| --- | --- | --- | --- | --- | --- | --- | --- |
| Binance | `BNS`, `BNP` | Separate Spot and USD-M API surfaces | API key and secret | Perp uses one-way/net `BOTH`; hedge-side responses are rejected | Existing cross/isolated account state; no mode selector | Perp `SetLeverage` confirms venue leverage | Perp close-position shapes are outside the portable contract |
| OKX | `OXS`, `OXP` | One trading account across Spot and SWAP | API key, secret, and passphrase | Orders target `net`; leverage accepts one current position scope | Orders use `cross`; leverage preserves discovered cross/isolated scope | Perp `SetLeverage` validates the returned scope | Multiple simultaneous position scopes are not portable |
| Lighter | `LIS`, `LIP` | One indexed account exposes assets and positions | Private key plus `accountIndex` and `keyIndex` | Signed single position per market in the normalized model | Leverage preserves the current account/position cross or isolated mode | Perp `SetLeverage` reuses the discovered margin mode | Native market and account indices stay internal |
| Hyperliquid | `HLS`, `HLP` | One owner account exposes Spot and Perp state | Signer address, or owner selected by `WithAccountAddress` | Signed one-way position per Perp asset | Leverage preserves current `cross` or `isolated`; no position defaults to cross | Perp `SetLeverage` enforces the instrument maximum when known | Agent/API-wallet signing can query the explicit owner |
| Bybit | `BYS`, `BYU`, `BYC` | `UNIFIED` wallet/account endpoints | API key and secret | Orders omit a hedge index and therefore target `positionIdx=0` one-way scope | Existing venue margin mode; no common selector | Perp `SetLeverage` sets equal buy/sell leverage | Spot market buys convert normalized base quantity to venue quote quantity |
| Bitget | `BGS`, `BGU`, `BGC` | `UTA` account/assets and order endpoints | API key, secret, and passphrase | Reads `one_way_mode` or `hedge_mode` and maps the order side | Perp orders and leverage use `crossed` | Perp `SetLeverage` uses the row settlement coin | Hedge reduce-only is encoded by position side |
| Gate | `GTS`, `GTU` | Spot reads `classic` or unified `multi_currency`/portfolio/single-currency mode; Perp uses the futures account | API key and secret | Signed one-way Perp position | Existing venue futures mode; no common selector | Perp `SetLeverage` confirms the response | Perp quantity must align to the contract multiplier |
| Aster | `ATS`, `ATP` | User account with separate API-wallet signer | User address, signer private key, and optional expected signer | Perp uses one-way/net `BOTH` | Existing venue mode; no common selector | Perp `SetLeverage` confirms the response | WebSocket-facet order commands use the signed REST command path |
| Nado | `NDS`, `NDP` | Unified-margin subaccount | Private key plus subaccount name | Signed one-way position in the normalized model | Backend unified-margin risk model; no common selector | `SetLeverage` returns `Effective=0` because leverage is backend-managed | Order IDs are lowercase `0x` digests; `WatchMarkPrice` returns `exchange.ErrUnsupported` |

## Venue notes

### Binance

The normalized Perp row is one-way only. An order, history row, or stream event
with a hedge-side position is treated as malformed instead of being silently
collapsed into one position.

### OKX

SWAP order placement uses the cross/net portable shape. Leverage updates inspect
the current position first and reject multiple simultaneous scopes because one
instrument-level request cannot identify them portably.

### Lighter

The configured indexed account is checked against account responses. Leverage
updates reuse the margin mode already reported for the position, falling back
to the account trading mode when no position exists.

### Hyperliquid

The signing identity and queried owner can differ. The explicit owner option
changes account-scoped REST and private-stream reads while retaining the signer
for authenticated actions.

### Bybit

Account reads use the unified wallet surface. The normalized order request has
no hedge-side selector, so callers must use a compatible one-way position
scope.

### Bitget

The client reads the account hold mode before Perp order placement. One-way and
hedge accounts are both mapped, while the normalized implementation keeps
crossed margin as the portable order/leverage scope.

### Gate

Spot balance reads detect classic versus unified account mode. The Perp row
uses the separate USDT-settled futures surfaces and converts normalized quantity
to an integer contract count.

### Aster

Factory validation binds the user account and API-wallet signer and can verify
an expected signer address locally. Perp order placement uses the net position
side.

### Nado

The subaccount is part of logical identity. Instrument leverage is not a venue
setting; successful validation returns zero effective leverage. Mark price is
also a documented method-level WebSocket exception rather than a removed
interface member.

## Product rows and exact operations

The [REST matrix](exchange-rest-v1-operation-matrix.md) and
[WebSocket matrix](exchange-ws-v1-operation-matrix.md) remain the exact owners
of product rows and methods. This page must not reproduce those complete
operation tables.

## Source and update rule

Update this page only from exchange code, the public surface manifest, exchange
tests, and exchange-specific acceptance evidence. If those sources disagree,
record the conflict and ask for a user decision instead of importing a claim
from adapter or runtime documentation.
