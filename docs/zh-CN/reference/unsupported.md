# Unsupported、Deferred 与 SDK-only Surface

[English](../../reference/unsupported.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页拥有跨 venue 的
> unsupported、deferred 与 SDK-only inventory。
> [能力矩阵](../adapter-capabilities.md)拥有详细的静态 runtime row；venue
> 页面拥有 product-specific order、configuration 与 stream 规则。

请使用[术语表](glossary.md)中的精确状态含义。这里的限制描述
BolterTrader 的 normalized surface，而不是交易所自身提供的全部功能。

## Deferred 与缺失的 product slice

- Bybit dated linear futures 为 Deferred。当前 derivative registry 只有
  USDT-linear 与 USDC-linear Perp/SWAP row，没有 dated-Future row。
- Gate USDC-linear Perp/Futures 为 Deferred。当前 Gate derivative slice 是
  USDT-linear。
- 当前静态矩阵包含 21 个 Spot 与 Perp product row。不要因为 core enum、SDK
  model 或 venue 的外部 product catalog 中出现某个类型，就推断 runtime
  已有 Future 或 Option product。

## 跨 venue command 边界

- Strategy `Context` 只授予普通 `Submit` 与按 client ID 的 `Cancel`。它不授予
  `Modify`、`CancelAll`、execution report、account mutation 或直接访问
  adapter/SDK 的能力。
- 两个 Aster row、两个 Nado row、Binance Spot 与两个 Gate row 的静态
  `Modify` 均为 absent。其他声明了该能力的 row 仍受其 venue 页面中的
  order 与 product 限制约束。
- `CancelAll`、`SetLeverage` 与 `SetMarginMode` 是 adapter-level method，不是
  portable strategy command。它们是否可用取决于 product；不适用的实现返回
  `contract.ErrNotSupported`。
- Lighter 当前暴露 leverage 与 margin-mode code path，但其 adapter/SDK
  parameter contract 未被记录为可用，Spot scope 也未被本地拒绝，Testnet
  acceptance 不覆盖这些方法。在修正并验证 contract 与 product guard 之前，
  不要依赖这些 mutation。
- Runtime 没有通用 prepared-order、lease、venue-capacity 或
  admission-reservation protocol。Portable path 使用 side-effect-free
  `ValidateSubmit`、可选且已配置的 venue-neutral risk/reservation、durable
  intent，以及一次普通 adapter `Submit`。

Portable API 与提交流程参见[策略](../guides/strategies.md)和
[执行与风险](../concepts/execution-risk.md)。

## 跨 venue 数据与报告边界

- 当前每个 Spot row 的 normalized position report 均为 Unsupported。Spot
  balance 或 inventory state 不会变成 derivative position report。
- Aster、Nado 与 Gate 提供 venue-specific bounded/current fill source；Bybit
  提供 bounded execution history；Bitget 提供 bounded 90-day trade history。
  Binance、OKX 与 Lighter 没有具体的 normalized fill-report method。
  Hyperliquid 的静态 row 将 fill report 声明为 Unsupported，但其 Spot、Perp
  与 HIP-3 client 会获取 `UserFills` 并应用有界本地过滤；mass status 仍不声明
  authoritative fill ledger。
- 当前每个 order-report row 都带有 open-only caveat：从完整 open-order
  result 中缺席，不能确定 terminal reason，也不能重建缺失 fill。
- 当前没有任何静态 row 承诺 adapter receive/emit latency timestamp。Runtime
  bus、application、callback 与 command timing 是独立能力。
- Hyperliquid Spot 没有 normalized public book、quote 或 trade subscription。
  Authenticated private order/fill 与 spot-state plumbing 取决于成功的 adapter
  startup；仅有已分配 channel 或 configuration 不代表 ready。
- Lighter 实现 REST book/bar query 与 Perp derivative-reference streaming。
  其 normalized book、quote 与 trade subscription 为 Unsupported。
- 在 OI surface 为 Implemented 的 product 中，Current open interest 是可选的
  直接 `OpenInterestClient` query。它不是 runtime-cache 或 subscription surface。

Bars、具体 stream kind、report bound、account mutation、order type 与
time-in-force 规则均取决于 product。请查阅[venue 索引](../venues/README.md)、
能力矩阵与[市场和参考数据](../guides/market-reference-data.md)，不要从粗粒度
capability category 推断。

## SDK-only venue family

下表四行穷举了仓库中没有静态 runtime capability row 的顶层 SDK family。
它们是底层 integration surface，不代表 strategy/runtime availability，也不代表
Demo/Testnet 或 production certification。

| SDK family | SDK 中呈现的 product shape | 底层 market surface | 底层 order/account surface | 底层 stream surface |
| --- | --- | --- | --- | --- |
| Backpack | Spot plus perpetual/futures-oriented models | Markets、ticker、depth/order book、trades、funding rates、mark prices 与 klines | Account settings、balances、open orders、positions、fill history、single/batch placement（以及一个 `ExecuteOrder` compatibility wrapper）、single/all cancellation | Generic raw public/private subscriber；depth、order、fill 与 position payload models |
| EdgeX | Linear Perp | Exchange metadata、ticker、depth、klines、long/short ratio 与 current/paged/historical funding | Signed place/cancel/cancel-all、order queries、account、assets/collateral、position transactions 与 leverage | Public metadata、ticker、kline、book、trade 与 funding；private order、fill、position 与 balance |
| GRVT | Perp-oriented fixtures on a generic multi-product-shaped model | Instruments、book、mini/full ticker、trades、klines 与 historical funding | Signed create/cancel/cancel-all、open orders、leverage、account summary 与 funding-account summary | Public mini/full ticker、book、trades 与 klines；private order、fill、position 与 fund movements；authenticated WebSocket create/cancel/cancel-all |
| StandX | Perp | Symbol information/statistics、overview、depth、price、recent trades 与 funding history | Create、single/multiple cancel、leverage、margin mode、positions、balances、open orders 与 user trades | Public price、depth 与 trades；authenticated orders、positions、balance、trades 与 WebSocket create/cancel commands |

SDK 的存在既不证明 normalized adapter completeness，也不证明 external
verification。晋升需要满足[adapter 贡献合同](../contributing/adapters.md)、新增具体
静态 runtime row 与对应测试，并提供 product-scoped evidence。

## 相关参考

- [能力矩阵](../adapter-capabilities.md)
- [测试与证据](testing.md)
- [配置](configuration.md)
- [架构](../concepts/architecture.md)
