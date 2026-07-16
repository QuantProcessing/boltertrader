# BolterTrader

> 英文为规范版本。[English](README.md)

BolterTrader 是一个 Go 原生交易框架：它将交易所差异收敛在 SDK 和
adapter 中，同时为策略提供统一的、与 venue 无关的 runtime。runtime 负责订单、
成交、持仓、余额、portfolio、risk、reconciliation 和重连处理等有状态行为；
它不导入交易所 SDK 或 adapter。

## 选择起步路径

- 初次接触本仓库：阅读[前置条件](docs/zh-CN/getting-started/prerequisites.md)。
- 没有凭证：运行[离线 runtime 演练](docs/zh-CN/getting-started/offline-runtime.md)。
- CEX 非生产验证：使用 [Binance Spot Demo](docs/zh-CN/getting-started/cex-demo.md)。
- DEX 非生产验证：使用 [Hyperliquid Perp Testnet](docs/zh-CN/getting-started/dex-testnet.md)。
- 查找某个主题或 venue：打开[文档索引](docs/zh-CN/README.md)。

## 一分钟了解架构

```text
strategy
   ↓ venue-neutral Context
runtime
   ↓ core contracts only
adapter/<venue>
   ↓ official-API translation
sdk/<venue>
```

这些边界是有意设计的：

- `core/` 定义与 venue 无关的 decimal model、contract、enum 和 clock。
- `runtime/` 负责可移植状态，并且只能依赖 `core/`。
- `adapter/<venue>/` 吸收 venue product、identifier、签名、响应语义、
  account profile 和 Unsupported（不支持的）surface 等差异。
- `sdk/<venue>/` 忠实表示 venue API；仅有 SDK package 并不意味着存在
  runtime-adapter 支持。
- `strategy/` 通过收窄的 `Context` 行动，绝不直接使用 adapter 或 SDK。

订单提交遵循一条通用 runtime 路径：

```text
ValidateSubmit → optional configured venue-neutral risk/reservation → Submit
```

venue capacity 以服务端为准。runtime 不存在 venue-specific prepared-submit、
lease 或 capacity-admission 路径。完整契约见
[执行与风险](docs/zh-CN/concepts/execution-risk.md)。

## 离线验证

`go.mod` 要求 Go 1.26 或更高版本。

```sh
make test
make test-capabilities
```

默认测试套件不需要凭证，并使用 short mode。交易所读写均为显式启用；文档中的
Demo/Testnet Make target 会仅为对应命令设置写入 gate，并在选定的验收测试发生
skip 时失败。

## 当前支持

runtime 矩阵目前包含 21 个 product 行，覆盖 Aster、Nado、Binance、OKX、
Bybit、Bitget、Gate、Hyperliquid 和 Lighter。支持以 product 为单位：
某一行处于 Implemented（已实现）状态或存在底层 SDK，不等同于当前已通过
Demo/Testnet 认证，也不代表 production readiness。

- [Runtime adapter 能力矩阵](docs/zh-CN/adapter-capabilities.md)
- [Venue 指南](docs/zh-CN/venues/README.md)
- [Unsupported、Deferred（延期）和 SDK-only surface](docs/zh-CN/reference/unsupported.md)
- [测试与认证语义](docs/zh-CN/reference/testing.md)

## 安全

规范的外部演练使用非生产环境和有界订单，但仍可能创建交易所状态。请使用有资金、
干净的 Demo/Testnet account，并串行运行。只有退出码为零，才能证明选定 product
所记录的终止条件。在非零或结果不明确的退出后，检查精确的 validation ID 和权威的
product-scoped 状态；绝不要盲目取消或平掉无关 exposure。

## 贡献

请从 [adapter 贡献规则](docs/zh-CN/contributing/adapters.md)或
[公开文档契约](docs/zh-CN/contributing/documentation.md)开始。development plan、
原始 validation evidence、凭证和本地执行产物不属于公开文档。
