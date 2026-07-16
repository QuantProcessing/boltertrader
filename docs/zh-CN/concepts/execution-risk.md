# 执行与风险

> 英文为规范版本。[English](../../concepts/execution-risk.md)

本页定义交易所中立的提交顺序、本地风险预留和执行结果语义。交易所专用支持
仍以[能力矩阵](../adapter-capabilities.md)为准；对账细节由
[状态与对账](state-reconciliation.md)说明。

## 通用提交顺序

进入面向交易所的路径前，runtime 会解析账户和客户端身份、拒绝重复 client
ID、检查生命周期命令 gate，并确认执行能力声明了 Submit。随后执行同一套
共享顺序：

1. 调用 adapter 无副作用的 `ValidateSubmit`。
2. 如果已配置，调用交易所中立的 `SubmissionRiskChecker`，并保留其敞口释放
   closure。
3. 创建并持久追加命令意图，同时以同一身份跟踪 in-flight 状态。
4. 将归一化订单以 `PendingNew` 插入 cache。
5. 对执行客户端的普通 `Submit` 只调用一次。
6. 分类结果，并把命令结果连同任何权威订单更新一起提交。
7. engine 调用返回时释放本地敞口预留。

Runtime 会在创建意图前后检查 context cancellation。意图尚不存在时，取消会
直接返回，不留下持久命令状态。意图记录后，runtime 会追加一个本地结果，且
仍不会调用 `Submit`。Journal 或 cache 失败也会在交易所边界之前停止。

校验与普通提交之间不存在 adapter hook。特别是，runtime 不存在
`SubmitPrepared`、venue pre-trade lease 或本地 venue-capacity admission
路径。Adapter 可以在它唯一的 `Submit` 实现内部私下完成所需签名或准备；这
不会改变 runtime 协议。

## 风险预留的含义

`CheckSubmission` 在同一把锁下评估通用策略，并在接受后返回幂等的释放函数。
并发检查会计入已经持有的预留，从而填补 `PendingNew` 订单在 cache 中可见之前
的空档。Execution engine 会立即 defer 释放，因此敞口占用通常覆盖意图持久化、
pending 状态插入、交易所调用与结果提交，并且在此后的每条错误路径上仍会释放。

内置 checker 可以执行：

- kill switch 和正数量检查；
- 有界的重复 client-ID 保留；
- 调用者配置的 `MaxOrderQty`、`MaxOrderNotional` 和 `MaxPositionQty`；
- 来自归一化元数据的交易品种最小数量与最小名义价值；
- 对 Spot cash 的新鲜、已入金现金余额保护，包括工作中订单和并发预留订单；
- 针对增加衍生品风险的订单，检查已配置的产品/账户来源和新鲜的权威保证金
  账户状态。

释放敞口不会擦除有界的 client-ID 去重历史。该历史独立于短期敞口占用来保护
幂等性。

## 本地策略不等于交易所容量

通用数量、名义价值和持仓限额是调用者选择的安全策略。交易品种最小值是静态
请求约束。Spot cash 检查保护已知的本地库存。它们都不声称能够预测交易所
流动性、rate limit、统一保证金容量或最终是否接受。

对于保证金账户，启用相应策略时 runtime 要求权威 readiness，但把可用容量
交给交易所判断。Adapter 只校验它能在本地证明且无副作用的请求缺陷；服务端
仍是接受或拒绝的权威。

## 确定性结果与歧义交接

| 结果 | 证据 | Runtime 处理 |
| --- | --- | --- |
| 交易所边界前的本地拒绝或不支持（unsupported）命令 | 交易所调用前失败 | 不写入交易所；如果已经记录本地意图，则关闭该意图 |
| 已确认接受 | 无错误且订单响应不是 rejected | 提交返回的订单并解决 in-flight 命令 |
| 交易所明确拒绝 | 显式 venue-rejection error，或无错误但响应携带 `Rejected`/`Expired` 订单 | 提交 rejected 订单并解决 in-flight 命令 |
| 边界后的防御性 unsupported | 被调用客户端在交易所边界后返回 `contract.ErrNotSupported` | 以 `Sent=true` 记录 `OutcomeUnsupported`，提交 rejected terminal 订单并解决意图，而不是把它保留为 ambiguous |
| Ambiguous | 交易所边界后的任何其他错误，包括 timeout、cancellation、disconnect 或未分类错误 | 保持意图 in flight 并保留 `PendingNew`，以及任何可信的 venue-order alias |

边界后返回错误，并不会仅因为“有错误”就被视为 rejection。上表明确的
`ErrNotSupported` 分支是防御性分类器，用于处理违反已声明/已校验操作边界的
客户端；其他边界后失败可能意味着交易所已经接受写入。因此 Ambiguous 状态
应交给权威订单/成交对账处理，而不是允许使用新身份重试。只有范围明确的证据
才能在之后确认接受或明确的否定结果。

运维处置见[运维与恢复](../guides/operations-recovery.md)。策略调用者还应阅读
[编写策略](../guides/strategies.md)和
[不支持（Unsupported）与延期（Deferred）功能](../reference/unsupported.md)。
