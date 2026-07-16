# Gate

[English canonical](../../venues/gate.md) · 本中文页为维护镜像。

## 范围与状态

当前 adapter 实现了 Spot cash 和 USDT-linear Perp/SWAP。公开非生产验证使用
已知的**官方 Gate Testnet** REST、Spot WS 和 USDT Futures WS profile。USDC
Perp 被明确 deferred：边界测试证明它保持关闭，不属于写入验收目标。

两个产品的逻辑账户 ID 默认都是 `GATE-001`，可通过 `Config.AccountID` 覆盖。
凭证名、写入门禁和 endpoint 安全规则由[配置](../reference/configuration.md)统一
定义。静态产品行参见[能力矩阵](../adapter-capabilities.md)，跨交易所边界清单参见
[Unsupported 和 deferred 功能](../reference/unsupported.md)。

## 交易与账户行为

Spot 和 USDT Perp 都 implemented 了 Submit 和按交易所 order ID 的 Cancel。两个产品的
CancelAll 和 Modify 均 unsupported。只标准化 Market 和 Limit 订单；Limit 接受 GTC、
IOC、FOK，以及映射到 Gate POC 的 GTX。

Spot quantity 是资产数量。请使用 `PosNet` 和 `ReduceOnly=false`；当前 Spot
conversion 不发送这些衍生品字段，也不会在本地拒绝每一种非默认组合，因此它们
不属于 implemented Spot 语义。

USDT Perp quantity 是整数 contract count：adapter 将中性 quantity 舍入为整数，
并根据 order side 添加符号。`ReduceOnly` 会发送。每次 futures Submit 前，
adapter 都会刷新 Gate 的 position mode：

- single mode 要求 `PosNet`；
- dual mode 根据 side 和 reduce-only 意图推导 `PosLong`/`PosShort`，并拒绝不匹配
  的请求；
- split 或 unknown mode unsupported。

两个产品的 leverage 或 margin-mode mutation 都 not implemented，USDT Perp 也不例外。Spot
账户状态呈 cash 形态；包含 Perp 的范围呈 margin 形态，带有 USDT balance、
margin summary 和账户支持的 position。

## 市场、参考数据与私有流

Spot 和 USDT Perp 都实现 REST order book 和 bars，以及其产品特定 WebSocket
上的具体公共 book、ticker/quote 和 trade 订阅。USDT Perp 通过 REST 实现当前
funding/mark/index 和当前 OI。`SubscribeReference` 发出 REST 快照，其 capability
是 polling，而不是 reference WebSocket stream。Spot 没有衍生品参考数据/OI
表面，runtime 不缓存 OI。

私有 Spot topic 是 orders、user trades 和 balances。私有 USDT Futures topic 是
orders、user trades、positions 和 balances；adapter 会先读取 futures account，
以获取 user ID 和 position mode。产品特定的断连会发出 reconciliation gap。

## 报告、歧义与清理

宽泛订单报告是开放订单快照。可以按交易所 order ID 精确查询终态；仅 client-ID
的查询仍基于开放订单。成交报告使用 Spot/Futures “my trades”，在获取后应用
请求的时间窗口，并且每次 mass-status 查询最多 100 条记录。Perp position 来自
USDT account snapshot/report；Spot position unsupported。

可解析的 business rejection 是确定的交易所拒绝。Context、transport 和非确定
错误仍有歧义。重试或清理前，必须对所选产品、symbol 和精确订单身份完成对账。
验收清理仅限 validation 所有的订单；Spot 还需证明权威 base balance 位于其
residual guard 内，Perp 则要求所选敞口回到 flat。

## 非生产验证目标

```sh
make test-gate-testnet-read
make test-gate-testnet-spot
make test-gate-testnet-runtime-spot
make test-gate-testnet-usdt-perp
make test-gate-testnet-runtime-usdt-perp
make test-gate-testnet-usdc-perp-deferred
make test-gate-testnet-reference-data-read
make test-gate-testnet-acceptance
make test-gate-acceptance
```

read/reference 和 deferred-boundary 目标不会写入订单。Spot 和 USDT Perp
lifecycle 目标执行有界 Testnet 写入，必须串行运行。聚合目标包含两对 live 产品、
read 检查和 deferred USDC 边界检查。Implemented 目标不会自动成为
`Demo/Testnet-certified`；请使用[测试与证据](../reference/testing.md)中带日期、
名称和零跳过的证据规则。

运行任何带凭证的写入目标前，请阅读
[运维与恢复](../guides/operations-recovery.md)。
