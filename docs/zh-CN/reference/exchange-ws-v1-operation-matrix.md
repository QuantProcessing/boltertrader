# Exchange WebSocket V1 Operation Matrix

Matrix-Schema: `exchange-ws-v1/v1`
Matrix-Review: `APPROVE`

本页镜像英文规范页：[Exchange WebSocket V1 Operation Matrix](../../reference/exchange-ws-v1-operation-matrix.md)。

## 目的

本 matrix 记录 `exchange.SpotWebSocket`、`exchange.PerpWebSocket`，以及
`SpotClient.WebSocket()`、`PerpClient.WebSocket()` 返回的 lazy WebSocket facet
实现的 public WebSocket exchange contract。

`exchange` WebSocket API 是 SDK-backed。不要把 `adapter/*` 或 `runtime/*`
描述为它的 implementation dependency。

## Product 行

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

`factory.New` 在本地构造 facet。调用 `client.WebSocket()` 是 lazy 的，不执行
network I/O。

## WebSocket Operation Matrix

Legend：`A` 表示该 product row 已准入。`N/A` 表示该 product interface 在
compile time 不包含该 method。

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

`PerpWebSocket` embeds Spot WebSocket method set，并额外提供 positions、mark
price 与 funding-rate stream。`SpotWebSocket` 不暴露 Perp-only stream。

## Watch Request

| Request | Methods | Required fields | Notes |
| --- | --- | --- | --- |
| `WatchRequest` | `WatchOrderBook`、`WatchBBO`、`WatchPublicTrades`、`WatchOrders`、`WatchFills`、`WatchPositions`、`WatchMarkPrice`、`WatchFundingRate` | `Instrument` | Instrument 不能为空，且不能有 surrounding whitespace。 |
| `WatchCandlesRequest` | `WatchCandles` | `Instrument`、`Interval` | Interval 必须是 `1m`、`5m`、`15m`、`30m`、`1h`、`4h`、`12h` 或 `1d`。 |
| `WatchAccountRequest` | `WatchBalances` | none | Balance 是 account-wide，不按 instrument scope。 |

`WatchOptions.Buffer` 为 zero 时默认是 `1024`。非 zero buffer 必须落在
inclusive range `1..65536`；negative value 与大于 `65536` 的 value 无效。

## Subscription Contract

每个 watch 返回 `exchange.Subscription[T]`。

| Method | Semantics |
| --- | --- |
| `ID()` | 返回 opaque stream identifier。不要 parse 它。 |
| `Events()` | 接收 typed stream event。Subscription close 后 channel 关闭。 |
| `Status()` | 接收 lifecycle status event。Subscription close 后 channel 关闭。 |
| `Errors()` | 接收 asynchronous stream error。Startup 与 validation failure 直接由 watch call 返回。 |
| `Close()` | Idempotently close subscription，local unsubscribe，emit `SubscriptionClosed`，并关闭所有 subscription channel。 |

取消 watch context 会关闭该 subscription。关闭 WebSocket facet 会关闭该 facet
拥有的每个 subscription，然后关闭 underlying public/private WebSocket backend。

## Status 与 Error Semantics

Lifecycle state 是显式的：

| State | Meaning |
| --- | --- |
| `SubscriptionConnecting` | exchange 正在启动 native subscription，或加入 in-flight topic。 |
| `SubscriptionActive` | startup 完成，subscription 可以接收 event。 |
| `SubscriptionGap` | backend 检测到 stream gap 或 reconnect boundary。 |
| `SubscriptionResyncing` | backend 正在 gap 后重建 state。 |
| `SubscriptionClosed` | subscription 或 facet 已关闭。 |

Gap phase 是 `GapStarted` 与 `GapRecovered`。Gap 与 reconnect status event 在已知
时包含 venue、product、stream ID、generation、safe reason 与 time。

Returned error 与 asynchronous error 使用和 REST 相同的 public `exchange.Error`
inventory。WebSocket-specific sentinel error 是 `exchange.ErrSubscriptionGap` 与
`exchange.ErrSubscriptionClosed`。只有 subscription 已关闭，或 operation-specific
recovery rule 要求 restart 时，才把 asynchronous error 视为 terminal。

## Command Semantics

`SpotWebSocket.PlaceOrder`、`PerpWebSocket.PlaceOrder`、
`SpotWebSocket.CancelOrder` 与 `PerpWebSocket.CancelOrder` 共享 REST order
request 与 acknowledgement contract：

- market order 没有 public price 或 limit policy；
- limit order 要求 positive `LimitPrice`，并使用 `resting`、`ioc` 或
  `post_only` 之一；
- `ReduceOnly` 只对 Perp client 有效；
- `ClientOrderID` 必填，并与 REST 使用相同的 positive decimal `uint48` string
  （`1` 至 `281474976710655`，且不带 leading zero）；
- portable `CancelOrder` locator 是 `OrderID`；client-order-ID-only cancel 是
  不属于共享 eight-row guarantee；`OrderID` 必须是不带 leading zero 的
  positive decimal `int64` string；
- ambiguous send 会在可能已经发送但没有权威结果时，返回 ambiguous
  acknowledgement 与 `exchange.ErrAmbiguousOutcome`。

## Demo/Testnet 验收

Offline WebSocket contract test 包含在：

```sh
make test-exchange-offline
```

Public stream smoke test 可在 caller 提供显式 environment configuration 时连接
non-production 或 public endpoint。Private stream 与 WebSocket order command
需要 credential。`PlaceOrder` 与 `CancelOrder` 会在 Demo/Testnet 修改真实的
non-production exchange state。

本 matrix 中的 capability cell 表示 implemented/admitted support，不等于
external environment certification。当前 acceptance certification 如下：

| Row | Acceptance status | Reason |
| --- | --- | --- |
| BNS | Passed | Binance Demo spot row 已通过 external acceptance。 |
| BNP | Passed | Binance Demo USD-M perp row 已通过 external acceptance。 |
| OXS | Passed | OKX Demo spot row 已通过 external acceptance。 |
| OXP | Passed | OKX Demo USDT-linear SWAP row 已通过 external acceptance。 |
| LIS | Waived | Lighter Testnet ETH/USDC 与 LIT/USDC 是 platform-provided one-sided books；user accepted waiver，且未使用 synthetic liquidity/self-trade。 |
| LIP | Passed | Lighter Testnet perp row 已通过 external acceptance。 |
| HLS | Passed | Hyperliquid Testnet spot row 已通过 external acceptance。 |
| HLP | Passed | Hyperliquid Testnet standard perp row 已通过 external acceptance。 |

除 acceptance status table 外，本 matrix 不声称 live validation 已通过。
