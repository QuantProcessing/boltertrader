# Hyperliquid

[English canonical](../../venues/hyperliquid.md) · 本中文页为维护镜像。

## 归属与范围

本页负责说明 Hyperliquid Testnet 产品范围、身份规则、动态流注意事项、订单和报告
限制，以及有界验收行为。[能力矩阵](../adapter-capabilities.md)仍是逐行清单。

## 环境、产品与身份

Runtime adapter 覆盖 Spot cash、标准 Perp 和 HIP-3 Perp。公开验证使用固定的
Hyperliquid Testnet REST/WS endpoint。规范 DEX 指南是
[Hyperliquid Perp Testnet](../getting-started/dex-testnet.md)。

私有操作使用 API-wallet 或 owner private key。Adapter 通过 Hyperliquid
`userRole` 解析 signer：`user` signer 拥有自身，而 `agent` signer 会映射到返回的
owner address。显式配置的 account address 必须匹配解析出的 owner。规范账户模型
会拒绝 `vault` 和 `subAccount` role。

HIP-3 不会从标准 Perp symbol 推断。请求的 instrument 必须带有显式的 HIP-3
DEX-qualified venue symbol，例如 `dex:coin`；发现、订单、报告和清理始终限定在
该 DEX。

准确的环境变量和安全门禁统一记录在[配置](../reference/configuration.md)中。

## 已实现范围（Implemented surface）

| 产品 | 市场请求 | 标准化市场流 | Execution 与 account 流 |
| --- | --- | --- | --- |
| Spot | `OrderBook`、`Bars` | `SubscribeBook`、`SubscribeQuotes` 和 `SubscribeTrades` unsupported | 静态行不声明宽泛 streaming。具备认证 WS 身份时，`Adapter.Start` 会有条件地订阅已确认的 `orderUpdates`、`userFills` 和 `spotState`，发出 execution/account 事件和 gap 证据。 |
| Standard Perp | `OrderBook`、`Bars` | Book、quote 和 trade 订阅 | 配置后提供私有 order、fill 和 clearinghouse/account state 订阅 |
| HIP-3 Perp | 与 Perp 相同的请求，限定于显式 DEX | 与 Perp 相同的订阅，限定于显式 DEX | 与 Perp 相同的私有流，账户状态按所选 DEX/account mode 解析 |

三个产品都实现 `Submit`、`Cancel`、合成的 `CancelAll` 和 `Modify`。`CancelAll`
会枚举并取消当前开放订单；它不能替代交易所无关的清理逻辑，测试仍需跟踪精确的
新建订单。

### 订单与账户变更

- Spot 只接受 limit 订单。TIF 可以是 GTC/default、IOC 或 GTX（映射到
  Hyperliquid ALO）；FOK unsupported。Spot 拒绝 `ReduceOnly` 和非 net position
  side。Spot 的 leverage 和 margin-mode mutation unsupported。
- Perp 和 HIP-3 接受 limit 订单、带显式激进 `Price` 安全边界的 market 订单，
  以及同时带 `TriggerPrice` 和 wire `Price` 的 stop-loss/take-profit market 或
  limit trigger。Limit TIF 可以是 GTC/default、IOC 或 GTX；FOK unsupported。只
  支持 net position mode，并保留 `ReduceOnly`。
- Perp `Modify` 会重建现有订单，并保留 type、TIF、trigger、client identity 和
  reduce-only 语义。当交易所响应没有足够语义可安全修改时，它会 fail closed。
- Perp account mutation 支持 leverage 和 cross/isolated margin mode。这并不
  意味着支持 portfolio-margin 或 unsupported owner role。

### 报告、持仓与账户状态

Spot、Perp 和 HIP-3 都提供开放订单快照，以及按交易所 order ID 或映射后的 client
identity 进行的精确单订单状态查询。`GenerateFillReports` 获取交易所的
`UserFills` 快照，并在本地应用有界的 identity、time 和 limit filter；但声明的
mass-status 恢复仍有意聚焦开放订单，不声明权威成交历史覆盖。不要把宽泛的
mass-status 调用当作完整成交账本。

Spot 库存由账户余额表示，没有 position-report 表面。Perp 和 HIP-3 position
来自 account client。Execution mass status 不能替代这些由账户支持的 position
快照。

## 衍生品参考数据

标准 Perp 和 HIP-3 提供当前 funding、mark price、oracle price，以及 reference
polling/snapshot 事件。当前未平仓量是直接的 asset-context 查询，不会由 runtime
缓存。Spot 没有衍生品参考数据表面。

## 验证命令

```sh
make test-hyperliquid-testnet-runtime-spot
make test-hyperliquid-testnet-runtime-perp
make test-hyperliquid-testnet-runtime-hip3
make test-hyperliquid-testnet-acceptance
make test-hyperliquid-testnet-reference-data-read
```

聚合目标包括 Spot、标准 Perp 和 HIP-3 的 read、adapter-write 和 runtime-write
覆盖。每个写入目标都受[配置](../reference/configuration.md)中记录的 Testnet 写入
门禁约束。

## 验收、清理与歧义

写入生命周期会挂出并取消一个挂单、提交一次有界 IOC 成交，并平掉由此产生
的敞口。Perp 平仓使用 reduce-only；Spot 清理使用所选基础币余额基线和允许的
dust。最终对账只在所选 product/instrument/account 范围内证明没有测试创建的
开放订单和残余敞口。

当 submit、cancel、fill 或 close 的结果存在歧义时，必须先解析精确订单身份、
开放订单快照、匹配成交和所选 balance/position，再产生另一个副作用。绝不能
盲目重试 close，也不能用全账户 `CancelAll` 证明歧义订单没有执行。

参见[运维与恢复](../guides/operations-recovery.md)、
[配置](../reference/configuration.md)和[能力矩阵](../adapter-capabilities.md)。
