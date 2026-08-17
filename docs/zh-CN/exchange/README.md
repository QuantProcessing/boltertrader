# Exchange API

[English](../../exchange/README.md) · 本页镜像英文规范页。

`exchange` 模块是独立、规范化、SDK-backed 的 API。它不依赖其上的任何一层：
调用方直接构造 typed `exchange.SpotClient` 或 `exchange.PerpClient`，并显式
选择 venue、product 与 environment。

## 从这里开始

- 阅读 [Exchange 架构与概念](../concepts/exchange.md)，了解模块边界、构造
  lifecycle、normalized product 和 account dimension。
- 按照[统一交易所 API 快速入门](../guides/exchange-rest-quickstart.md)完成 typed
  构造，以及其中不需要凭证的 public 示例。
- 如果调用方不应在进程内管理 client、缓存、凭证和重启恢复，而应通过常驻远程
  服务交互，请选择 [Exchange Gateway](../guides/exchange-gateway.md)。两者是
  同级入口，不是同一套 API 的迭代版本。

## 精确 API 清单

- [Exchange REST V1 operation matrix](../reference/exchange-rest-v1-operation-matrix.md)
  维护精确的 REST product 与 method 清单。
- [Exchange WebSocket V1 operation matrix](../reference/exchange-ws-v1-operation-matrix.md)
  维护精确的 WebSocket product 与 method 清单。

## Venue 与 account 差异

通过 [Exchange venue 与 account-mode 差异](../reference/exchange-venues-and-account-modes.md)
查看集中的九 venue account、identity、position-mode、margin-mode、leverage 与
order-caveat 摘要。

## 配置与测试

- [配置](../reference/configuration.md)维护精确的 environment、credential name、
  selector 与 endpoint-profile 规则。
- [测试与证据](../reference/testing.md)维护 offline gate、容器 gate，以及一次跑绿
  到底允许断言什么。

## 边界与事实来源

Exchange 行为声明必须以 public exchange contract、factory、manifest 与
exchange test 为依据。已经没有外部 acceptance evidence 可供援引了：产出它的那一层
已经和它所依赖的 Demo/Testnet 环境一起删除。Gateway 与产品服务文档可以提供背景，
但不能定义本模块的行为。

- [Exchange contract](../../../exchange/contract.go)
- [Factory API](../../../exchange/factory/factory.go)
- [Public surface manifest](../../../exchange/testdata/public_surface_manifest.json)
