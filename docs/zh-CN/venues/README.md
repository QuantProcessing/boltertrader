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
| 哪条命令能针对 Demo/Testnet 认证某个 product 行 | [测试与认证](../reference/testing.md) |
| 凭证名、environment 与写闸门 | [配置](../reference/configuration.md) |
| 只存在于 SDK 层的 venue，以及被推迟的维度 | [Unsupported、Deferred（延期）和 SDK-only surface](../reference/unsupported.md) |

## 已认证的 venue 与 product 行

九个 venue 接入 gateway，共二十个 product 行。每一行都有自己的验收目标；
`make test-gateway-demo-testnet` 会串行运行全部二十个。

| Venue | 非生产环境 | Product 行 | 验收目标 |
| --- | --- | --- | --- |
| Aster | Testnet | `ATS`, `ATP` | `test-gateway-aster-spot-testnet`, `test-gateway-aster-usdt-perp-testnet` |
| Binance | Demo | `BNS`, `BNP` | `test-gateway-binance-spot-demo`, `test-gateway-binance-usd-perp-demo` |
| Bitget | Demo | `BGS`, `BGU`, `BGC` | `test-gateway-bitget-spot-demo`, `test-gateway-bitget-usdt-perp-demo`, `test-gateway-bitget-usdc-perp-demo` |
| Bybit | Demo | `BYS`, `BYU`, `BYC` | `test-gateway-bybit-spot-demo`, `test-gateway-bybit-usdt-perp-demo`, `test-gateway-bybit-usdc-perp-demo` |
| Gate | Testnet | `GTS`, `GTU` | `test-gateway-gate-spot-testnet`, `test-gateway-gate-usdt-perp-testnet` |
| Hyperliquid | Testnet | `HLS`, `HLP` | `test-gateway-hyperliquid-spot-testnet`, `test-gateway-hyperliquid-perp-testnet` |
| Lighter | Testnet | `LIS`, `LIP` | `test-gateway-lighter-spot-testnet`, `test-gateway-lighter-perp-testnet` |
| Nado | Testnet | `NDS`, `NDP` | `test-gateway-nado-spot-testnet`, `test-gateway-nado-usdt0-perp-testnet` |
| OKX | Demo | `OXS`, `OXP` | `test-gateway-okx-spot-demo`, `test-gateway-okx-usdt-perp-demo` |

`sdk/` 另外还包含 Backpack、EdgeX、GRVT 和 StandX。这些 venue 没有
`exchange/factory` 构造路径，因此也没有 product 行。

## 如何理解一条支持声明

支持以 product 行为单位，且有三种声明经常被混淆：

- **Implemented** 表示 operation matrix 将该行标记为 `A` 且代码路径存在。
  它不说明当前的外部行为。
- **Demo/Testnet-certified** 表示该行的验收目标有一次记录在案的零跳过运行
  通过。它只证明该次运行点名的 product、生命周期与终态断言——不证明相邻的
  行、account mode 或 stream 维度。
- **Production-ready** 从不由本仓库声明。任何非生产运行都无法确立它。

`sdk/<venue>` package、make target 或矩阵单元格的存在，都不能作为上述三者
中任何一条的证据。只有零跳过的退出码可以，且只对它自己那一行有效。
