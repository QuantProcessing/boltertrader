# 移除本地资金费套利应用

按用户要求移除本地套利的全部后端和前端实现。当前 Trading 应用保留手动交易、命令恢复、预留、持仓与账务；SDK → exchange → Gateway 的通用交易所能力继续供应用使用。

## 删除范围

- 套利执行服务、双腿计划、恢复与停止告警、预览封签、机会扫描和组合持仓投影。
- Trading 中仅为套利服务的行情缓存、Gateway 市场流客户端、后台行情订阅和专用观测指标。
- `/api/v1/products/arbitrage/*`、Trading 的 `/api/v1/realtime/ws` 及对应 Auth Edge 转发。
- 前端策略页面、菜单、旧地址跳转、对冲按钮、机会列表、历史对比图、双腿下单票、组合列表、查询缓存、API 类型、Mock 与文案。
- 预览签名密钥配置与生成流程、账户新建时的套利权限、`arbitrage_top` 热订阅理由。
- 专属测试、设计说明和截图；通用文档同步当前结构。早期架构与账务实施记录标记为历史范围。

未新增替代策略框架、空执行器或预留入口。通用 Mock 的未知路径现在返回 404，避免删除后的地址仍得到伪造成功响应。

## 保留的能力与边界

- SDK、exchange、Gateway 的资金费率、历史、资金流水、行情和订单能力。
- `/funding` 公共资金费率看板；Gateway 的市场与账户 WebSocket。
- 手动交易、成交入账、原币种费用和 PnL、账户真实持仓。
- 通用命令与预留接口拒绝新建 `productId=arbitrage`。手动对账从存储重新读取命令归属，拒绝处理其他产品的命令，包括拒单结果。

## 数据迁移

Trading 目标 schemaVersion 为 **7**。保留迁移 005 的原始内容与校验和，新增 `007_retire_arbitrage.sql`：

1. 删除套利专属 `arbitrage_executions` 表。
2. 删除旧套利命令对应的应用恢复任务。
3. 释放尚未关联任何命令的套利预留。已关联命令的预留及 consumed 记录保持原样。

共享 `commands`、`portfolio_fills`、`positions`、`ledger_entries`、`unrealized_pnl` 的历史记录不删除、不重写。旧套利命令可能仍显示 submitted 或 uncertain，这表示保留的历史事实；退役不等于撤单、结算或平仓。Gateway 对真实订单的管理仍独立存在。

HTTP 契约标识更新为 **v6**，旧套利 API 不再存在。更新运行环境时应配套替换 Trading、Auth Edge 和前端，并通过现有迁移入口升级 schema；没有在本次操作中连接或迁移业务数据库，也没有执行交易所订单操作。

## 验证

- 全量无凭据 Go 回归：`go test -short ./...`，99 个有测试的包通过。
- 执行、恢复、HTTP、代理、实时协议和 hotset 共 8 个包的竞态检查通过。
- 前端 29 个文件、191 项单测及生产构建通过。
- 隔离 PostgreSQL 上 18 项存储测试通过：覆盖从 schema 6 升至 7、重复迁移、保留历史账目、仅释放未使用预留、保留手动恢复任务。
- 专门验证旧 API 无法注册或转发、旧订阅被拒绝、Mock 对旧路径返回 404，以及手动对账不接管其他产品。

验证日志在 `output/architecture-review-2026-09-10/remove-arbitrage-*.log`。临时数据库测试后已停止；未进行生产交易所验收。
