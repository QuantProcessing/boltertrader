# Exchange WebSocket V1 Operation Matrix

Matrix-Schema: `exchange-ws-v1/v1`
Matrix-Review: `APPROVE`

## Purpose

This matrix records the public WebSocket exchange contract implemented by
`exchange.SpotWebSocket`, `exchange.PerpWebSocket`, and the lazy WebSocket facets
returned by `SpotClient.WebSocket()` and `PerpClient.WebSocket()`.

The `exchange` WebSocket API is SDK-backed. Do not describe `adapter/*` or
`runtime/*` as its implementation dependencies.

## Product rows

| Row | Venue | Product | Factory config | WebSocket facet |
| --- | --- | --- | --- | --- |
| BNS | Binance | Spot | `BinanceSpotConfig` | `exchange.SpotWebSocket` |
| BNP | Binance | USD-M Perp | `BinanceUSDPerpConfig` | `exchange.PerpWebSocket` |
| OXS | OKX | Spot | `OKXSpotConfig` | `exchange.SpotWebSocket` |
| OXP | OKX | USDT-linear SWAP | `OKXUSDTPerpConfig` | `exchange.PerpWebSocket` |
| LIS | Lighter | Spot | `LighterSpotConfig` | `exchange.SpotWebSocket` |
| LIP | Lighter | Perp | `LighterPerpConfig` | `exchange.PerpWebSocket` |
| HLS | Hyperliquid | Spot | `HyperliquidSpotConfig` | `exchange.SpotWebSocket` |
| HLP | Hyperliquid | Standard Perp | `HyperliquidPerpConfig` | `exchange.PerpWebSocket` |

`factory.New` constructs the facet locally. Calling `client.WebSocket()` is lazy
and performs no network I/O.

## WebSocket operation matrix

Legend: `A` means admitted on the product row. `N/A` means absent from that
product interface at compile time.

| Operation | Event type | Scope | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| WatchOrderBook | `BookEvent` | public market stream | A | A | A | A | A | A | A | A |
| WatchBBO | `BBOEvent` | public market stream | A | A | A | A | A | A | A | A |
| WatchPublicTrades | `PublicTradeEvent` | public market stream | A | A | A | A | A | A | A | A |
| WatchCandles | `CandleEvent` | public market stream | A | A | A | A | A | A | A | A |
| WatchOrders | `OrderEvent` | private account stream | A | A | A | A | A | A | A | A |
| WatchFills | `FillEvent` | private account stream | A | A | A | A | A | A | A | A |
| WatchBalances | `BalanceEvent` | private account-wide stream | A | A | A | A | A | A | A | A |
| PlaceOrder | `OrderAcknowledgement` | private command | A | A | A | A | A | A | A | A |
| CancelOrder | `OrderAcknowledgement` | private command | A | A | A | A | A | A | A | A |
| WatchPositions | `PositionEvent` | private Perp account stream | N/A | A | N/A | A | N/A | A | N/A | A |
| WatchMarkPrice | `MarkPriceEvent` | public Perp reference stream | N/A | A | N/A | A | N/A | A | N/A | A |
| WatchFundingRate | `FundingRateEvent` | public Perp reference stream | N/A | A | N/A | A | N/A | A | N/A | A |
| Close | error return | facet lifecycle | A | A | A | A | A | A | A | A |

`PerpWebSocket` embeds the Spot WebSocket method set and adds positions, mark
price, and funding-rate streams. `SpotWebSocket` does not expose Perp-only
streams.

## Watch requests

| Request | Methods | Required fields | Notes |
| --- | --- | --- | --- |
| `WatchRequest` | `WatchOrderBook`, `WatchBBO`, `WatchPublicTrades`, `WatchOrders`, `WatchFills`, `WatchPositions`, `WatchMarkPrice`, `WatchFundingRate` | `Instrument` | Instrument must not be empty and must not contain surrounding whitespace. |
| `WatchCandlesRequest` | `WatchCandles` | `Instrument`, `Interval` | Interval must be `1m`, `5m`, `15m`, `30m`, `1h`, `4h`, `12h`, or `1d`. |
| `WatchAccountRequest` | `WatchBalances` | none | Balances are account-wide, not instrument-scoped. |

`WatchOptions.Buffer` defaults to `1024` when zero. A non-zero buffer must be in
the inclusive range `1..65536`; negative values and values above `65536` are
invalid.

## Subscription contract

Every watch returns `exchange.Subscription[T]`.

| Method | Semantics |
| --- | --- |
| `ID()` | Returns an opaque stream identifier. Do not parse it. |
| `Events()` | Receives typed stream events. The channel closes when the subscription closes. |
| `Status()` | Receives lifecycle status events. The channel closes when the subscription closes. |
| `Errors()` | Receives asynchronous stream errors. Startup and validation failures are returned directly by the watch call. |
| `Close()` | Idempotently closes the subscription, unsubscribes locally, emits `SubscriptionClosed`, and closes all subscription channels. |

Canceling the watch context closes that subscription. Closing the WebSocket
facet closes every subscription owned by that facet and then closes the
underlying public and private WebSocket backends.

## Status and error semantics

Lifecycle states are explicit:

| State | Meaning |
| --- | --- |
| `SubscriptionConnecting` | The exchange is starting the native subscription or joining an in-flight topic. |
| `SubscriptionActive` | Startup completed and the subscription can receive events. |
| `SubscriptionGap` | The backend detected a stream gap or reconnect boundary. |
| `SubscriptionResyncing` | A backend is rebuilding state after a gap. |
| `SubscriptionClosed` | The subscription or facet was closed. |

Gap phases are `GapStarted` and `GapRecovered`. Gap and reconnect status events
include venue, product, stream ID, generation, safe reason, and time when known.

Returned errors and asynchronous errors use the same public `exchange.Error`
inventory as REST. WebSocket-specific sentinel errors are
`exchange.ErrSubscriptionGap` and `exchange.ErrSubscriptionClosed`. Treat an
asynchronous error as terminal only when the subscription is closed or the
operation-specific recovery rule requires restart.

## Command semantics

`SpotWebSocket.PlaceOrder`, `PerpWebSocket.PlaceOrder`,
`SpotWebSocket.CancelOrder`, and `PerpWebSocket.CancelOrder` share the REST
order request and acknowledgement contracts:

- market orders have no public price or limit policy;
- limit orders require a positive `LimitPrice` and one of `resting`, `ioc`, or
  `post_only`;
- `ReduceOnly` is valid only for Perp clients;
- `ClientOrderID` is required and uses the same positive decimal `uint48`
  string as REST (`1` through `281474976710655`, with no leading zero);
- the portable `CancelOrder` locator is `OrderID`; client-order-ID-only cancel
  is not part of the shared eight-row guarantee; `OrderID` must be a positive
  decimal `int64` string without a leading zero;
- ambiguous sends return an ambiguous acknowledgement plus
  `exchange.ErrAmbiguousOutcome` when a send may have occurred but no
  authoritative result is available.

## Demo/Testnet acceptance

Offline WebSocket contract tests are included in:

```sh
make test-exchange-offline
```

Public stream smoke tests may connect to non-production or public endpoints when
the caller provides explicit environment configuration. Private streams and
WebSocket order commands are credentialed. `PlaceOrder` and `CancelOrder`
mutate real non-production exchange state in Demo/Testnet.

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

This matrix does not claim live validation has passed beyond the acceptance
status table.
