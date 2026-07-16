# BolterTrader 文档

> 英文为规范版本。[English](../README.md)

本索引是公开文档导航的主要归属页。先选择一个演练，然后通过 Concepts 了解系统
行为，通过 Guides 了解 API 和操作，通过 Venues 了解 product-specific 事实，
通过 Reference 查阅精确的状态、配置和测试语义。

[返回项目中文 README](../../README.zh-CN.md)

## 入门

- [前置条件](getting-started/prerequisites.md)
- [Binance Spot Demo](getting-started/cex-demo.md)
- [Hyperliquid Perp Testnet](getting-started/dex-testnet.md)
- [离线 runtime](getting-started/offline-runtime.md)

## 概念

- [架构与边界](concepts/architecture.md)
- [执行与风险](concepts/execution-risk.md)
- [状态与 reconciliation](concepts/state-reconciliation.md)
- [Account 与 instrument](concepts/accounts-instruments.md)

## 指南

- [编写策略](guides/strategies.md)
- [构建 runtime node](guides/runtime-node.md)
- [市场与 reference data](guides/market-reference-data.md)
- [操作与恢复](guides/operations-recovery.md)

## Venues

- [Venue 指南索引](venues/README.md)
- [Binance](venues/binance.md)
- [OKX](venues/okx.md)
- [Bybit](venues/bybit.md)
- [Bitget](venues/bitget.md)
- [Gate](venues/gate.md)
- [Hyperliquid](venues/hyperliquid.md)
- [Lighter](venues/lighter.md)
- [Aster](venues/aster.md)
- [Nado](venues/nado.md)

## 能力与参考

- [Runtime adapter 能力矩阵](adapter-capabilities.md)
- [Unsupported、Deferred（延期）和 SDK-only surface](reference/unsupported.md)
- [测试与认证](reference/testing.md)
- [配置](reference/configuration.md)
- [术语表](reference/glossary.md)

## 贡献

- [Adapter 贡献规则](contributing/adapters.md)
- [文档贡献规则](contributing/documentation.md)

英文目录是事实来源。公开中文页面镜像其 scope、结构、命令、identifier、
environment variable、status token 和安全警告；私有 development artifact
绝不会被镜像。
