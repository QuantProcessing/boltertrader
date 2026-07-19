# Exchange REST V1 Operation Matrix

Matrix-Schema: `exchange-rest-v1/v2`
Matrix-Review: `APPROVE`

本页镜像英文规范页：[Exchange REST V1 Operation Matrix](../../reference/exchange-rest-v1-operation-matrix.md)。

## 目的

本 matrix 记录 `exchange.SpotClient`、`exchange.PerpClient` 与
`exchange/factory` 实现的 public REST exchange contract。依据包括
`exchange/contract.go`、`exchange/model.go`、`exchange/ack.go`、
`exchange/factory/factory.go`、`exchange/testdata/public_surface_manifest.json`
以及 exchange contract tests。

`exchange` 是 SDK-backed public API。不要把 `adapter/*` 或 `runtime/*`
描述为它的 implementation dependency。

## Product 行

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

`factory.New` 要求一个显式 environment，本地验证 credential 和 endpoint
option，格式化时 redacts credential，并且构造期间不执行 network I/O。

## REST Operation Matrix

Legend：`A` 表示该 product row 已准入。`N/A` 表示该 product interface 在
compile time 不包含该 method。

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

Nado 将 `SetLeverage` 保留在公共 Perp REST surface 中。Nado 不提供
instrument 级杠杆 setter：该 method 会校验 context、instrument 与正数
leverage 请求，然后成功返回 `Leverage.Effective=0`。零值表示没有应用
venue-side 杠杆设置；实际杠杆由 Nado 后端 risk engine 根据账户、仓位与产品
风险状态计算。

## Order Parameter Matrix

`PlaceOrderRequest.Validate(product)` 是 REST 与 WebSocket command 共用的
public request-shape gate。

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

Common validation rule：

- `Instrument` 必填。
- `Side` 必须是 `SideBuy` 或 `SideSell`。
- `Quantity` 必须为 positive。
- `PlaceOrder` 的 `ClientOrderID` 必填，且必须是不带 leading zero 的 positive
  decimal `uint48` string（`1` 至 `281474976710655`）。
- Spot 拒绝 `ReduceOnly`。
- Portable `CancelOrderRequest` locator 是 `OrderID`。Client-order-ID-only
  cancel 不属于共享 twenty-row guarantee。`OrderID` 是本 API 返回的 venue-issued
  opaque identifier：数值型 venue 要求不带 leading zero 的 canonical positive
  decimal `int64` string；Nado 要求 canonical lowercase `0x` 前缀的 32-byte
  order digest。

## Page 与 History Semantics

`Candles`、`PublicTrades`、`OpenOrders`、`OrderHistory`、`Fills` 与
`FundingRateHistory` 只暴露所选 venue 的 native page、cursor、limit 或 bounded
time window。只有 `PageInfo.HasMoreKnown` 为 true 时，`PageInfo.HasMore` 才有
意义。缺少 cursor 或 `HasMoreKnown=false` 不能证明 full account history 已完整。

## Acknowledgement State

Order command 返回 `exchange.OrderAcknowledgement`。

| State | Meaning |
| --- | --- |
| `AckAcceptedPending` | command 或 transaction handoff 已被接受，但尚未证明 terminal order state。 |
| `AckResting` | response 证明订单 resting。Market order 不能使用此 state。 |
| `AckPartiallyFilled` | response 证明 partial fill，并携带 positive `FilledQuantity`。 |
| `AckImmediatelyFilled` | response 证明 immediate full fill。 |
| `AckCanceled` | response 证明或接受 cancellation，并携带 correlation reference。 |
| `AckRejected` | venue 明确拒绝 command，并返回 safe venue details。 |
| `AckAmbiguous` | 可能已经发送，但没有可用的权威结果。 |

Ambiguous command outcome 会与 `exchange.ErrAmbiguousOutcome` 配对。Reconciliation
前保留返回的 order ID、client order ID 或 transaction hash。

## Error Inventory

Public sentinel kind 包括 `ErrInvalidConfig`、`ErrInvalidRequest`、
`ErrAuthentication`、`ErrPermission`、`ErrRateLimit`、`ErrNotFound`、
`ErrUnsupported`、`ErrVenueRejected`、`ErrTransport`、`ErrAmbiguousOutcome`、
`ErrMalformedResponse`、`ErrCanceled`、`ErrDeadlineExceeded`、
`ErrSubscriptionGap` 与 `ErrSubscriptionClosed`。

`exchange.Error.Details()` 暴露 safe metadata：venue、product、operation、safe
code/message 与 retry-after duration。Credential、signature、auth token、signed
payload、native SDK response 与 raw request body 都不属于 public error
contract。

## Demo/Testnet 验收

Offline exchange test 是常规 documentation 与 contract gate：

```sh
make test-exchange-offline
```

Demo/Testnet acceptance 是可选的，需要 credential，并依赖具体 environment。
Read-only call 只验证实际调用的 method。`PlaceOrder`、`CancelOrder`、
`SetLeverage` 与 WebSocket order command 会修改真实的 non-production exchange
state。External validation 必须串行执行，并使用显式 symbol、显式 notional
bound、专用 non-production credential 与 cleanup evidence。

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
| BYS | Passed | Bybit Demo Spot row 已通过 full external acceptance。 |
| BYU | Passed | Bybit Demo USDT-linear Perp row 已通过 full external acceptance。 |
| BYC | Passed | Bybit Demo USDC-linear Perp row 已通过 full external acceptance。 |
| BGS | Passed | Bitget Demo Spot row 已通过 full external acceptance。 |
| BGU | Passed | Bitget Demo USDT-linear Perp row 已通过 full external acceptance。 |
| BGC | Passed | Bitget Demo USDC-linear Perp row 使用原生 `BTCPERP` 通过 full external acceptance。 |
| GTS | Passed | Gate Testnet Spot row 已通过 full external acceptance。 |
| GTU | Passed | Gate Testnet USDT-settled Perp row 已通过 full external acceptance。 |
| ATS | Passed | Aster Testnet Spot row 已通过 full external acceptance。 |
| ATP | Passed | Aster Perp row 使用 Testnet 写操作/私有流和 production 只读 funding REST/WebSocket reference data 通过验收。 |
| NDS | Passed | Nado Testnet USDT0 Spot row 已通过 full external acceptance。 |
| NDP | Passed | Nado Testnet USDT0 Perp row 已通过 full external acceptance；`SetLeverage` 返回文档约定的后端管理 `Effective=0`，`WatchMarkPrice` 返回 `ErrUnsupported`。 |

除 acceptance status table 外，本 matrix 不声称 live validation 已通过。
