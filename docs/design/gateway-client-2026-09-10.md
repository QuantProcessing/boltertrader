# U2：独立 Gateway 客户端与协议

本批完成[更新计划](update-plan-2026-09-10.md)的 U2。新 Go 应用可以通过
`clients/gateway` 使用账户查询、余额、当前持仓、订单与公共行情，不需要依赖 Trading。

## 实现与删除

- `api/exchange-gateway/v1` 增加中立 REST DTO。原 `gateway/realtimewire` 的协议代码
  与测试移动到这里，原目录实现删除；Gateway 改用共享协议。没有另造协议生成框架。
- `clients/gateway` 实现账户摘要与连接、余额、当前持仓快照、订单提交/查询/原键查询/
  撤销/列表、订单成交遍历，以及公共行情目录、K 线、快照和多主题订阅。
- Gateway 下单请求、账户目录、行情响应与错误信封使用共享 DTO。订单响应补充
  `account_id`、`instrument_id`，幂等重放也保留两者；OpenAPI 与冻结契约断言同步。
- 输入有明确校验；响应允许新增字段，拒绝重复 JSON 字段、无效金额、错误身份、未知
  状态和过大数据。保留原币种费用符号；查询失败不返回正常空值。
- 请求有超时和上下文取消；不跟随重定向，不自动重试写请求。禁用带幂等键 POST 的
  transport 自动重放。响应丢失后通过原键只读恢复；404 也不等于允许重发。
- 撤单先读取并核对订单/账户/标的身份，再提交 DELETE，避免错误范围触发交易请求。
- 公共订阅共享一个 socket；按订阅验证 generation 与 sequence，通知缺口、重新连接、
  等待快照。实现心跳、初始快照超时、有界退避、慢消费者终止和订阅释放。
- Trading 账户连接查询使用新客户端；三个应用投影类型移到 `accountpositions`，HTTP
  包只作类型别名。客户端不再导入 `services/trading/httpapi`。
- 旧 Trading 响应解码允许新增字段，兼容当前 Gateway 新增的订单身份字段。用实际
  扩展字段回归替换“必须复制 exchange.Fill 的所有字段”测试。
- 删除没有生产调用方的 Trading WebSocket 心跳辅助代码，心跳随新客户端集中测试。
- 增加 `cmd/gateway-market` 只读示例、`make test-gateway-client`、中英文使用说明和
  客户端/协议包依赖边界检查。

## 已确认的边界

账户添加/删除仍由现有登录与设置流程处理；没有新增角色、权限配置或凭证流程。
普通能力使用 Gateway 服务 token；账户目录仍兼容现有内部监听与服务 token。

客户端不是策略或交易执行器：不计算本地持仓，不补单，不把 HTTP 202 当作成交，
不把连接心跳当作行情新鲜。`prepared`、`dispatching`、`uncertain` 均需继续查询。
价格、数量、费用为字符串；所有计算留给 decimal 消费方。

余额端点当前没有游标。达到 500 行时无法证明完整，新客户端对此报错；U3 迁移旧余额
调用方时补齐服务端完整性契约。成交遍历明确最多 100 页，任何失败丢弃本次部分结果。

## 验证

全部通过：

- `make test-gateway-client`：独立客户端和示例、共享协议、真实 Gateway handler 及
  Market Hub 契约。日志：`output/architecture-review-2026-09-10/u2-client-gate.log`。
- 使用无效 `GATEWAY_TEST_POSTGRES_DSN`、`BT_TEST_POSTGRES_DSN` 和启用的
  `BT_G010_POSTGRES_ACCEPT` 执行 `go test -short ./...`：98 个有测试的包通过。
  日志：`output/architecture-review-2026-09-10/u2-short-tests.log`。
- `go test -race -short ./clients/gateway ./gateway/httpapi ./gateway/order
  ./gateway/integration ./services/trading/gateway ./services/trading/accountpositions`：
  六个包通过。日志：`output/architecture-review-2026-09-10/u2-race-tests.log`。
- `git diff --check` 通过。

回归覆盖：响应丢失后只查询原键、错误账户/订单、兼容新增字段、拒绝重复/尾随 JSON、
过大响应、取消、重定向、凭证错误脱敏、负手续费、分页循环与部分失败、公开行情
命名空间标的 ID、断线重订阅、序号缺口、快照恢复、无声断线、只 pong 不初始化和
慢消费者退出。

测试只使用本地假交易所与 HTTP/WS 夹具。未使用真实凭证、未提交真实交易、未重跑
PostgreSQL/Compose/浏览器验收，也未进行持续公共实时行情验收；这些不计入本批结果。

## U3 剩余工作

本批不是 Trader 整体迁移：其目录、交易证据转换、部分余额/持仓调用仍保留旧适配代码。
U3 会迁移这些调用并删除重复 HTTP 代码，收敛浏览器专用行情路径，删除 `requestedState`
等常量兼容字段，补齐余额完整性契约。独立监控在 U4 使用同一个客户端，不再实例化 SDK。
