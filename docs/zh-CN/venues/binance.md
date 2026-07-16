# Binance

[English canonical](../../venues/binance.md) · 本中文页为维护镜像。

## 范围与状态

当前代码树为 Spot cash 和 USD-M Perp 分别实现了 Binance adapter。两个产品的
公开非生产路径都是 **Binance Demo**。本页描述方法级行为；
[能力矩阵](../adapter-capabilities.md)负责产品行，
[术语表](../reference/glossary.md)负责定义 implemented、capability-advertised 和
`Demo/Testnet-certified` 的含义。

两个 adapter 的逻辑账户 ID 默认都是 `BINANCE-001`；可通过 `Config.AccountID`
覆盖。凭证、显式写入门禁和 secret 处理规则由[配置](../reference/configuration.md)
统一定义。如果一个进程使用两套凭证，请为它们分配不同的逻辑账户 ID。

## 交易行为

| 表面 | Spot cash | USD-M Perp |
| --- | --- | --- |
| Submit | implemented | implemented |
| Cancel | 按交易所 order ID implemented | regular 和已知 conditional 订单 implemented |
| CancelAll | implemented，symbol 范围 | implemented，symbol 范围；覆盖 regular 和开放的 conditional 订单 |
| Modify | unsupported | regular 挂单 implemented |
| Leverage mutation | unsupported | implemented；decimal 输入会截取整数部分 |
| Margin-mode mutation | unsupported | `cross` 和 `isolated` 映射到 Binance margin type |

Spot 接受 Market、Limit、StopMarket、StopLimit、MarketIfTouched 和
LimitIfTouched。Limit 系列订单接受 GTC、IOC 或 FOK；普通
`Limit + GTX` 映射到 Binance `LIMIT_MAKER`。Spot 明确拒绝
`ReduceOnly=true` 和 `PosNet` 以外的任何 position side。

USD-M Perp 接受相同系列以及 TrailingStopMarket。Limit 系列订单接受 GTC、
IOC、FOK 或 GTX。Trailing 订单要求非零 `TrailingOffsetBps`；
`ActivationPrice` 可选。`ReduceOnly` 会传给交易所，`PosNet`、`PosLong` 和
`PosShort` 映射到 Binance one-way/hedge position-side 字段。账户在交易所侧的
position mode 必须与请求一致。

Perp Modify 会先读取现有 regular 订单，因为 Binance amendment 要求 side、
quantity 和 price。`newPrice` 或 `newQty` 为零表示“保留现有值”。尚未实现
conditional/algo 订单 amendment（not implemented）。Spot Modify 被有意禁用：Binance
cancel-replace 会生成两个交易所订单实例，而 runtime 的订单身份目前只支持单实例。

## 市场、参考数据与私有流

两个产品都实现了 REST order-book 快照与 bars，以及具体的公共 book、quote 和
aggregate-trade 订阅：

| 产品 | Book | Quote | Trade | 衍生品参考数据 | 未平仓量 |
| --- | --- | --- | --- | --- | --- |
| Spot | limited depth | book ticker | aggregate trade | not applicable | not applicable |
| USD-M Perp | limited depth | book ticker | aggregate trade | REST funding/mark/index snapshot plus mark-price stream | current direct query |

当前未平仓量只支持查询；runtime 不订阅也不缓存它。`Adapter.Start` 会开启私有
user-data 流。Spot 路由 execution report 和 account-position/balance 更新；Perp
路由包含余额和持仓的 order update 与 account update。重连缺口会显式暴露，以便
进行权威对账。

## 报告、歧义与清理

Order-status 和 mass-status 恢复都基于开放订单。Perp mass status 还包括待处理
conditional 订单。两个 adapter 都未实现历史成交报告，也都没有声明 terminal
single-order report。因此，如果完整的开放订单覆盖中不存在某订单，只能证明它
不再开放；其终止原因和成交仍然未知。Spot 没有标准化持仓报告；Perp 持仓来自
account client 快照。

解析出的业务拒绝会被标记为确定的交易所拒绝。Transport error 以及 malformed、
empty 或 identity-mismatched success envelope 仍然存在歧义。在拿到精确
client/order 身份和账户证据前，不要盲目重试存在歧义的 Submit 或进行补偿。

Spot harness 只跟踪其 validation ID、所选 symbol 和权威 base balance；干净退出
允许小于一个 size step 的残余 base delta。Perp 清理同样限定在所选产品，并要求
不存在 validation 所有的开放订单且所选持仓为 flat。两种断言都不能证明整个账户
完全相等。

## 非生产验证目标

外部写入目标必须串行运行。准确的 implemented 目标如下：

```sh
make test-binance-demo-spot-data
make test-binance-demo-spot
make test-binance-demo-runtime-spot
make test-binance-demo-perp
make test-binance-demo-runtime-perp
make test-binance-demo-reference-data-read
make test-binance-demo-acceptance
```

`test-binance-demo-spot-data` 和 reference-data 目标只读；adapter/runtime
lifecycle 目标会写入有界 Demo 状态。这些目标名只能说明 harness implemented。本页不
声称当前存在 `Demo/Testnet-certified` 结果；该结果还必须具备明确候选版本、日期、
范围、零跳过和终态断言，具体见[测试与证据](../reference/testing.md)。

写入运行前，请阅读[运维与恢复](../guides/operations-recovery.md)以及规范的
[Binance Spot Demo 指南](../getting-started/cex-demo.md)。
