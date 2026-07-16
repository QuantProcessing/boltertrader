# 架构

> 英文为规范版本。[English](../../concepts/architecture.md)

本页定义 BolterTrader 稳定的分层与依赖边界。详细的提交语义由
[执行与风险](execution-risk.md)说明，状态收敛则由
[状态与对账](state-reconciliation.md)说明。

## 分层与依赖方向

| 层 | 负责 | 可以了解 |
| --- | --- | --- |
| `core/` | 中立模型、枚举、时钟、事件信封、能力与客户端契约 | 标准库和精确十进制基础类型 |
| `runtime/` | 事件处理、缓存、投资组合、执行、风险、日志、生命周期与对账 | 中立的 `core/{clock,contract,enums,model}` 包及同级 runtime 包 |
| `adapter/<venue>/` | 交易品种解析、账户模式发现、无状态请求校验、归一化、流/报告语义与交易所错误映射 | `core/`、对应交易所 SDK 及共享 adapter 辅助代码 |
| `sdk/<venue>/` | 官方 API 传输、签名、端点与线协议类型 | 交易所 API，而非 runtime 策略 |
| `runtime/strategy` | 可移植的策略上下文与回调契约 | 中立的 runtime 视图与契约 |
| `strategy/` and `cmd/` | 示例策略与应用组合 | Runtime 以及显式选择的 adapter |

在本仓库中，生产 runtime 代码不导入 `adapter/**` 或 `sdk/**`。它在项目层
只依赖上表列出的四个中立 core 包和其他 runtime 包；标准库与
`shopspring/decimal` 导入不会削弱这条边界。交易所名称可以作为
`InstrumentID` 或能力声明中的数据，但不能用来选择交易所专用的 runtime
分支。

## 组合边界

应用代码选择交易所，并构造其 SDK 与 adapter。它只把
`contract.MarketDataClient`、`contract.ExecutionClient` 和
`contract.AccountClient` 传给 `runtime.NewNode`。对于仅市场数据进程等部分
节点，任一客户端都可以缺省。

Runtime 通过市场客户端的 `InstrumentProvider` 获取归一化交易品种，分别
保留执行能力与账户能力的来源，并从配置的中立客户端中解析一个逻辑账户
身份。策略接收的是 runtime 上下文，而不是 adapter 或 SDK 句柄。

## 状态归属与串行化

节点通过一条 runtime 总线消费类型化的市场、执行和账户信封。实时事件变更
与恢复变更都通过 runtime 自己管理的缓存、投资组合、成交与回调路径收敛。
直接对账与事件应用串行执行，因此权威快照不会与实时变更竞争并产生策略可见
的错误状态。

主要状态所有者有意彼此分离：

- cache 负责归一化的当前状态；
- portfolio 负责账户感知的敞口与估值视图；
- execution journal 负责命令意图和结果恢复；
- reconciliation 负责在明确限定的范围内与权威报告比较；
- lifecycle 负责命令处于 active、restricted 还是 halted 状态。

这些组件交换中立模型。任何组件都不会接收 SDK 响应类型。

## 交易所差异应放在哪里

Adapter 负责那些无法表达为通用策略的差异：原生 symbol 或 asset index、
支持的订单字段、账户和持仓模式、请求签名、端点 profile、stream gap 解释、
报告覆盖范围以及精确的响应身份。SDK 负责其线协议表示。Runtime 只负责这些
差异被转换为 core 契约后的可移植编排。

Runtime 的提交表面就是普通的 `ExecutionClient` 契约。Adapter 可以在实现
`Submit` 时在内部准备或签名请求，但 runtime 不存在 prepared-order、venue
pre-trade lease 或 venue-capacity admission 协议。唯一规范流程见
[执行与风险](execution-risk.md)。

## 贡献者边界检查

增加 runtime 抽象之前，先判断每个已实现（implemented）交易所能否在不暴露 SDK 类型或交易所
专用生命周期的前提下表达它。如果不能，请把行为留在 adapter 中，并只暴露
最小的中立能力。

接下来可阅读[账户与交易品种](accounts-instruments.md)、
[adapter 贡献指南](../contributing/adapters.md)以及详细的
[能力矩阵](../adapter-capabilities.md)。
