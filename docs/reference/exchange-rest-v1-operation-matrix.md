# Exchange REST V1 Operation Matrix

Matrix-Schema: `exchange-rest-v1/v2`
Matrix-Review: `APPROVE`

## Purpose

This matrix records the public REST exchange contract implemented by
`exchange.SpotClient`, `exchange.PerpClient`, and `exchange/factory`. It is derived
from `exchange/contract.go`, `exchange/model.go`, `exchange/ack.go`,
`exchange/factory/factory.go`, `exchange/testdata/public_surface_manifest.json`,
and the exchange contract tests.

The `exchange` package is an SDK-backed public API. Do not describe `adapter/*`
or `runtime/*` as its implementation dependencies.

## Product rows

| Row | Venue | Product | Factory config |
| --- | --- | --- | --- |
| BNS | Binance | Spot | `BinanceSpotConfig` |
| BNP | Binance | USD-M Perp | `BinanceUSDPerpConfig` |
| OXS | OKX | Spot | `OKXSpotConfig` |
| OXP | OKX | USDT-linear SWAP | `OKXUSDTPerpConfig` |
| LIS | Lighter | Spot | `LighterSpotConfig` |
| LIP | Lighter | Perp | `LighterPerpConfig` |
| HLS | Hyperliquid | Spot | `HyperliquidSpotConfig` |
| HLP | Hyperliquid | Standard Perp | `HyperliquidPerpConfig` |
| BYS | Bybit | Spot | `BybitSpotConfig` |
| BYU | Bybit | USDT-linear Perp | `BybitUSDTPerpConfig` |
| BYC | Bybit | USDC-linear Perp | `BybitUSDCPerpConfig` |
| BGS | Bitget | Spot | `BitgetSpotConfig` |
| BGU | Bitget | USDT-linear Perp | `BitgetUSDTPerpConfig` |
| BGC | Bitget | USDC-linear Perp | `BitgetUSDCPerpConfig` |
| GTS | Gate | Spot | `GateSpotConfig` |
| GTU | Gate | USDT-settled Perp | `GateUSDTPerpConfig` |
| ATS | Aster | Spot | `AsterSpotConfig` |
| ATP | Aster | USDT-linear Perp | `AsterUSDTPerpConfig` |
| NDS | Nado | USDT0 Spot | `NadoSpotConfig` |
| NDP | Nado | USDT0-settled Perp | `NadoUSDT0PerpConfig` |

Every row reaches the venue's production endpoints. `factory.EnvironmentLive` is
the only environment and the default, so `factory.WithEnvironment` is optional.
`factory.New` validates credentials and endpoint options locally, redacts
credentials in formatting, and performs no network I/O during construction.

## REST operation matrix

Legend: `A` means admitted on the product row. `N/A` means absent from that
product interface at compile time.

| Operation | Interface | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP | BYS | BYU | BYC | BGS | BGU | BGC | GTS | GTU | ATS | ATP | NDS | NDP |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ResolveInstrument | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| RefreshInstruments | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| Instruments | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OrderBook | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| Candles | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| PublicTrades | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| PlaceOrder | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| CancelOrder | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OpenOrders | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OrderHistory | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| Fills | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| LookupExactOrder | `ExactOrderLookup` | A | A | A | A | A | A | A | A | A | A | A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A | N/A |
| Balances | account REST | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| SpotAccount | `SpotAccountREST` | A | N/A | A | N/A | A | N/A | A | N/A | A | N/A | N/A | A | N/A | N/A | A | N/A | A | N/A | A | N/A |
| PerpAccount | `PerpAccountREST` | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| Positions | `PerpAccountREST` | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| FundingRate | `PerpREST` | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| FundingRateHistory | `PerpREST` | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| SetLeverage | `PerpREST` | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |

Nado keeps `SetLeverage` in the common Perp REST surface. Nado does not expose
an instrument leverage setter: the method validates the context, instrument,
and positive requested value, then returns success with `Leverage.Effective=0`.
The zero value means no venue-side leverage setting was applied; Nado's backend
risk engine determines actual leverage from account, position, and product risk
state.

## Order parameter matrix

`PlaceOrderRequest.Validate(product)` is the public request-shape gate used by
REST and WebSocket commands.

| Shape | Product | `Type` | `LimitPrice` | `LimitPolicy` | `ReduceOnly` |
| --- | --- | --- | --- | --- | --- |
| Market | Spot | `OrderTypeMarket` | must be zero | must be empty | must be false |
| Market reduce-only | Perp | `OrderTypeMarket` | must be zero | must be empty | allowed |
| Limit resting | Spot | `OrderTypeLimit` | positive | `LimitPolicyResting` | must be false |
| Limit resting reduce-only | Perp | `OrderTypeLimit` | positive | `LimitPolicyResting` | allowed |
| Limit IOC | Spot | `OrderTypeLimit` | positive | `LimitPolicyIOC` | must be false |
| Limit IOC reduce-only | Perp | `OrderTypeLimit` | positive | `LimitPolicyIOC` | allowed |
| Limit post-only | Spot | `OrderTypeLimit` | positive | `LimitPolicyPostOnly` | must be false |
| Limit post-only reduce-only | Perp | `OrderTypeLimit` | positive | `LimitPolicyPostOnly` | allowed |

Common validation rules:

- `Instrument` is required.
- `Side` must be `SideBuy` or `SideSell`.
- `Quantity` must be positive.
- `PlaceOrder` `ClientOrderID` is required and must be a positive decimal
  `uint48` string with no leading zero (`1` through `281474976710655`).
- Spot rejects `ReduceOnly`.
- The portable `CancelOrderRequest` locator is `OrderID`. Client-order-ID-only
  cancel is not part of the shared twenty-row guarantee. `OrderID` is the
  venue-issued opaque identifier returned by this API. Numeric venues require
  a canonical positive decimal `int64` string without a leading zero; Nado
  requires its canonical lowercase `0x`-prefixed 32-byte order digest.

## Page and history semantics

`Candles`, `PublicTrades`, `OpenOrders`, `OrderHistory`, `Fills`, and
`FundingRateHistory` expose the native page, cursor, limit, or bounded time
window available from the selected venue. `PageInfo.HasMore` is meaningful only
when `PageInfo.HasMoreKnown` is true. A missing cursor or
`HasMoreKnown=false` is not evidence that full account history is complete.

## Acknowledgement states

Order commands return `exchange.OrderAcknowledgement`.

| State | Meaning |
| --- | --- |
| `AckAcceptedPending` | The command or transaction handoff was accepted, but no terminal order state is proven. |
| `AckResting` | The response proves that the order is resting. A market order cannot use this state. |
| `AckPartiallyFilled` | The response proves a partial fill and carries positive `FilledQuantity`. |
| `AckImmediatelyFilled` | The response proves an immediate full fill. |
| `AckCanceled` | The response proves or accepts cancellation with a correlation reference. |
| `AckRejected` | The venue definitively rejected the command and returns safe venue details. |
| `AckAmbiguous` | A send may have occurred, but no authoritative result is available. |

Ambiguous command outcomes pair with `exchange.ErrAmbiguousOutcome`. Preserve the
returned order ID, client order ID, or transaction hash before reconciliation.

## Error inventory

The public sentinel kinds are `ErrInvalidConfig`, `ErrInvalidRequest`,
`ErrAuthentication`, `ErrPermission`, `ErrRateLimit`, `ErrNotFound`,
`ErrUnsupported`, `ErrVenueRejected`, `ErrTransport`, `ErrAmbiguousOutcome`,
`ErrMalformedResponse`, `ErrCanceled`, `ErrDeadlineExceeded`,
`ErrSubscriptionGap`, and `ErrSubscriptionClosed`.

`exchange.Error.Details()` exposes safe metadata: venue, product, operation, safe
code/message, and retry-after duration. Credentials, signatures, auth tokens,
signed payloads, native SDK responses, and raw request bodies are not part of
the public error contract.

## Acceptance

Offline exchange tests are the documentation and contract gate:

```sh
make test-exchange-offline
```

Capability cells in this matrix are implemented/admitted support, not a claim
that the venue answers today. There is no non-production environment left to
certify against: every credentialed call reaches the real venue, and
`PlaceOrder`, `CancelOrder`, `SetLeverage`, and WebSocket order commands spend
real funds. Validate against production only with a dedicated account, explicit
symbols, explicit notional bounds, serial execution, and terminal cleanup
evidence. For click-through work that must not use credentials or write to a
venue, run the application anonymously in read-only mode; its public market
data is still live.
