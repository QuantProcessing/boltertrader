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

| Row | Venue | Product | Factory config | Non-production environment | Live environment |
| --- | --- | --- | --- | --- | --- |
| BNS | Binance | Spot | `BinanceSpotConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| BNP | Binance | USD-M Perp | `BinanceUSDPerpConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| OXS | OKX | Spot | `OKXSpotConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| OXP | OKX | USDT-linear SWAP | `OKXUSDTPerpConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| LIS | Lighter | Spot | `LighterSpotConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| LIP | Lighter | Perp | `LighterPerpConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| HLS | Hyperliquid | Spot | `HyperliquidSpotConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| HLP | Hyperliquid | Standard Perp | `HyperliquidPerpConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| BYS | Bybit | Spot | `BybitSpotConfig` | `EnvironmentDemo` or `EnvironmentTestnet` | `EnvironmentLive` |
| BYU | Bybit | USDT-linear Perp | `BybitUSDTPerpConfig` | `EnvironmentDemo` or `EnvironmentTestnet` | `EnvironmentLive` |
| BYC | Bybit | USDC-linear Perp | `BybitUSDCPerpConfig` | `EnvironmentDemo` or `EnvironmentTestnet` | `EnvironmentLive` |
| BGS | Bitget | Spot | `BitgetSpotConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| BGU | Bitget | USDT-linear Perp | `BitgetUSDTPerpConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| BGC | Bitget | USDC-linear Perp | `BitgetUSDCPerpConfig` | `EnvironmentDemo` | `EnvironmentLive` |
| GTS | Gate | Spot | `GateSpotConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| GTU | Gate | USDT-settled Perp | `GateUSDTPerpConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| ATS | Aster | Spot | `AsterSpotConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| ATP | Aster | USDT-linear Perp | `AsterUSDTPerpConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| NDS | Nado | USDT0 Spot | `NadoSpotConfig` | `EnvironmentTestnet` | `EnvironmentLive` |
| NDP | Nado | USDT0-settled Perp | `NadoUSDT0PerpConfig` | `EnvironmentTestnet` | `EnvironmentLive` |

`factory.New` requires one explicit environment, validates credentials and
endpoint options locally, redacts credentials in formatting, and performs no
network I/O during construction.

## REST operation matrix

Legend: `A` means admitted on the product row. `N/A` means absent from that
product interface at compile time.

| Operation | Interface | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP | BYS | BYU | BYC | BGS | BGU | BGC | GTS | GTU | ATS | ATP | NDS | NDP |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| Instruments | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OrderBook | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| Candles | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| PublicTrades | `MarketREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| PlaceOrder | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| CancelOrder | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OpenOrders | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| OrderHistory | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| Fills | `OrderREST` | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
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

## Demo/Testnet acceptance

Offline exchange tests are the normal documentation and contract gate:

```sh
make test-exchange-offline
```

Demo/Testnet acceptance is optional, credentialed, and environment-specific.
Read-only calls validate only the method invoked. `PlaceOrder`, `CancelOrder`,
`SetLeverage`, and WebSocket order commands mutate real non-production exchange
state. Run external validation serially, with explicit symbols, explicit
notional bounds, dedicated non-production credentials, and cleanup evidence.

Capability cells in this matrix are implemented/admitted support, not external
environment certification. Current acceptance certification is:

| Row | Acceptance status | Reason |
| --- | --- | --- |
| BNS | Passed | Binance Demo spot row passed external acceptance. |
| BNP | Passed | Binance Demo USD-M perp row passed external acceptance. |
| OXS | Passed | OKX Demo spot row passed external acceptance. |
| OXP | Passed | OKX Demo USDT-linear SWAP row passed external acceptance. |
| LIS | Waived | Lighter Testnet ETH/USDC and LIT/USDC had platform-provided one-sided books; the user accepted the waiver, and no synthetic liquidity/self-trade was used. |
| LIP | Passed | Lighter Testnet perp row passed external acceptance. |
| HLS | Passed | Hyperliquid Testnet spot row passed external acceptance. |
| HLP | Passed | Hyperliquid Testnet standard perp row passed external acceptance. |
| BYS | Passed | Bybit Demo spot row passed full external acceptance. |
| BYU | Passed | Bybit Demo USDT-linear perp row passed full external acceptance. |
| BYC | Passed | Bybit Demo USDC-linear perp row passed full external acceptance. |
| BGS | Passed | Bitget Demo spot row passed full external acceptance. |
| BGU | Passed | Bitget Demo USDT-linear perp row passed full external acceptance. |
| BGC | Passed | Bitget Demo USDC-linear perp row passed full external acceptance with native `BTCPERP`. |
| GTS | Passed | Gate Testnet spot row passed full external acceptance. |
| GTU | Passed | Gate Testnet USDT-settled perp row passed full external acceptance. |
| ATS | Passed | Aster Testnet spot row passed full external acceptance. |
| ATP | Passed | Aster perp row passed with Testnet writes/private streams and production read-only funding REST/WebSocket reference data. |
| NDS | Passed | Nado Testnet USDT0 spot row passed full external acceptance. |
| NDP | Passed | Nado Testnet USDT0 perp row passed full external acceptance; `SetLeverage` returned the documented backend-managed `Effective=0`, and `WatchMarkPrice` returned `ErrUnsupported`. |

This matrix does not claim live validation has passed beyond the acceptance
status table.
