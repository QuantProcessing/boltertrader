# funding-trader 运行手册

本文面向单用户私有部署。浏览器只连接 funding-trader 的同源前端，
前端代理到独立 Go 后端；后端连接 PostgreSQL 和 BolterTrader Gateway。
交易所账户、API Key、Secret、签名和交易所私有连接始终只由 Gateway 管理。

## 1. 运行拓扑

```text
Browser
  -> funding-trader frontend /api/*
  -> funding-trader Go backend
       -> PostgreSQL
       -> BolterTrader Gateway
            -> configured Demo/Testnet/Live accounts
```

后端和 Gateway 都是当前的单用户控制面。数据库模型和所有读写 scope 仍携带
`principalId` 与 `mode`，为未来多用户扩展保留隔离边界；V1 不提供注册、
用户管理或浏览器凭证管理。

## 2. 前置条件

- Gateway 已启动，并由操作者配置目标 mode 允许的账户与凭证；
- Go 1.26；
- Node.js 22.13 或更高；
- 本机 PostgreSQL 命令行工具可用：`initdb`、`pg_ctl`、`createdb`、`createuser`；
- Gateway bearer token 存放在权限受限的文件中，不写入仓库、命令输出或浏览器。

当前 Go workspace 会同时使用本仓库的 `backend` 和相邻的
`../BolterTrader`。这是本地联调统一 funding/Gateway 能力的明确边界；
干净环境必须把两个仓库放在相邻目录，或改用已发布的兼容 BolterTrader 模块版本。

## 3. Docker-free 本地启动

本地开发固定使用以下 loopback 端口，且不依赖 Docker：

| 组件 | 地址 | 管理方式 |
| --- | --- | --- |
| PostgreSQL | `127.0.0.1:15432` | 本仓库脚本创建/启动本机 PostgreSQL 数据目录 |
| BolterTrader Gateway | `http://127.0.0.1:18082` | 外部进程；本仓库只检查状态，不启动、不停止 |
| funding-trader backend | `127.0.0.1:18091` | 本仓库脚本构建二进制并直接启动/停止 |
| frontend | `127.0.0.1:13001` | 本仓库脚本直接启动仓库内 Vite 进程 |

脚本默认把二进制、pid、启动时间、命令身份、日志和 PostgreSQL 数据目录放在
`FT_LOCAL_RUNTIME_DIR=/tmp/funding-trader-local` 下，避免在仓库内生成运行态文件。
如需保留在其他位置，显式传入：

```sh
export FT_LOCAL_RUNTIME_DIR=/tmp/funding-trader-local
```

准备操作者自己的后端环境文件，例如 `/tmp/funding-trader-local/backend.env`。
不要把该文件提交到仓库；它必须包含操作者自己的 Gateway token 文件路径和登录
bootstrap 密码：

```sh
FT_MODE=test
FT_STARTUP_MODE=serve
FT_HTTP_ADDR=127.0.0.1:18091
FT_DATABASE_URL=postgres://funding_trader@127.0.0.1:15432/funding_trader?sslmode=disable
FT_GATEWAY_BASE_URL=http://127.0.0.1:18082
FT_GATEWAY_TOKEN_FILE=/absolute/path/to/GATEWAY_AUTH_TOKEN
FT_SESSION_TTL=24h
FT_COOKIE_SECURE=false
FT_PRODUCTION_WRITES_ENABLED=false
FT_MARKET_SYMBOLS=BTC-USDT,ETH-USDT,SOL-USDT
FT_BOOTSTRAP_EMAIL=owner@example.com
FT_BOOTSTRAP_PASSWORD=<operator-owned strong password>
```

`FT_PRODUCTION_WRITES_ENABLED=false` 是本地默认安全边界。脚本按 Go
`strconv.ParseBool` 的真值拼写处理 `true/TRUE/True/1/t/T`；在 `FT_MODE=live`
且该值为真时会失败关闭，除非操作者在同一个 env 文件中显式设置
`FT_LOCAL_ALLOW_LIVE_WRITES=true`。本地只读验收不需要这个开关。

后端 env 只在后端启动子 shell 中加载。Gateway token、bootstrap 密码和其他后端
变量不会进入随后启动的前端进程。`FT_GATEWAY_TOKEN` 与
`FT_GATEWAY_TOKEN_FILE` 必须且只能配置一个；推荐使用权限受限的 token 文件。

检查当前状态：

```sh
scripts/local-dev-stack.sh --backend-env /tmp/funding-trader-local/backend.env status all
```

启动本地 PostgreSQL、后端和前端。Gateway 必须已由操作者作为外部进程在
`127.0.0.1:18082` 启动；脚本不会接管 Gateway：

```sh
scripts/local-dev-stack.sh --backend-env /tmp/funding-trader-local/backend.env start all
```

如果任一目标端口已被非脚本 pid 占用，启动会拒绝继续，避免覆盖当前运行的
PostgreSQL、Gateway、后端或前端。运行目录强制为当前用户私有目录；停止命令要求
pid、启动时间和命令身份全部匹配。PostgreSQL 还要求脚本创建的 PGDATA 标记与
`postmaster.pid` 匹配。旧 pidfile、伪造 pidfile 或外部 PostgreSQL 均不会被停止；
Gateway 始终保持外部进程不变：

```sh
scripts/local-dev-stack.sh stop all
scripts/local-dev-stack.sh status all
```

也可以按服务单独执行：

```sh
scripts/local-dev-stack.sh start postgres
scripts/local-dev-stack.sh --backend-env /tmp/funding-trader-local/backend.env start backend
scripts/local-dev-stack.sh start frontend
scripts/local-dev-stack.sh status gateway
```

传入 `--backend-env` 的 status 会在隔离子 shell 中读取实际 Gateway/backend 地址。
仅对带显式端口的 `127.0.0.1` 或 `localhost` Gateway URL 执行本地监听检查；远程
URL 只报告已配置，避免把远程地址误判成本机端口。

前端通过服务端环境连接后端，浏览器只访问同源 `/api/*`：

```sh
FT_BACKEND_ORIGIN=http://127.0.0.1:18091 npm run dev -- --host 127.0.0.1 --port 13001
```

浏览器不得接收 `FT_GATEWAY_TOKEN`、`FT_GATEWAY_TOKEN_FILE`、交易所 API Key
或任何 Gateway `Authorization` header。

如果你完成 Gateway 外部配置修复，建议直接执行后续验收套件（在仓库内执行）：

```sh
backend/scripts/gateway-acceptance.sh \
  --env /tmp/funding-trader-local/backend.env \
  --evidence-dir /tmp/funding-trader-local/run-$(date +%Y%m%dT%H%M%S)
```

该验收包装器只打开 `RUN_GATEWAY_READ=1`、`RUN_GATEWAY_PUBLIC_READ=1` 和
`RUN_GATEWAY_ACCEPTANCE_GATE=1`，用于真实只读与两腿写入能力门证据；它不会启用
Live 私有写入。

### macOS Docker 卸载核验

Docker Desktop 卸载后，macOS 上可能残留一个受 TCC/系统隐私保护的约 4 KiB
容器 metadata stub。它不是正在运行的 Docker daemon，也不会让本 runbook 重新依赖
Docker。无需删除它；不要尝试用提升权限删除该 stub，除非操作者另行决定调整 macOS
隐私授权。Docker-free 核验以本节脚本、端口和本机 PostgreSQL 工具为准。

## 4. 模式切换

模式切换不修改前端代码、后端代码、路由或数据库 schema，只修改运行配置并重启
后端：

| 配置 | Test | Live |
| --- | --- | --- |
| `FT_MODE` | `test` | `live` |
| `FT_COOKIE_SECURE` | 本地可为 `false` | 必须为 `true` |
| `FT_PRODUCTION_WRITES_ENABLED` | 不控制 Test 写入 | 默认/建议为 `false` |
| Gateway URL | HTTP/HTTPS | 远端必须 HTTPS；HTTP 仅允许 loopback |
| Gateway 账户 | Gateway-native `demo` 和/或 `testnet` | 仅 Gateway-native `live` |

切换步骤：

1. 停止后端，保留前端构建不变；
2. 将 `FT_MODE`、Gateway URL/token 和 Gateway 账户配置切到同一 mode 边界；
3. Live 首次只读验收时保持 `FT_PRODUCTION_WRITES_ENABLED=false`；
4. 重启后端并重新登录；
5. 检查 `/api/v1/system/status` 和 `/api/v1/system/readiness`；
6. 确认 `publicData` 对应目标 mode，且 `twoLegWriteReady`/阻断原因符合预期。

顶层 readiness 只表示服务读取、投影和执行恢复链路可用；它不是私有写入授权。
即使顶层 `ready=true`，`twoLegWrite` 仍可因账户、能力或 mode/native 环境门而失败关闭。

同一数据库可保留不同 mode 的历史投影，因为查询和执行均按 mode 隔离。
切换 mode 时，已有 session 与 CSRF 不再匹配当前服务 mode；浏览器必须重新登录。
回滚到原 mode 使用同一流程。浏览器不能通过 query string 覆盖 mode 或
Gateway-native `venueEnvironment`。

Test mode 统一 Gateway-native Demo 与 Testnet。Gateway-native
`venueEnvironment` 仍必须保留在账户、instrument、leg、fill 和 funding history
的身份里，不能折叠成 bare product environment。

## 5. 私有写入门

前端按钮可见性不是授权边界。`POST /api/v1/orders` 和
`POST /api/v1/positions/{positionId}/close` 只有同时满足以下条件才会进入 Gateway
私有写入：

1. funding-trader 会话、CSRF 和幂等键有效；
2. 私有事件监控已完成恢复并处于 ready；
3. Gateway 当前 mode 允许范围内至少有两个不同场所的合格永续账户；
4. 两个账户均 read-ready、write-ready，并具备完整订单、成交、持仓、资金费率和
   private-stream 能力；
5. 请求账户、产品、mode、native `venueEnvironment` 与发现结果一致；
6. Live 额外要求 `FT_PRODUCTION_WRITES_ENABLED=true`。

Gateway V1 不提供标准化风险限额。`risk_unverifiable` 会在执行 preview 中明确提示，
但不会替用户决定资金规模或杠杆，也不会单独阻断 Test open；余额、交易规则、容量、
滑点、强平缓冲和机会原始 blocker 仍使用真实服务端证据校验。

任一条件不满足都返回稳定的失败关闭结果，不使用 Spot、跨 native 环境账户、模拟响应或
未经授权的新账户替代。

Live 与 Test 使用同一代码路径。Live 保持 default-deny；当前验收不接受任何
Live order，不能把 readiness 或只读 Gateway 成功解释为 Live 写入授权。

## 6. 私有 V1 cutover

产品模式 cutover 将旧 `environment` 作用域迁移为 `mode` 作用域，并保留
Gateway-native `venueEnvironment`。该流程会撤销旧 session/CSRF，清空 derived
read model 和 cursor namespace，并要求操作者在恢复窗口关闭前明确决定是否回滚。

以下是唯一允许的顺序。命令块是操作者 runbook 模板；`BACKUP_ID`、`BACKUP_SHA256`
和 DSN 必须来自操作者自己的备份系统，不能包含 secret 值。

```text
1. Fence traffic:
   - stop old funding-trader service
   - disable automatic restart
   - keep the frontend/backend in maintenance, not normal serve

2. Create and verify backup:
   - create a database backup outside the application process
   - compute sha256 of the backup artifact
   - run a restore rehearsal against a disposable database
   - record BACKUP_ID, BACKUP_SHA256, and BACKUP_VERIFIED_AT

3. Run migration only after the fence and verified backup:
   FT_STARTUP_MODE=migrate
   FT_CUTOVER_DATABASE_URL=<operator-owned cutover DSN>
   FT_CUTOVER_BACKUP_ID=<BACKUP_ID>
   FT_CUTOVER_BACKUP_SHA256=<64 lowercase hex chars>
   FT_CUTOVER_BACKUP_VERIFIED_AT=<RFC3339 UTC timestamp>
   FT_CUTOVER_OLD_SERVICE_STOPPED=true
   FT_CUTOVER_AUTO_RESTART_DISABLED=true
   go run ./cmd/funding-trader-api

4. Validate post-cutover state before traffic:
   - cutover_control.phase is validated
   - schema version is 4
   - old sessions are revoked
   - read_model_events, opportunities, alerts, and execution_previews are cleared
   - authoritative execution, idempotency, position, activity, audit, and funding rows retain exact mode/native provenance

5. Restore-before-reopen decision:
   - if validation fails or rollback is required, authorize restore against the same BACKUP_ID while phase is still validated
   - restore is forbidden after normal traffic is reopened
   - restore is forbidden after any new-contract write is accepted

6. Reopen:
   - start with FT_STARTUP_MODE=serve and FT_MODE=test or FT_MODE=live
   - require all users to log in again
   - treat all old browser cursors as invalid and resnapshot from the backend
   - only after smoke checks may cutover_control be marked reopened
```

Do not mark `cutover_control.normal_traffic_reopened=true` or
`new_contract_write_accepted=true` until the restore-before-reopen decision is closed.
The code enforces the same boundary: restore authorization is accepted only before
traffic reopen and before any new-contract write acceptance.

## 7. 验证

后端静态与回归检查：

```sh
cd backend
go test ./... -count=1
go test -race ./... -count=1
go vet ./...
```

前端：

```sh
cd prototype
FT_BACKEND_ORIGIN=http://127.0.0.1:18091 npm run verify
```

Test-mode 的真实只读与能力门命令、当前账户阻断和验收证据见
[`TEST_MODE_GATEWAY_ACCEPTANCE.md`](./TEST_MODE_GATEWAY_ACCEPTANCE.md)。

## 8. 安全与监控

- 优先使用 `FT_GATEWAY_TOKEN_FILE`，并限制文件读取权限；
- Live 远端 Gateway 必须使用 HTTPS/WSS，本机 sidecar 才允许 loopback HTTP/WS；
- Live Cookie 必须为 Secure；TLS/HSTS 在反向代理或边缘终止；
- 登录按规范化邮箱与直接来源地址进行有界节流，连续失败会返回 `429` 和
  `Retry-After`；
- `/metrics` 与详细 `/api/v1/system/readiness` 都要求认证；
- 重点监控 `partiallyHedged`、`compensating`、`uncertain`、
  `manualInterventionRequired` 与 `resnapshot_required`；
- 日志只记录请求 ID、方法、路径、状态和错误类型，不记录 token、密码、CSRF、
  Gateway 原始错误正文或交易所凭证。

## 9. Live 生产发布门

Test mode 和本地只读验收不受以下门槛影响；Live 生产部署必须额外完成依赖
安全放行。当前前端执行干净 `npm ci` 时，包管理器摘要报告 12 个已知漏洞
（1 个 low、11 个 high）。受当前环境的依赖清单披露策略限制，本次验收未取得可用于
定位具体包与修复版本的详细 advisory 报告；离线审计缓存也不包含足够数据，不能作为
零漏洞证据。

在满足以下条件前，不得批准 Live 生产发布：

1. 在获批的 CI、私有 registry 或内部安全扫描环境取得完整 advisory 产物；
2. 对所有 high 项逐一确认运行时可达性、修复版本、替代方案或有时限的风险接受；
3. 升级或缓解后重新执行前端测试、类型检查、生产构建和完整 Test-mode 回归；
4. 将安全审查结果与发布审批一同留档。

该门槛不改变 Live 的配置切换能力：代码路径仍保持 mode 无关、远端 Gateway 强制
HTTPS、Cookie 强制 Secure，且 `FT_PRODUCTION_WRITES_ENABLED` 默认失败关闭。
