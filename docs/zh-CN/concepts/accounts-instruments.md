# 账户与交易品种

> 英文为规范版本。[English](../../concepts/accounts-instruments.md)

本页定义交易所中立的交易品种、账户、余额、保证金、持仓和账户 readiness 模型。
报告覆盖范围与收敛由[状态与对账](state-reconciliation.md)说明。

## 交易品种分离中立身份与交易所身份

`InstrumentID` 是 runtime key：由 `Venue`、规范 `Symbol` 和产品 `Kind` 组成。
它绝不包含交易所的整数 asset index 或备用 wire symbol。这些形式位于已解析的
`Instrument` 上，由 adapter 通过中立的 `InstrumentProvider` 契约暴露。

一个 `Instrument` 携带：

- 中立的 base、quote 和 settlement currencies；
- adapter 自己管理的 `VenueSymbol` 和可选 `AssetIndex` routing identity；
- 精确十进制的 price tick、size step、minimum quantity、minimum notional 和
  contract multiplier；
- 派生的 price precision，以及中立的 net-only 或 hedge-capable position-mode
  capability。

Runtime 使用这些字段进行精确计算和通用策略判断；它不会重新解释交易所 symbol
规则。Prices、quantities、multipliers、balances 和 PnL 使用
`shopspring/decimal`；只有在无法避免的 wire conversion 边界才使用 floating
point。

## 精确的 `AccountState` 快照

每个 account adapter 都必须实现 `AccountClient.AccountState`。该快照只包含以下
账户域：

| 字段组 | 字段 |
| --- | --- |
| 身份 | `AccountID`, `Venue`, `Type`, `BaseCurrency` |
| 余额 | `Balances[]` |
| 保证金要求 | `Margins[]` |
| 可选汇总 | `Summary` |
| 报告与事件身份 | `Reported`, `EventID` |
| 时间 | `TsEvent`, `TsInit` |

`Type` 是粗粒度中立账户类型 `Cash` 或 `Margin`；`Unknown` 不是有效的权威状态。
每个 `AccountBalance` 包含 `AccountID`、currency、total、free、locked、borrowed、
interest 和 update time。每个 `MarginBalance` 包含 currency、可选 instrument
scope、initial requirement、maintenance requirement 和 update time。可选 summary
包含 settlement currency、equity、available collateral 和 update time。

Orders 和 positions 有意不嵌入 `AccountState`。Order、fill 与 position reports
是类型化的 execution-report domains。Adapter 声明支持时，
`AccountClient.Positions` snapshot 可以提供 scoped position evidence，但对账会
把该证据转换到同一条 position-report comparison path，而不会把它视为账户
快照的一部分。

## 持仓与余额

中立 `Position` 具有 account 和 instrument identity、position side、signed
quantity、entry 和 mark prices、unrealized PnL、leverage 以及 update time。
正数量表示 long，负数量表示 short；当两条 hedge-mode legs 可以共存时，
`PositionSide` 会保持二者独立。

对于 cash balances，在没有 borrowing 或 interest 时，模型可以校验
`total = free + locked`。Margin-account 的 `Free` 可以表示 free margin，因此
不会对所有账户统一施加该 cash invariant。

## 账户身份与 readiness

真实交易所的 execution 与 account clients 通过 `contract.AccountIDProvider`
暴露其逻辑账户。二者都配置时，ID 必须一致；显式期望的 runtime account ID 也
必须匹配。空的或冲突的 adapter identities 会在启动或对账前失败。

`AccountState.ValidateTradingReady` 要求快照有效、`Reported=true`、event ID
非空、event 与 initialization timestamps 存在、stale threshold 为正，并且
account-state 或 reconciliation time 仍然新鲜。Generic risk 可以对增加风险的
订单要求这种 authoritative readiness；详细风险策略由
[执行与风险](execution-risk.md)负责。

## 账户模式属于 adapter

交易所账户模式、账户角色、保证金模式和持仓模式不会成为 runtime 分支。Adapter
发现并校验 unified/classic accounts、owner/agent roles、cross/isolated margin
以及 one-way/hedge positions 等形式，并在配置形态不受支持时 fail closed。
`AccountState.Type` 仍是粗粒度中立的 cash-or-margin 结果，而不是每种交易所模式
的副本。

可移植的 `AccountClient` 契约仍暴露 `SetLeverage` 和 `SetMarginMode`。它们是
中立命令，不是 discovery mechanisms。每个 adapter 要么实现交易所映射，要么
返回 `contract.ErrNotSupported`；必须按交易所和产品分别检查支持情况。

在依赖账户或产品模式之前，请查看[能力矩阵](../adapter-capabilities.md)、
[配置](../reference/configuration.md)和
[不支持（Unsupported）与延期（Deferred）功能](../reference/unsupported.md)。
