# OKX

[English canonical](../../venues/okx.md) · 本中文页为维护镜像。

## 范围与状态

当前代码树实现了 OKX Spot cash 和 USDT-linear SWAP adapter。公开非生产验证
使用模拟模式下的 **OKX Demo Trading**。支持官方 global 和 EEA Demo host
family；自定义 REST/WS host 需要测试环境强制执行的显式 custom-write opt-in。

两个 adapter 的逻辑账户 ID 默认都是 `OKX-001`；可通过 `Config.AccountID`
覆盖。凭证名、host-profile 变量、写入门禁和 secret 规则由
[配置](../reference/configuration.md)统一定义。[能力矩阵](../adapter-capabilities.md)
负责静态行。

## 账户与产品限制

Spot 支持 `TdMode=cash` 和 `TdMode=cross`。Demo 验收会读取账户配置：简单账户
选择 cash，其他账户选择 cross。Spot 拒绝 reduce-only 订单和非 net position
side；leverage 和账户 margin-mode mutation unsupported。

USDT SWAP 接受 adapter `TdMode=cross`（默认）或 `isolated`，并把它附加到每个
订单。Demo 验收会拒绝简单账户，因为它不能交易 SWAP 生命周期。`PosNet`、
`PosLong` 和 `PosShort` 映射到 OKX `posSide`；请求必须匹配账户的 net 或
long/short position mode。Reduce-only 会原样传递。Perp leverage mutation 已为
所选 instrument 和已配置 `TdMode` implemented；`SetMarginMode` unsupported，因为 OKX
margin mode 是订单字段，不是可移植的账户级 mutation。

## 交易行为

| 表面 | Spot cash | USDT-linear SWAP |
| --- | --- | --- |
| Submit | implemented | implemented |
| Cancel | regular 订单和 adapter 已知的 algo 订单 | regular 订单和 adapter 已知的 algo 订单 |
| CancelAll | symbol 范围的 regular pending 订单 | symbol 范围的 regular pending 订单 |
| Modify | regular 订单；至少一个非零字段 | regular 订单；price 和 quantity 都必须为正 |

两个产品都实现 Market、Limit、StopMarket、StopLimit、MarketIfTouched、
LimitIfTouched 和 TrailingStopMarket。Conditional 系列使用 OKX algo endpoint；
trailing 订单要求非零 `TrailingOffsetBps`，并可包含 `ActivationPrice`。

对于 Limit，支持的 TIF 是 GTC、IOC、FOK 和 GTX/post-only。Spot Market 接受
unknown/GTC/IOC，并拒绝 Market+FOK。SWAP Market 接受 unknown/GTC；
Market+IOC 映射到 `optimal_limit_ioc`；Market+FOK unsupported。

CancelAll 枚举 regular pending-order endpoint，**不会**枚举 pending algo
parent。已知 conditional 订单需单独取消。Modify 同样使用 regular amend
endpoint。Spot adapter 会省略值为零的未更改字段，并在两个字段均为零时拒绝
请求。当前 SWAP adapter 会同时序列化 `newSz` 和 `newPx`，因此调用方必须同时
提供二者，不能依赖过时的 SDK README 示例或 zero-as-unchanged 行为。

## 市场、参考数据与私有流

两个产品都实现 REST order book 和 bars，以及公共 WebSocket order book、
ticker/top-of-book quote 和 trade 订阅。USDT SWAP 还实现当前 funding、mark 和
index 快照，funding/mark/index 流，以及直接的 current-open-interest 查询。Spot
没有衍生品参考数据或 OI 表面。OI 只支持查询，不会由 runtime 缓存。

`Adapter.Start` 为两个产品订阅私有 orders，并为 SWAP 订阅私有 positions。这些
order push 会产生 order/fill execution 事件；SWAP position push 会产生 account
事件。当前 Spot start 路径不订阅 OKX balance/account channel，因此 Spot balance
仍来自 REST account-state 快照。尽管 Spot account client 有粗粒度 Account-stream
capability 声明，也不要把 balance streaming 当成已经具体接通。

## 报告、歧义与清理

宽泛订单报告是 pending/open-order 快照；mass status 还会读取 pending algo
parent。历史成交报告和 terminal single-order report unsupported。Spot position 来自
balance，不属于 position-report 表面；SWAP position 来自 account client。完整
开放订单覆盖中的缺失只能证明“不再开放”。

OKX 每个结果中非零的 `sCode` 是确定的交易所拒绝。Transport failure、空/多结果、
缺少 ID，以及请求/响应身份不匹配仍然存在歧义。重试或清理前，必须按精确
client/order 身份完成对账。

Demo adapter 和 runtime 测试要求所选 instrument 预先没有开放订单。SWAP 还要求
所选 position 为 flat。Spot 清理仅限被跟踪的 ID 和所选资产的权威余额；SWAP
清理仅限被跟踪的 ID 和所选敞口。

## 非生产验证目标

```sh
make test-okx-demo-spot
make test-okx-demo-runtime-spot
make test-okx-demo-perp
make test-okx-demo-runtime-perp
make test-okx-demo-reference-data-read
make test-okx-demo-acceptance
```

reference-data 目标只读；其他产品目标执行有界 Demo 写入，必须串行运行。目标的
存在只能说明 harness implemented，不代表当前认证。`Demo/Testnet-certified` 声明需要
[测试与证据](../reference/testing.md)中定义的、带日期和名称的零跳过证据。

运行凭证化目标前，请参阅[运维与恢复](../guides/operations-recovery.md)。包内 SDK
README 片段不是 adapter contract；当前 adapter config 和当前 SDK request type
才是权威依据。
