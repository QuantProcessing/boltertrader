# 术语表

[English](../../reference/glossary.md)

> 本页是中文镜像；英文版是规范文本。**归属：**本页定义 BolterTrader 的
> 规范公开术语。其他页面使用这些含义并链接到这里，而不自行创建定义。

## 状态与发布语义

| 术语 | 规范含义 | 不得暗示 |
| --- | --- | --- |
| Implemented | 当前代码树中存在所指 surface 的代码 | 已有 capability declaration、已通过外部验证或 production readiness |
| Capability-advertised | 静态或已配置的动态声明对精确的 product、report、command 或 stream surface 进行了声明 | 粗粒度类别中的所有方法均可用，或该 surface 已通过外部 acceptance |
| Demo/Testnet-certified | 对具名 candidate、date、environment、product scope 和 terminal assertion，具名非生产目标以 zero skips 通过 | Mainnet 正确性、永久有效、账户全局干净或 production readiness |
| Deferred | 因明确原因有意将 product 或 surface 排除在当前实现切片之外 | 交易所本身没有该功能，或该功能被永久拒绝 |
| Unsupported | 精确的 normalized surface 不存在或返回 `contract.ErrNotSupported` | 交易所或其底层 API 没有等价能力 |
| SDK-only | 存在底层 venue SDK 代码，但没有 runtime adapter/product row | 已有 normalized adapter、runtime、strategy 或 certification 覆盖 |
| Production-ready | 独立的运维证据支持在所述生产范围内使用 | 可由实现、capability bit 或 Demo/Testnet 证据推导得出 |
| Public-safe | 经过策划的材料适合公开文档树：不包含秘密与账户残留，运维证据经过摘要，且每项声明都由当前源码拥有 | 可以发布原始日志、计划、trace、reviewer 对话或私有路径 |
| Development-generated | 开发或验证变更时产生的临时计划、原始验证输出、trace、reviewer note 和迁移制品 | 它是公开事实来源，或无需策划即可复制到公开页面 |

单独使用 **supported** 和 **certified** 不是规范状态。先写明精确的
product 与 surface，再使用上表中的术语。

## 执行与证据词汇

| 术语 | 规范含义 |
| --- | --- |
| Acceptance harness | 显式 opt-in 的仓库测试，对具名非生产环境执行有界真实验证；它不是可复用的 strategy application |
| Clean final state | 在 harness 的精确 symbol、product、account 和 validation-owned-order 范围内通过 zero-exit terminal assertion |
| Ambiguous outcome | 可能已发生 venue handoff，但权威结果尚未被证明；自动重试或补偿动作不安全 |
| Open-only caveat | 完整的 open-order 覆盖可以证明范围内订单已不再 open，但不能凭空生成 terminal cause 或缺失 fill |
| Runtime latency | Runtime instrumentation 测得的 bus、application、callback 和 command timing；adapter receive/emit timing 是独立声明，也可能不存在 |

## 数据与报告词汇

| 术语 | 规范含义 |
| --- | --- |
| Market stream | 粗粒度 capability 类别；仍必须明确具体的 book、quote、trade 或 derivative-reference stream kind |
| Derivative reference data | 当精确 product 实现相应数据时，将 funding、mark、index 或 oracle 值归一化为 market event 与 cache state |
| Query-only OI | 通过可选 `OpenInterestClient` 直接获取的当前 open interest；它既不被订阅，也不存入 runtime cache |
| Account-state snapshot | `AccountClient.AccountState` 返回的强制性权威 readiness snapshot，包含该 product 的 account scope、可用 balance、margin summary、identity 与 freshness；position 仍是独立的 typed report/query evidence。 |
| Mass status | Execution adapter 直接拥有并一同返回的 order、fill 和 position report domain；account-only snapshot 不会因 reconciliation 而变成 execution-owned report support |

## 架构词汇

| 术语 | 规范含义 |
| --- | --- |
| Core | `core/` 下与 venue 无关的 enum、model、contract 和 clock |
| Runtime | `runtime/` 下与 venue 无关的 bus、cache、execution、portfolio、risk、reconciliation 和 strategy orchestration |
| Adapter | `adapter/<venue>/` 下，在 normalized contract 与 venue SDK/API 之间进行转换并承载行为边界的组件 |
| SDK | `sdk/<venue>/` 下呈现底层官方 API 形状的 client code；仅存在 SDK 不代表存在 runtime product |

具体用法参见[架构](../concepts/architecture.md)、
[能力矩阵](../adapter-capabilities.md)、
[测试与证据](testing.md)以及
[Unsupported、Deferred 与 SDK-only Surface](unsupported.md)。
