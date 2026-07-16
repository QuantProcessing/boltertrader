# Bybit

[English canonical](../../venues/bybit.md) · 本中文页为维护镜像。

## 范围与状态

当前 adapter 实现了 Spot，以及以 USDT 和 USDC 结算的 linear Perp/SWAP。
Dated linear futures 被有意过滤并 deferred。公开验证使用**主网账户上的 Bybit Demo
Trading**，不是 Bybit Testnet，也不是 Testnet demo 凭证。

所有产品共享一个已配置的 unified account 身份，默认值为 `BYBIT-001`。
Account-state 路径会拒绝非 unified account mode。Demo preflight 还要求可写的
unified-account API key；Spot 需要 SpotTrade permission，账户对账需要
ContractTrade Position permission；Perp 还需要 ContractTrade Order permission。
凭证名和写入门禁参见[配置](../reference/configuration.md)，静态行参见
[能力矩阵](../adapter-capabilities.md)。

## 交易与账户行为

三个产品行都 implemented 了 Submit、Cancel、symbol/category 范围的 CancelAll 和 Modify。
只标准化 Market 和 Limit 订单。Limit 支持 GTC、IOC、FOK，以及映射到 Bybit
`PostOnly` 的 GTX。Modify 只发送非零的 price/quantity 变更字段；值为零的字段
会被省略。

`PosNet`、`PosLong` 和 `PosShort` 分别映射到 Bybit position index 0、1 和 2，
`ReduceOnly` 会原样传递。这些字段必须匹配交易所账户的 one-way 或 hedge mode。
对于 Spot，请使用 `PosNet` 和 `ReduceOnly=false`；adapter 当前不会为衍生品专用
字段添加 Spot 本地拒绝，因此这些字段的存在不属于 implemented Spot 语义。

Account client 始终读取 `UNIFIED` wallet，并暴露 margin 形态的账户状态，即使
配置的产品范围只有 Spot 也是如此。Linear USDT 和 USDC position 是账户快照；
Spot 没有标准化 position report。Perp leverage mutation 会同时设置 buy 和 sell
leverage。Spot leverage 和 per-symbol margin-mode mutation unsupported。

## 市场、参考数据与私有流

所有 implemented 产品都提供 REST order book 和 bars，并把公共 `orderbook.50`、
`tickers` 和 `publicTrade` 订阅标准化为 book、quote 和 trade 事件。Linear Perp
还提供当前 funding/mark/index 快照、基于 ticker 的 reference streaming，以及
直接的 current-OI 查询。Spot 没有衍生品参考数据或 OI 表面，runtime 也不缓存
OI。

`Adapter.Start` 会订阅私有 `order`、`execution`、`position` 和 `wallet` topic。
无法标准化的重要 execution record 会发出 gap signal，使 runtime 对账继续保持
权威。重连缺口同样会显式暴露。

## 报告、歧义与清理

宽泛订单报告使用 Bybit realtime/open record。精确 client/order 身份会先查询
realtime record，再查询经过筛选的 order history。成交报告使用 execution
history，排除 funding row，并受查询范围限制；mass status 最多恢复 1,000 条
成交。对于衍生品成交，terminal-order hydration 扫描七天窗口，最多 1,000 条
记录；达到上限时仍需精确订单 fallback。Perp position report 查询 USDT/USDC
结算范围；Spot position unsupported。

可解析的 command/business rejection 是确定的交易所拒绝。Transport、timeout
和其他非确定性失败仍有歧义。在精确订单和账户证据解析结果之前，不要重试存在
歧义的写入。

验收生命周期要求写入前所选范围内没有开放订单。它会跟踪精确 validation 身份，
证明所选 symbol 的开放订单已清理，对 Spot 使用权威 base-balance 证据，并要求
所选 Perp 敞口回到 flat。这不是全账户清理。

## 非生产验证目标

```sh
make test-bybit-demo-spot
make test-bybit-demo-runtime-spot
make test-bybit-demo-usdt-perp
make test-bybit-demo-runtime-usdt-perp
make test-bybit-demo-usdc-perp
make test-bybit-demo-runtime-usdc-perp
make test-bybit-demo-reference-data-read
make test-bybit-demo-acceptance
make test-bybit-acceptance
```

最后两个聚合目标当前覆盖相同的三对产品；不带 `demo` 的名称是产品聚合，而非
第二种环境。Reference data 只读；lifecycle 目标会写入有界 Demo 状态，必须串行
运行。Implemented 目标不会自动成为 `Demo/Testnet-certified`。请使用
[测试与证据](../reference/testing.md)中带日期、零跳过的证据规则。

任何写入运行前，请参阅[运维与恢复](../guides/operations-recovery.md)。
