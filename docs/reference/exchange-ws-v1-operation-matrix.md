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
| BYS | Bybit | Spot | `BybitSpotConfig` | `exchange.SpotWebSocket` |
| BYU | Bybit | USDT-linear Perp | `BybitUSDTPerpConfig` | `exchange.PerpWebSocket` |
| BYC | Bybit | USDC-linear Perp | `BybitUSDCPerpConfig` | `exchange.PerpWebSocket` |
| BGS | Bitget | Spot | `BitgetSpotConfig` | `exchange.SpotWebSocket` |
| BGU | Bitget | USDT-linear Perp | `BitgetUSDTPerpConfig` | `exchange.PerpWebSocket` |
| BGC | Bitget | USDC-linear Perp | `BitgetUSDCPerpConfig` | `exchange.PerpWebSocket` |
| GTS | Gate | Spot | `GateSpotConfig` | `exchange.SpotWebSocket` |
| GTU | Gate | USDT-settled Perp | `GateUSDTPerpConfig` | `exchange.PerpWebSocket` |
| ATS | Aster | Spot | `AsterSpotConfig` | `exchange.SpotWebSocket` |
| ATP | Aster | USDT-linear Perp | `AsterUSDTPerpConfig` | `exchange.PerpWebSocket` |
| NDS | Nado | USDT0 Spot | `NadoSpotConfig` | `exchange.SpotWebSocket` |
| NDP | Nado | USDT0-settled Perp | `NadoUSDT0PerpConfig` | `exchange.PerpWebSocket` |

`factory.New` constructs the facet locally. Calling `client.WebSocket()` is lazy
and performs no network I/O.

## WebSocket operation matrix

Legend: `A` means admitted on the product row. `N/A` means absent from that
product interface at compile time.

| Operation | Event type | Scope | BNS | BNP | OXS | OXP | LIS | LIP | HLS | HLP | BYS | BYU | BYC | BGS | BGU | BGC | GTS | GTU | ATS | ATP | NDS | NDP |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| WatchOrderBook | `BookEvent` | public market stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchBBO | `BBOEvent` | public market stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchPublicTrades | `PublicTradeEvent` | public market stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchCandles | `CandleEvent` | public market stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchOrders | `OrderEvent` | private account stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchFills | `FillEvent` | private account stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchBalances | `BalanceEvent` | private account-wide stream | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| PlaceOrder | `OrderAcknowledgement` | private command | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| CancelOrder | `OrderAcknowledgement` | private command | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |
| WatchPositions | `PositionEvent` | private Perp account stream | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| WatchMarkPrice | `MarkPriceEvent` | public Perp reference stream | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| WatchFundingRate | `FundingRateEvent` | public Perp reference stream | N/A | A | N/A | A | N/A | A | N/A | A | N/A | A | A | N/A | A | A | N/A | A | N/A | A | N/A | A |
| Close | error return | facet lifecycle | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A | A |

`PerpWebSocket` embeds the Spot WebSocket method set and adds positions, mark
price, and funding-rate streams. `SpotWebSocket` does not expose Perp-only
streams.

Nado keeps `WatchMarkPrice` in the common Perp WebSocket interface so callers
can use one API shape across venues. Because Nado does not provide a mark-price
subscription, the NDP implementation returns a nil subscription and
`ErrUnsupported` immediately. `WatchFundingRate` remains a real Nado stream;
REST mark-price data remains available through the normalized Perp REST reads.

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
  is not part of the shared twenty-row guarantee; `OrderID` is the opaque
  identifier returned by the selected venue row. Numeric venues require a
  canonical positive decimal `int64` string without a leading zero, while Nado
  requires its lowercase `0x`-prefixed 32-byte order digest;
- ambiguous sends return an ambiguous acknowledgement plus
  `exchange.ErrAmbiguousOutcome` when a send may have occurred but no
  authoritative result is available.

The transport remains venue-truthful. Aster documents WebSocket market and
user-data streams but exposes order placement and cancellation through signed
REST endpoints, so the Aster WebSocket facet methods intentionally use the
same credentialed REST command path. Other rows use their venue-native
WebSocket trade command when one is available.

## Acceptance

Offline WebSocket contract tests are included in:

```sh
make test-exchange-offline
```

Public stream smoke tests may connect to public endpoints. Private streams and
order commands are credentialed and reach the venue's production environment,
including Aster's documented REST command fallback, so `PlaceOrder` and
`CancelOrder` spend real funds.

Capability cells in this matrix are implemented/admitted support, not a claim
that the venue answers today. For click-through work that must not touch a venue
at all, run the frontend against its offline mock.
