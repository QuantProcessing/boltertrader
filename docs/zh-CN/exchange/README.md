# Exchange API

[English](../../exchange/README.md) · 本页镜像英文规范页。

`exchange` 模块是独立、规范化、SDK-backed 的 API。它与 `adapter/*` 和
`runtime/*` 相互独立：调用方直接构造 typed `exchange.SpotClient` 或
`exchange.PerpClient`，并显式选择 venue、product 与 environment。

## 从这里开始

- 阅读 [Exchange 架构与概念](../concepts/exchange.md)，了解模块边界、构造
  lifecycle、normalized product 和 account dimension。
- 按照[统一交易所 API 快速入门](../guides/exchange-rest-quickstart.md)完成 typed
  构造与安全的非生产示例。
- 如果调用方不应在进程内管理 client、缓存、凭证和重启恢复，而应通过常驻远程
  服务交互，请选择 [Exchange Gateway](../guides/exchange-gateway.md)。
- 如果进程承载单一有状态策略，请选择 [runtime node](../guides/runtime-node.md)。
  三者是同级产品路径，不是直接 API 的迭代版本。

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
- [测试与证据](../reference/testing.md)维护 offline gate 与 Demo/Testnet command
  清单。

## 边界与事实来源

Exchange 行为声明必须以 public exchange contract、factory、manifest、
exchange test 与 exchange-specific acceptance evidence 为依据。Adapter 和
runtime 文档可以提供背景，但不能定义本模块的行为。

- [Exchange contract](../../../exchange/contract.go)
- [Factory API](../../../exchange/factory/factory.go)
- [Public surface manifest](../../../exchange/testdata/public_surface_manifest.json)
