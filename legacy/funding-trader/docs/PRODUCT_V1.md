# 自费资金费率套利终端：V1 产品规格

> 文档状态：当前实现规格
>
> 实现边界：Go 后端、PostgreSQL 读模型、已接入真实 API/SSE 的前端、BolterTrader Gateway 集成
>
> 相关文档：[API V1 契约](./API_V1.md)、[运行手册](./OPERATIONS.md)、[后端运行说明](../backend/README.md)、[Test-mode Gateway 验收记录](./TEST_MODE_GATEWAY_ACCEPTANCE.md)

## 1. 产品定位

V1 是一个私有、单用户、认证后的资金费率套利操作终端。它面向同一操作者在两个中心化交易所之间发现资金费率机会、检查执行前提、手动提交双腿 open/close 请求，并查看持仓、活动、审计和实时状态。

funding-trader 是独立后端，不是 Gateway 的浏览器外壳。它负责会话认证、读模型、机会和预警展示、服务端执行预览、幂等执行意图、执行状态投影、审计事件和浏览器 SSE。BolterTrader Gateway 始终是交易所账户、交易所凭证和私有交易写入的唯一所有者。

## 2. V1 范围

### 已实现

- 通过 `FT_BOOTSTRAP_EMAIL` 和 `FT_BOOTSTRAP_PASSWORD` 引导唯一默认 principal。
- Cookie 会话认证、CSRF 防护、登录节流、退出和认证审计。
- 按当前后端 `FT_MODE` 读取 `test` 或 `live` 作用域数据；Test mode 统一 Gateway-native Demo 与 Testnet。
- 机会、资金费率预警、持仓、活动、审计事件和系统 readiness 的认证只读视图。
- 浏览器 SSE：初始 `snapshot`、增量 upsert/append、heartbeat、`resnapshot.required` 后重取快照。
- 手动两步执行：先创建服务端预览，再用显式 `Idempotency-Key` 提交 open 或 close。
- 后端执行状态机：两腿下单、成交观察、部分成交、撤销不安全余量、补偿、恢复和人工介入状态。
- Prometheus 指标：安全关键执行状态和 resnapshot 事件计数。
- Live 可配置为只读市场数据路径；真实私有写入默认拒绝。

### 未实现

- 公开注册、找回密码、邮箱验证、SSO、钱包登录。
- 多用户 UI、角色模型、管理员后台、用户启用/禁用或密码重置。
- 浏览器或 funding-trader 后端中的交易所 API Key、Secret、Passphrase 管理。
- 交易所连接管理页面、凭证添加/替换/校验/删除接口。
- 自动开仓、自动平仓、自动再平衡、策略托管或收益保证。
- 充值、提现、资金划转、跨交易所调拨。
- 由浏览器选择 `test`/`live` mode、Gateway-native `venueEnvironment` 或绕过后端写入门。

## 3. 身份、会话与权限

V1 当前只有一个 bootstrap principal。登录成功后，后端设置 `ft_session` HttpOnly Cookie，并在响应中返回 CSRF token。所有业务视图和写入请求都基于该 session；除登录外，浏览器不能匿名访问 V1 数据。

当前产品不区分 admin、trader、viewer。未来多用户可以沿用当前 `principalId + mode`
作用域和审计模型扩展，但这只是非实现展望，不是 V1 可用能力。

## 4. Gateway 边界

Gateway 独占以下职责：

- 保存和使用交易所 API Key、Secret、Passphrase；
- 发现当前 mode 允许范围内可用账户、场所、永续合约和能力；
- 执行交易所私有写入，包括下单、撤单、订单查询、成交、余额、账户、仓位、资金费率和私有事件流；
- 判断账户是否 read-ready、write-ready。

funding-trader 和浏览器不能接收、保存、校验、删除或展示交易所凭证。V1 中任何需要交易所私有能力的动作，都必须通过后端调用 Gateway，且后端只暴露安全处理后的状态、错误和审计结果。

## 5. 主要界面

### 登录页

- 输入 bootstrap 邮箱和密码。
- 登录失败返回通用错误；过多失败会触发登录节流。
- 登录成功进入认证工作台。
- 不提供注册、用户申请或凭证配置入口。

### 工作台

工作台以高密度操作界面呈现以下区域：

- 顶部导航：机会、持仓、执行、活动、审计。
- 系统状态：当前 mode、readiness、SSE 状态、build、当前 principal。
- 资金费率预警：按 `all`、`critical`、`warning`、`watch` 过滤。
- 机会列表：按净 APR 排序，可按做空场所过滤，点击查看后端详情。
- 执行工作台：展示选中机会、后端写入门、资金、杠杆、服务端预览和提交结果。
- 持仓：展示当前 open/attention 持仓，并在写入门就绪时允许手动平仓。
- 执行状态：展示 open/close execution 的状态、腿、成交、阻断和人工介入信息。
- 活动与审计：展示业务活动和安全审计事件。

前端不会补算缺失的收益、强平、资金费或计划腿。提交判断、手续费、滑点、计划腿和 blocker/warning 均以后端预览为准。

## 6. 读模型

V1 读模型来自 PostgreSQL 投影，并通过 [API V1 契约](./API_V1.md) 暴露：

| 视图 | API | 用途 |
| --- | --- | --- |
| 机会 | `GET /api/v1/opportunities`、`GET /api/v1/opportunities/{id}` | 查看双腿套利路径、净 APR、稳定性、容量、费率状态和结算时间 |
| 预警 | `GET /api/v1/funding-rate-alerts` | 查看单一市场资金费率异常，不代表可执行套利 |
| 持仓 | `GET /api/v1/positions` | 查看 open/attention 持仓和后续 close 入口 |
| 活动 | `GET /api/v1/activities` | 查看业务闭环记录 |
| 审计 | `GET /api/v1/audit-events` | 查看认证、CSRF、会话和执行相关审计 |
| readiness | `GET /api/v1/system/readiness`、`GET /health/ready` | 查看 database、gateway、publicData、accounts、execution、twoLegWrite 等组件 |
| SSE | `GET /api/v1/stream` | 将快照和增量事件推送到浏览器 |

## 7. 执行流程

V1 是手动执行，不自动交易。

1. 操作者选择机会。
2. 前端读取 readiness 中的两腿写入门；未就绪时禁用执行。
3. 操作者输入资金和杠杆。当前前端限制资金为 `$100-$10,000`，杠杆为 `1x` 或 `2x`。
4. 前端调用 `POST /api/v1/execution-previews`。
5. 后端基于最新 read/execution scope 生成服务端预览，返回 `previewId`、`snapshotId`、计划腿、费用、滑点、7 日净收益、blockers、warnings 和 `canSubmit`。
6. 操作者确认后，前端用显式 `Idempotency-Key` 调用 `POST /api/v1/orders`，只提交 `operationKind=open`。
7. 后端按长期稳定的命令哈希和幂等记录处理重复请求。同 key 同请求返回已保存结果；同 key 不同请求返回冲突。
8. 平仓由持仓行手动触发，调用 `POST /api/v1/positions/{positionId}/close`，同样需要 CSRF 和显式幂等键。

执行服务会依次处理两腿，观察 Gateway 订单和成交。如果出现部分成交、只成交一腿、撤单不确定或 Gateway 结果不确定，状态机会进入 `partiallyHedged`、`compensating`、`uncertain` 或 `manualInterventionRequired`，并保留恢复/补偿证据供操作者处理。

## 8. 风险与写入门

服务端预览负责最终执行检查。硬阻断会使 `canSubmit=false`；警告不会自动阻断，但必须在预览中可见。

核心硬阻断包括：

- 资金小于 100 USDT 或超过可用资金；
- 杠杆不是 `1` 或 `2`；
- 市场数据超过 3 秒或时间倒挂；
- 风险、账户或规则不可验证；
- open 的预计 7 日净收益不为正；
- 数量无法满足最小数量、最小名义或步进规则；
- 容量占用超过 80%；
- open 的最低强平缓冲低于 30%；
- close 的当前仓位不可验证或平仓数量超过当前敞口；
- 继承自机会、账户、Gateway 或 readiness 的原始 blocker。

核心警告包括：

- 容量占用超过 50%；
- open 的最低强平缓冲低于 50%；
- 30 日稳定性低于 80%；
- 近期资金费历史反向。

## 9. 模式策略

`FT_MODE` 只能是 `test` 或 `live`。后端使用当前配置的 mode 建立读写作用域，
不接受浏览器请求覆盖。API 层会拒绝带 `environment` 或 `mode` 查询参数的
`/api/v1/*` 请求。

Test mode 统一 Gateway-native Demo 与 Testnet。Gateway 返回的
`venueEnvironment` 仍作为账户、instrument、执行 leg、fill 和 funding history 的
合格身份保留；它不是产品 mode，也不能由浏览器选择。

Test-mode 的真实 open 不是默认假成功。它必须先通过发现驱动的两腿能力门：
Gateway 在 Test mode 允许的 native `venueEnvironment` 中发现至少两个不同场所的
read-ready、write-ready 永续账户，并且两者都具备 place、cancel、orders、fills、
balances、account、positions、funding 和 private-stream 能力；两条腿还必须使用相同
settlement asset，并具备实际 account funding payment 读取能力。Gateway V1 不提供
标准化风险限额；资金规模和杠杆由当前单用户自行控制，`risk_unverifiable` 只作为
preview 警告展示，不阻断提交。余额、交易规则、容量、滑点、强平缓冲和原始 blocker
仍按服务端真实证据校验。当前验收结果和复现命令记录在
[Test-mode Gateway 验收记录](./TEST_MODE_GATEWAY_ACCEPTANCE.md)。

close 使用独立的后端 `closeReady` 门，只能对可解析的既有头寸执行 reduce-only，且要求
权威私有流或 resnapshot 新鲜、执行服务允许 close、Gateway 具备对应写能力。它不要求
重新通过新开仓两场所门，因此 open 被阻断时仍可安全减仓；浏览器状态不能绕过该后端门。

Live 支持配置为只读市场数据路径。私有写入默认拒绝：
`FT_PRODUCTION_WRITES_ENABLED=false` 时，后端必须 fail-closed。即使显式打开写入，
仍需满足 secure cookie、Gateway 账户能力及对应的 operation-specific readiness；open
继续要求两腿验收，close 还要求显式 Live emergency reduce-only 批准。Live 与 Test 使用
同一执行代码路径；Live 额外保持 default-deny，并且当前验收不接受任何 Live order。

## 10. 运维与可观测性

运行、切换和回滚以 [运行手册](./OPERATIONS.md) 与
[后端运行说明](../backend/README.md) 为准。产品层面的运维要求：

- Secret 通过 `FT_GATEWAY_TOKEN` 或更推荐的 `FT_GATEWAY_TOKEN_FILE` 注入，不能出现在浏览器、日志或文档命令行示例中。
- `FT_MARKET_SYMBOLS` 定义后端资金费率市场集合，默认 `BTC-USDT,ETH-USDT,SOL-USDT`。
- `/api/v1/system/readiness` 必须清楚区分 database、gateway、publicData、accounts、execution 和 two-leg write 状态。
- `/metrics` 需要认证，且不得带 principal、mode、venueEnvironment、account、token 或 secret 标签。
- SSE 断线后前端应按 cursor 恢复；收到 `resnapshot.required` 时重取后端快照。

## 11. V1 验收

V1 验收以当前实现和文档化命令为准：

- API 路由、认证、CSRF、错误响应、分页、SSE 和执行契约符合 [API V1 契约](./API_V1.md)。
- 后端配置、Gateway public market data、readiness、Live default-deny writes 符合 [后端运行说明](../backend/README.md)。
- Test-mode Gateway 账户发现、公开数据、SSE、读模型、指标和两腿写入门符合 [验收记录](./TEST_MODE_GATEWAY_ACCEPTANCE.md)。
- 浏览器没有交易所凭证 UI，也没有 admin/user-management UI。
- 真实 open/close 只有在 Gateway 发现门满足时才允许；当前 Test-mode 记录中的阻断结果必须保留为 fail-closed，而不是用伪造响应绕过。

## 12. 后续展望

后续版本可以在不改变 V1 安全边界的前提下扩展：

- 多用户：沿用 `principalId + mode` 作用域，增加角色、邀请、禁用、审计和管理员 UI。
- 凭证：仍由 Gateway 独占，funding-trader 只展示 Gateway 返回的账户能力和 readiness。
- 执行：增加人工恢复操作、批量 close、再平衡和更细的执行 runbook。
- 风险：增加账户余额、保证金模式、仓位模式、真实手续费档位和更严格的场所规则解释。

这些都是非 V1 能力，不能写入当前 UI 或 API 验收标准。
