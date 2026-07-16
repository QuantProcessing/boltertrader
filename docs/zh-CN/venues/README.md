# 交易所指南

[English canonical](../../venues/README.md) · 本中文页为维护镜像。

## 归属与范围

本索引用于查找各交易所特定的环境、产品、配置、订单、账户和验收说明。
交易所页面负责定义交易所特有语义；逐产品的详细事实表仍以 21 行的
[能力矩阵](../adapter-capabilities.md)为准。

| 交易所 | 公开非生产环境 | Runtime 产品页面 |
| --- | --- | --- |
| [Binance](binance.md) | Demo | Spot, USD-M Perp |
| [OKX](okx.md) | Demo | Spot cash, USDT-linear SWAP |
| [Bybit](bybit.md) | Demo Trading | Spot, USDT-linear Perp, USDC-linear Perp |
| [Bitget](bitget.md) | Demo/PAP | Spot, USDT-linear Perp, USDC-linear Perp |
| [Gate](gate.md) | Testnet | Spot, USDT-linear Perp; USDC deferred |
| [Hyperliquid](hyperliquid.md) | Testnet | Spot, Perp, HIP-3 Perp |
| [Lighter](lighter.md) | Testnet | Spot, Perp |
| [Aster](aster.md) | Testnet | Spot, USDT-linear Perp |
| [Nado](nado.md) | Testnet | Spot no-borrow, Perp |

## 如何阅读交易所页面

- 请求方法与订阅方法会分开列出。粗粒度的 `Market`、`Execution` 或
  `Account` 流标志，并不能证明每一种标准化订阅方法都已接通。
- `Submit`、`Cancel`、`CancelAll` 和 `Modify` 与流支持分别说明。某个交易所
  即使没有私有流分发，也可能通过 REST 对命令进行对账。
- 报告覆盖会区分精确单订单证据、开放订单快照、有界成交历史、持仓和账户
  状态。除非交易所页面另有说明，仅开放订单范围内的缺失不能证明订单终态。
- 除非另有说明，参考数据仅适用于 Perp。当前未平仓量直接向交易所查询，
  runtime 不保留其缓存。
- 一次有记录、成功且零跳过的 Demo/Testnet 运行，只能证明其明确命名的产品、
  生命周期和终态断言。仅仅存在测试目标，不能证明当前外部行为；任何一次运行
  也不能证明生产就绪，或证明未命名的产品、账户模式和流维度。

文档中的验收目标使用仓库的零跳过运行器。请阅读[测试](../reference/testing.md)
了解门禁与清理语义，并阅读[配置](../reference/configuration.md)了解规范凭证名和
安全门禁名。SDK-only 交易所和 deferred 维度由
[Unsupported 和 Deferred 功能](../reference/unsupported.md)说明。
