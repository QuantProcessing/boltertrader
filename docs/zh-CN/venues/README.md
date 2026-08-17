# Venue 支持

[English canonical](../../venues/README.md) · 本中文页为维护镜像。

## 本页的定位

本页是路由页，不是事实来源。venue 行为不再按 venue 分页描述，而是按维度
描述，且相关页面均由契约测试与代码冻结在一起。请用下表找到你需要的那条
venue 事实的归属页。

| 问题 | 归属页 |
| --- | --- |
| 存在哪些规范化 REST product 与 method | [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md) |
| 存在哪些规范化 WebSocket product 与 method | [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md) |
| Account 体系、逻辑身份、position mode、margin mode、leverage、order caveat | [Exchange venue 与 account-mode 差异](../reference/exchange-venues-and-account-modes.md) |
| 离线套件证明了什么，又没证明什么 | [测试与证据](../reference/testing.md) |
| 凭证名与写闸门 | [配置](../reference/configuration.md) |
| 只存在于 SDK 层的 venue，以及被推迟的维度 | [Unsupported、Deferred（延期）和 SDK-only surface](../reference/unsupported.md) |

## 已支持的 venue 与 product 行

九个 venue 接入 gateway，共二十个 product 行。每一行都直连交易所的 production
endpoint：非生产环境已被删除——各交易所的实现差异过大——因此不再有可运行的
带凭证验收目标。

| Venue | Product 行 |
| --- | --- |
| Aster | `ATS`, `ATP` |
| Binance | `BNS`, `BNP` |
| Bitget | `BGS`, `BGU`, `BGC` |
| Bybit | `BYS`, `BYU`, `BYC` |
| Gate | `GTS`, `GTU` |
| Hyperliquid | `HLS`, `HLP` |
| Lighter | `LIS`, `LIP` |
| Nado | `NDS`, `NDP` |
| OKX | `OXS`, `OXP` |

`sdk/` 另外还包含 Backpack、EdgeX、GRVT 和 StandX。这些 venue 没有
`exchange/factory` 构造路径，因此也没有 product 行。

## 如何理解一条支持声明

支持以 product 行为单位，且有三种声明经常被混淆：

- **Implemented** 表示 operation matrix 将该行标记为 `A` 且代码路径存在。
  它不说明当前的外部行为。
- **Offline-conformant** 表示该行在确定性、免凭证的离线套件中有一次记录在案
  的零跳过运行通过。它证明的是规范化实现与录制 fixture 相符——不证明该 venue
  今天仍然可用。
- **Production-ready** 从不由本仓库声明。本仓库中的任何东西都无法确立它，
  因为这里没有任何东西会真正连向 venue。

`sdk/<venue>` package、make target 或矩阵单元格的存在，都不能作为上述三者
中任何一条的证据。只有零跳过的退出码可以，且只对它自己那一行有效。
