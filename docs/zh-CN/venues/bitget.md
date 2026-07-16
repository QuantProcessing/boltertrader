# Bitget

[English canonical](../../venues/bitget.md) · 本中文页为维护镜像。

## 范围与状态

当前 adapter 实现了 Spot，以及以 USDT 和 USDC 结算的 linear Perp/SWAP。
公开非生产验证使用 **Bitget Demo/PAP**：REST 使用 Demo trading header，公共和
私有 WebSocket 使用 PAP host family。保留的 `test-bitget-testnet-*` Make 名称是
指向 Demo 目标的兼容别名，并不代表第二种环境。

所有产品的逻辑账户 ID 默认都是 `BITGET-001`，可通过 `Config.AccountID` 覆盖。
账户状态接受交易所账户模式 `UNIFIED`、`UTA` 或 `HYBRID`；其他模式不在第一阶段
adapter 范围内。凭证名、写入门禁和 endpoint-override 安全规则由
[配置](../reference/configuration.md)统一定义。静态行由
[能力矩阵](../adapter-capabilities.md)统一定义。

## 交易与账户行为

Spot、USDT Perp 和 USDC Perp 都 implemented 了 Submit、Cancel、symbol/category 范围的
CancelAll 和 Modify。只标准化 Market 和 Limit 订单。Limit 支持 GTC、IOC、FOK
和 GTX/post-only。Modify 会省略值为零的 price/quantity 字段，并把它视为未更改。

Perp 订单使用 crossed margin。在 one-way（`one_way_mode`/`single_hold`）模式下，
使用 `PosNet`；`ReduceOnly` 会直接发送。在 hedge（`hedge_mode`/`double_hold`）
模式下，使用 `PosLong` 或 `PosShort`；reduce-only long-leg 订单必须卖出，
reduce-only short-leg 订单必须买入。Hedge-leg 请求编码 leg，而不是 one-way
reduce-only 标志。对于 Spot，请使用 `PosNet` 和 `ReduceOnly=false`；adapter 当前
不会在本地拒绝每一种衍生品专用的 Spot 组合。

Perp leverage mutation 已为所选结算 category implemented，并使用 crossed margin。
Spot leverage 和 margin-mode mutation unsupported。Spot 没有 position-report 表面；
USDT 和 USDC Perp position 是账户快照。

## 市场、参考数据与私有流

所有 implemented 产品都提供 REST order book 和 bars，以及公共 `books`、`ticker` 和
`trade` 订阅。Perp 还提供当前 funding/mark/index 快照、基于 ticker 的 reference
streaming 和直接的 current-OI 查询。Spot 没有衍生品参考数据/OI 表面。OI 只支持
查询，不会存入 runtime cache。

`Adapter.Start` 会订阅私有 `UTA/order`、`UTA/fill`、`UTA/position` 和
`UTA/account` topic。Category 与 symbol 共同组成私有 instrument 身份；范围内
无法解析的记录会触发 reconciliation gap，而不是静默跨越 Spot/Perp 范围。

## 报告、歧义与清理

宽泛订单报告是开放订单快照。当提供 instrument 和 order/client 身份时，精确单
订单查询使用交易所 order endpoint。成交报告扫描有界 trade history，并设有
严格的 90-day 下限，同时把请求拆成符合交易所大小的窗口；mass status 最多
包含 1,000 条成交，达到上限时标记为部分覆盖。衍生品 terminal-order hydration
同样有界，达到上限时保留精确订单 fallback。Perp position report 按 settlement
和 instrument 划定范围；Spot position unsupported。

可解析的 business rejection 是确定的交易所拒绝。Transport、deadline 和非确定
错误仍有歧义，直到精确订单/账户证据解析结果。Lifecycle harness 只清理所选产品
中 validation 所有的订单，对 Spot 使用所选资产的权威余额证据，并要求所选 Perp
敞口回到 flat。

## 非生产验证目标

```sh
make test-bitget-demo-spot
make test-bitget-demo-runtime-spot
make test-bitget-demo-usdt-perp
make test-bitget-demo-runtime-usdt-perp
make test-bitget-demo-usdc-perp
make test-bitget-demo-runtime-usdc-perp
make test-bitget-demo-reference-data-read
make test-bitget-demo-acceptance
make test-bitget-acceptance
```

新说明应使用 `*-demo-*` 名称。产品聚合 `test-bitget-acceptance` 仍解析到相同的
Demo 目标。Reference-data 目标只读；lifecycle 目标执行有界 PAP 写入，必须串行
运行。目标存在表示 implemented，不代表当前 `Demo/Testnet-certified` 通过。认证要求
[测试与证据](../reference/testing.md)中定义的、带日期和名称的零跳过证据。

写入运行前，请参阅[运维与恢复](../guides/operations-recovery.md)。
