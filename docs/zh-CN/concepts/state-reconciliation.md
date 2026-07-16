# 状态与对账

> 英文为规范版本。[English](../../concepts/state-reconciliation.md)

本页定义 runtime 状态如何与交易所权威证据收敛，包括覆盖范围、来源、成交去重、
启动、重连与激活。账户快照的精确形态由
[账户与交易品种](accounts-instruments.md)说明。

## 权威输入保持类型化

对账消费彼此独立、类型化的中立来源：

- `AccountClient.AccountState` 提供强制账户快照，用于余额、保证金摘要、身份和
  新鲜度；
- 配置 execution client 时，`ExecutionClient.GenerateExecutionMassStatus`
  提供订单、成交和持仓报告域；
- capability-advertised 的 `AccountClient.Positions` 可以被归一化到同一条持仓
  报告比较路径。当 execution client 不拥有该域时直接使用它；当 execution
  position coverage 既不完整、也没有明确标记为不请求时，将其作为受保护的
  fallback。

持仓 fallback 必须证明账户、交易所、冻结的交易品种 selector、请求时间边界
以及账户能力完全相同。超出范围的 row、重复身份、缺失能力、时钟回退或 failed query（查询失败）
都会保留原 execution coverage 并附带 warning。该 fallback 不会把持仓嵌入
`AccountState`，也不会把账户快照变成 execution-owned mass status。

账户快照的 account ID 必须与 runtime 账户匹配，其 venue 必须与账户客户端的
能力来源匹配。在信任任何报告 row 之前，mass-status 响应会针对精确的 query
venue、account、client filter 和冻结的 instrument selector 进行校验。

## 覆盖范围控制基于缺失的结论

Open orders、fills 和 positions 各自携带独立的 `ReportCoverage` 状态：
`Unknown`、`NotRequested`、`Complete`、`Partial` 或 `Unavailable`。Complete
与 partial 证据还携带精确的 account、client filter、归一化 instrument
selector 和 observation boundary。Fill coverage 还拥有其精确的 `From`/
`Through` 历史区间。

这些域不可互换。Complete open-order coverage 不能证明 fill history 完整，
账户持仓快照也不能扩大 execution report 的冻结范围。Runtime 会接受有效
partial scope 内的正向证据，但只有该身份与该域具有 complete coverage 时，
才会根据“缺失”得出结论。

## 各个域如何收敛

- 有效 `AccountState` 通过规范的 account-cache 应用路径，并更新对账新鲜度。
- 交易所报告的 open orders 会刷新已知订单，并实体化首次在 runtime 外部观察到
  的订单。
- Complete 且冻结的 open-order snapshot 中缺失的 cached open order 会变为
  `Unknown`；对账不会凭空把原因设为 `Canceled` 或 `Filled`。
- 恢复的 fills 走节点的规范 fill path，使 cache、portfolio、terminal-order
  state 和 callbacks 与实时事件保持一致。
- Position reports 是比较证据。它们可以产生阻塞 finding，但 reconciler 不会
  直接覆盖或清除 position cache state。

只有类型化身份一致时，订单和成交证据才能解决 ambiguous in-flight command。
未变化的 open order 可以确认 ambiguous submit，但不能证明 pending cancel 或
modify 已成功。结果分类由[执行与风险](execution-risk.md)负责。

## 来源、身份与成交去重

恢复的 fills 携带 `SourceReconciliation` 以及 snapshot/reconciliation event
flags；从 report data 合成的 fills 会显式标记为 synthetic。Runtime 使用交易所
范围内的身份 `AccountID + InstrumentID + TradeID` 去重成交，不包含 order
aliases，因为这些 alias 可能稍后才被学习到。报告没有 trade ID 时，对账会在
应用前派生稳定的 synthetic ID。

Adapter 重连 snapshot 交付的 fill 会保留其原始 stream source 与 snapshot
provenance。Snapshot flag 不是丢弃该 fill 的理由：它与 live traffic 一样进入
规范 fill path 和交易所范围内的去重，因此重复交付的 snapshot fill 及其随后
到达的 live duplicate 只会应用一次。

Modify 会保留订单原始的逻辑 `ClientID`。Sparse venue response 会从该 logical
request 补全。如果交易所返回不同的 response `ClientID`，runtime 只有在该
identity 尚未被占用时才接受 response，随后会把它归一化回原始 `ClientID`；
runtime 不会注册 alternate client alias。已属于另一个订单的 identity 会 fail
closed，且绝不会重新绑定该 logical order。

Reconciler 保留有界的 completed-fill index，以及下一 cursor window 使用的独立
exact overlap set。Journal-backed state store 会在 full-coverage cursor 前进前
记录 applied-fill dependencies。重启时，replay 会为幂等状态播种，但不会再次
应用业务状态或发出 callback。Identity conflict 和 retention exhaustion 会
fail closed，而不会容许潜在的重复成交。

Incomplete fill coverage 可以应用可信的正向 fills，但不能推进持久的
full-coverage cursor。后续 pass 会有意与上一次成功边界重叠，并依靠去重确保
该重叠安全。

### Journal 保留语义

Physical journal file 保持 append-only。Retention 只限制内存中的 ordinary
replayed diagnostic/history window；recovery-critical records 会额外保留，包括
open 或 ambiguous intents、blocking reports、uncommitted applied events，以及
latest cursor 与其 dependencies。丢弃 ordinary in-memory entry 不会重写或压缩
磁盘文件。Runtime 不会自动压缩磁盘文件，因此不能把 bounded diagnostic memory
理解为 bounded journal file size。

## 启动、重连与 stream gap

`TradingNode.Run` 会 replay open command intents，在节点的 reconciliation 与
event-serialization locks 下执行启动对账，评估 activation verdict，之后才启动
strategy callbacks 和常规 event processing。Reconciliation error 会把节点
置于 failed 状态。

`TradingNode.Reconnect` 调用实现了 `contract.Reconnectable` 的客户端，然后在
恢复交易前对账。内部自行重连的客户端不会被强制走该方法。Private-stream gap
会立即把交易置于 reconciling；恢复会等待每个 active gap generation，运行一次
scoped reconciliation，然后重新评估 activation。如果 execution stream 可能
丢失 fills，但 adapter 没有 authoritative fill-history report，重连恢复会保持
restricted，而不会假定连续性。

## 激活判定

存在 execution evidence 时，激活要求 complete open-order coverage，并要求
fill 与 position coverage 要么 complete，要么明确 not requested。Incomplete
fill cursor continuity 和 blocking findings 也会阻止激活。Diagnostic warnings
与通用 `Partial` summary bit 本身不是权威；最终判定由类型化的 domain coverage
和 findings 决定。

组合方式见[运行 Runtime 节点](../guides/runtime-node.md)，操作员行动见
[运维与恢复](../guides/operations-recovery.md)。Demo/Testnet 证据策略由
[测试](../reference/testing.md)负责。
