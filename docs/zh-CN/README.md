# BolterTrader 文档

> 英文为规范版本。[English](../README.md)

本索引是公开文档导航的主要归属页。先通过「入门」把整套栈跑起来，然后通过
Concepts 了解系统行为，通过 Guides 了解每一层的 API 与操作，通过 Reference
查阅精确的 venue 支持范围、配置和测试语义。

[返回项目中文 README](../../README.zh-CN.md)

## 各层如何组合

BolterTrader 是一个分层的整体，不是一组可互换的产品。每一层只依赖它下面的
那一层：

```text
web                        唯一的前端
services/authedge          浏览器会话与路径转发
services/trading           手动交易与资金费套利两个产品
gateway                    共享能力面
exchange                   venue-neutral 的 typed client
sdk                        每个 venue 一个 package，忠实表示其原生 API
```

强制的 import 规则见[架构与边界](concepts/architecture.md)。其中两层也可以
单独使用：

- [统一交易所 API](exchange/README.md) 可嵌入任意 Go 进程，不需要它之上的
  任何一层。
- [Exchange Gateway](guides/exchange-gateway.md) 是常驻服务，为其上的任意
  产品集中管理 account、catalog、持久订单、reconciliation 和私有事件。

## 入门

- [前置条件](getting-started/prerequisites.md)
- [运行本地栈](getting-started/local-stack.md)
- [下第一笔订单](getting-started/first-order.md)

## 概念

- [架构与边界](concepts/architecture.md)
- [Exchange 架构与概念](concepts/exchange.md)
- [Account 与 instrument](concepts/accounts-instruments.md)
- [执行与风险](concepts/execution-risk.md)
- [状态与 reconciliation](concepts/state-reconciliation.md)

## 指南

Exchange 模块：

- [独立统一交易所 API](exchange/README.md)
- [统一交易所 API 快速入门](guides/exchange-rest-quickstart.md)

服务：

- [交易所网关](guides/exchange-gateway.md)
- [Trading 服务](guides/trading-service.md)
- [Auth edge](guides/auth-edge.md)
- [Web 应用](guides/web-app.md)

运维：

- [市场与 reference data](guides/market-reference-data.md)
- [操作与恢复](guides/operations-recovery.md)

## 参考

- [Venue 支持索引](venues/README.md)
- [Exchange venue 与 account-mode 差异](reference/exchange-venues-and-account-modes.md)
- [Exchange REST V1 operation matrix](reference/exchange-rest-v1-operation-matrix.md)
- [Exchange WebSocket V1 operation matrix](reference/exchange-ws-v1-operation-matrix.md)
- [Unsupported、Deferred（延期）和 SDK-only surface](reference/unsupported.md)
- [测试与认证](reference/testing.md)
- [配置](reference/configuration.md)
- [术语表](reference/glossary.md)

## 贡献

- [新增一个 venue](contributing/venues.md)
- [文档贡献规则](contributing/documentation.md)

英文目录是事实来源。公开中文页面镜像其 scope、结构、命令、identifier、
environment variable、status token 和安全警告；私有 development artifact
绝不会被镜像。
