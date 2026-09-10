# 个人交易平台改造：第一批实施记录

> 历史实施记录：本地套利应用已按用户要求删除；涉及套利的实现和测试不再保留。当前范围与迁移说明见[删除记录](remove-arbitrage-2026-09-10.md)。

本批落实架构审查 F01–F04 的核心修复，建立后续执行与账务改造的基线。项目目标仍是 SDK → exchange → Gateway → 个人交易、套利、监控等应用。没有通过更换目录来代替行为修复。

## 已完成的行为变化

| 问题 | 现在的行为 | 验证 |
| --- | --- | --- |
| F01：订单契约与 HTTP 实现不一致 | 下单契约使用 `runtime_id`，补齐 `leverage`；缺失或不合法的运行时标识在 HTTP 边界返回 400。撤单如实声明无请求体，路由取自持久订单；改单明确返回 501，删除未实现的请求 schema。 | OpenAPI 示例直接进入真实 Handler；逐项移除必填字段和加入旧版本字段必须在访问订单后端前失败。解码结构的所有字段与 schema 属性对照。 |
| F02：已确认订单因反复查询不到而被拒绝 | `AcceptanceObserved` 从持久订单事件中恢复。确认接单以后，后续缺失观察不能清除历史事实；订单保持 uncertain、保留对账任务，账户写入门保持关闭。 | 连续三次重新创建 Coordinator 后对账；覆盖有交易所订单 ID、只有接单历史两种情况；PostgreSQL 断开连接后的各读取路径均保留接单证据。 |
| F03：状态校验与写入之间存在竞态 | `orders.ReduceTransition` 统一校验和合并；内存 Store 在锁内调用，PostgreSQL 在 `SELECT ... FOR UPDATE` 后调用。状态、执行证据和对账任务在同一事务内更新。 | 强制延迟 submitted 写入，让 filled 先提交；旧写入必须失败，不能恢复对账任务。真实数据库中 12 个并发观察的成交证据全部保留，重连后仍一致。 |
| F04：原始成交 ID 被当成全局身份 | 成交与账目去重使用 principal、account、instrument 和原始 ID 的组合；私有成交事件的版本范围也包含 instrument。 | 跨账户、跨品种同号成交各入账一次；同一成交改变数量、产品或归属仍冲突；v2 存量数据升级后回放不会增加持仓或重复记费。 |

F04 采用显式组合身份，保留原始成交 ID，没有把它改成新的全局字符串。这样无需重写历史 execution evidence 或成交指纹，也不会因新旧 ID 共存而重复入账。Gateway 文档明确要求消费者使用 `account_id + instrument_id + fill.fill_id` 去重。产品、lineage 不属于成交身份，防止同一事实换一个策略就再次记账。

## 实际改动边界

- `api/exchange-gateway/v1/openapi.yaml`、`gateway/httpapi` 和 `gateway/contracts`：订单契约与行为回归。
- `gateway/domain`、`gateway/store`、`gateway/order`：历史接单证据及保守对账。
- `services/trading/orders`、`services/trading/store/postgres`：原子状态转换与证据合并。
- `services/trading/portfolio`、`services/trading/ledger`、`gateway/event`：成交范围与账务去重。
- `AGENTS.md` 和中英文 Gateway 指南：修正已经不存在的目录、错误的调用方限制、过期下单示例。

修正了若干过期测试夹具：HTTP 成功路径必须携带实际要求的运行时标识；Gateway 数据库账户夹具必须使用当前 runtime_id；两条“查回原订单”的测试改为返回同一个交易所订单 ID，避免原测试实际靠查不到后的拒单分支通过。

仓库原有未提交改动保留。原架构审查和隔离探针作为审查时点的证据保留，新回归测试已经进入正式包测试。

## 数据库升级与回退

新增 Trading 迁移 `003_scoped_accounting_identity.sql`。只扩大 `portfolio_fills`、`ledger_entries` 的主键范围，不改已有行的 ID、金额、成交指纹、持仓或发生时间。Gateway 的接单标记直接读取已有事件，不增加数据库列，也不重写历史。

部署顺序：暂停 Trading 写入并保留可恢复备份，使用本版本的 `cmd/trading-migrate` 对明确选定的 Trading 数据库执行迁移，确认 schemaVersion 为 3，再启动本版本 Trading。独立迁移程序已经检查连接的数据库和角色身份。修改主键需要表锁，应在停止写入的窗口执行。

新旧 Trading 写入二进制不能混跑：旧代码的 `ON CONFLICT (principal_id, fill_id)` / `ON CONFLICT (principal_id, entry_id)` 不再匹配新约束。新代码同样要求先完成迁移，不能只更新服务二进制。

回退前需检查新模型是否已接纳旧唯一键无法容纳的数据：

```sql
SELECT principal_id, fill_id, count(*)
FROM portfolio_fills
GROUP BY principal_id, fill_id HAVING count(*) > 1;

SELECT principal_id, entry_id, count(*)
FROM ledger_entries
GROUP BY principal_id, entry_id HAVING count(*) > 1;
```

任一查询返回行，都不能直接收窄主键；这些是合法的不同账户/品种事实，不能删除或合并。应向前修复，或在明确处理升级后新交易的情况下恢复数据库备份与配套二进制。即使查询为空，也必须同步处理迁移版本记录和二进制，不能只手动改约束。

本次只在 `/tmp` 下新建的 PostgreSQL 16 实例中执行迁移和验证，仅使用 Unix socket。没有迁移项目运行中的数据库，没有部署服务，也没有执行交易所认证或实盘读写。

## 验证结果

- 完整 `go test -short ./...` 通过，101 个 package 报告 ok，包含缓存结果。
- 相关 `-race` 回归通过：订单状态并发、成交证据合并、成交和账目作用域、重复缺失后的恢复、私有成交事件版本。
- 7 项真实 PostgreSQL 测试通过：本批 4 项新测试及 3 项已有账本/归属恢复回归。包括多连接并发、v2 → v3 迁移、存量回放、跨范围资金费替换、关闭连接后的恢复。
- `git diff --check` 通过。

完整 short suite 默认仍会跳过没有显式测试 DSN 的 PostgreSQL 集成测试；不能把它当成全部数据库测试均已执行。这里的 7 项验证使用临时实例显式运行。没有执行全部旧 PostgreSQL 集成测试、进程 kill 故障矩阵或实盘验收。前端未发生本批行为改动，没有重复执行前端构建。

日志保存在 `output/architecture-review-2026-09-10/implementation-*.log`。

## 已确认的套利处置原则

2026-09-10 用户确认：一条腿成交、另一条失败时，停止后续下单，保留真实敞口并告警，由用户决定补单或平仓。系统不得自行产生补偿订单。

这是一条后续实现约束，**本批尚未交付套利停止/告警工作流**。后续恢复工作应区分读取事实与交易动作：查询、补齐成交、修复投影可自动进行；补开或平仓需要明确的用户意图及独立幂等身份。

## 下一批依赖与验收

后续更新：独立预留和通用命令写入口已删除，手动提交已收进应用服务；见[下单边界简化](trading-submission-2026-09-10.md)。F09 改用删除无实际资源约束的预留实现处理，当前 schema 为 7、HTTP 契约为 v7；下述计划保留历史上下文。

最新进展：F08 原币种费用和净库存改造见 [第四批实施记录](fee-accounting-2026-09-10.md)，该批次 schemaVersion 为 **6**、HTTP 契约标识为 **v5**。F06 套利实现现已删除。以下 v3/v4 说明保留对应批次的历史范围，发布当前代码应遵守最新记录。

更新：F05 单笔命令执行与恢复已在第二批实施，详见 [命令执行与恢复](command-execution-recovery-2026-09-10.md)。该批新增迁移 004，当前目标版本为 4；下列条目保留第一批结束时的依赖与验收描述。

1. **F05：命令创建、交接与恢复。** command 和持久任务同事务保存；为丢失响应但 Gateway 已接单的场景提供按原幂等身份只读查询。当前 `Client.Reconcile` 在缺少 Gateway ID 且不是 uncertain 时会走 `Handoff`，不能简单给 accepted 命令补一个定时任务就算修复，否则恢复可能变成下单。必须先拆开恢复查询与派发协议，覆盖创建后、发出后、响应后、提交前的崩溃窗口。
3. **F08/F09/F10：单笔交易配套。** 支持费用返佣和多币种现金流，预留消费/释放/过期具备原子约束，接通真正使用的限流器并保留类型化 HTTP/交易所错误。
5. **公共能力与监控。** 在上述基础上稳定公开市场 API、客户端、订阅质量与有限历史；再实现窗口规则、checkpoint 和通知 outbox。

尚有两项必须保留的证据边界：本批未修复历史上已经被错误写成 rejected 的订单，不能凭当前缺失信息自动翻转；从未观察到接单的 dispatching/uncertain 命令仍依赖现有 adapter 对 authoritative not_found 的保证，查询覆盖范围和交易所历史保留窗口需要后续加强。
