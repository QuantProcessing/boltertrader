# U1：简化账户的跨层验收

本批完成[更新计划](update-plan-2026-09-10.md)中的 U1。账户实现沿用已确认的
登录、添加并验证、查看、删除流程，本批补齐真实数据库与 HTTP 链路的验收。

## 本批改动

- 新增 `gateway/integration/account_stack_postgres_test.go`：使用真实 Auth Edge
  登录会话、代理、Gateway handler、账户服务、Manager、Trading Gateway 客户端和
  当前持仓投影。Auth Edge 与 Gateway 使用不同 PostgreSQL schema。
- 交易所使用无凭证假客户端，不构造 SDK；账户 Manager 的 HTTP transport 拒绝联网。
- `make test-gateway-postgres` 和 `make test-compose-postgres` 加入该跨层用例，并修正
  账户存储测试名称过滤，确保新的 `Registration` 用例实际执行。
- 修复部分数据库测试没有遵守 `-short` 的问题。Gateway、Trading、Auth Edge 的数据库
  集成辅助函数以及数据库权限/进程崩溃测试均先检查短模式，再读取启用配置。
- 更新中英文测试说明，区分本地 Go 夹具与读取公共实时行情的浏览器 mock。

## 覆盖行为

| 链路 | 验证结果 |
| --- | --- |
| 登录与 CSRF | 未登录请求返回 401；缺少 CSRF 的添加返回 403；均不调用交易所余额接口 |
| 添加与错误 | 零余额可添加；交易所权限错误保留安全错误码且不落库；相同添加请求重放不再次验证余额 |
| 当前持仓 | 交易所持仓变化直接反映；读取失败标为不可用；真实零持仓显示正常空仓 |
| 组件恢复 | 重建 Gateway、Auth Edge 会话服务及其 SQL 连接后，原登录会话继续可用；账户从密文恢复，持仓读取当前交易所状态 |
| 删除 | 有持仓时可删除；账户、凭证与路由消失；添加、查询、删除过程中没有任何交易所下单或撤单 |
| 存储证据 | 凭证加密往返；删除后已有订单、订单事件和幂等身份保留，旧请求不会创建第二笔订单 |
| 注销与脱敏 | 注销后不能访问账户；HTTP 响应不包含夹具中的凭证、密码或服务 token |

关闭后的旧路由拒绝调用由已有账户回归覆盖。跨层测试里的 Trading 命令存储使用内存
实现，因为本用例只读取交易所当前持仓；不把它算作 Trading 账务数据库集成验收。

## 已运行的验证

全部通过：

1. PostgreSQL 存储测试 21 项，含迁移、并发幂等、状态转换、写入锁和账户删除证据。
2. 新账户跨层用例及 `-race` 检查。
3. 订单真实子进程崩溃测试的五个窗口：prepared 后、dispatching 后、等待交易所期间
   强杀、收到交易所结果后、结果提交后。交易所仍是本地 HTTP 假服务。
4. 设置无效数据库连接配置后执行 `go test -short ./...`，96 个有测试的包通过。
   这验证默认短测不会因启用环境变量而尝试连接数据库。
5. 实际执行更新后的 `make test-gateway-postgres test-gateway-restart`，三个测试命令
   全部通过。

数据库验收命令需要调用者提供隔离测试库的 `GATEWAY_TEST_POSTGRES_DSN`：

```sh
make test-gateway-postgres test-gateway-restart
go test -race ./gateway/integration -run '^TestAccountStackRealPostgres' -count=1
```

默认测试隔离检查：

```sh
GATEWAY_TEST_POSTGRES_DSN=invalid BT_TEST_POSTGRES_DSN=invalid BT_G010_POSTGRES_ACCEPT=1 \
  go test -short ./...
```

日志保存在 `output/architecture-review-2026-09-10/`：

- `u1-postgres-tests.log`
- `u1-account-stack-race.log`
- `u1-order-crash.log`
- `u1-short-tests.log`
- `u1-make-acceptance.log`

## 实例范围与剩余验收

本次使用新建的 `/tmp/bolter-u1-postgres.vSgr3R/data` PostgreSQL 实例和 `bolter_u1`
测试数据库，只开放本地 Unix socket。测试使用独立 schema 并清理；验收结束后实例已
关闭。未迁移或操作用户业务数据库。

本批没有 UI 修改，未重跑浏览器与前端构建；没有运行 Compose、真实交易所凭证测试或
实际交易。组件重建的 HTTP 集成测试不等于整套部署的进程恢复验收。更广泛的进程退出、
数据库故障、公开行情持续运行及单机部署验收仍在 U6。
