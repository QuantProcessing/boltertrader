# Funding Arb API V1 契约

> 状态：当前实现契约
>
> 目标：描述 Go 后端与前端 `apiClient`/SSE 客户端实际对接的 V1 HTTP API。
>
> 验收边界：Test-mode 的真实 Gateway 验收记录见
> [TEST_MODE_GATEWAY_ACCEPTANCE.md](./TEST_MODE_GATEWAY_ACCEPTANCE.md)。

## 1. 范围

V1 当前实现是单用户、Cookie 会话认证的后端。启动时通过
`FT_BOOTSTRAP_EMAIL` 和 `FT_BOOTSTRAP_PASSWORD` 创建或确保唯一的默认
principal。请求按 `principalId + mode` 作用域读写；未来可以沿用这个
作用域扩展多用户，但当前没有浏览器可用的注册、角色、管理员或用户管理 API。

Gateway 是交易所账户、交易所凭证和私有写入的唯一所有者。浏览器和
funding-trader 后端不接收、保存、校验或删除交易所 API Key、Secret、
Passphrase。V1 没有交易所连接管理接口。

## 2. 通用约定

| 项目 | 契约 |
| --- | --- |
| API Base URL | `/api/v1` |
| 健康检查 | `/health/live`、`/health/ready` 不带 `/api/v1` 前缀 |
| 指标 | `/metrics`，认证后返回 Prometheus text |
| JSON 字段 | camelCase |
| 时间 | RFC3339/RFC3339Nano UTC 字符串 |
| 金额与数量 | 十进制字符串，避免 JSON 浮点数表达资金量 |
| 请求追踪 | 客户端可发 `X-Request-Id`；后端总返回 `X-Request-Id` 响应头 |
| 会话 Cookie | `ft_session` |

JSON 成功响应使用信封：

```json
{
  "data": {},
  "meta": {
    "requestId": "req-123"
  }
}
```

分页响应：

```json
{
  "data": [],
  "meta": {
    "nextCursor": "",
    "hasMore": false,
    "asOf": "2026-07-29T16:00:00Z",
    "counts": {
      "all": 1
    },
    "requestId": "req-123"
  }
}
```

错误响应：

```json
{
  "error": {
    "code": "bad_request",
    "message": "invalid limit",
    "details": {
      "field": "limit"
    },
    "retryable": false,
    "requestId": "req-123"
  }
}
```

通用状态语义：

| 状态码 | 含义 |
| --- | --- |
| `200` | 请求成功；readiness 不就绪时也可能用 `503` 返回成功形状 |
| `202` | 执行写入已接受 |
| `400` | JSON、请求体、查询参数、路径参数或幂等键格式错误 |
| `401` | 缺少、无效或过期会话 |
| `403` | CSRF 校验失败 |
| `404` | 作用域内资源不存在 |
| `409` | 幂等冲突、预览冲突、预览过期或执行已在进行 |
| `422` | 预览被业务规则阻断 |
| `429` | 登录失败节流；响应带 `Retry-After` |
| `503` | readiness、读模型、执行或指标服务不可用 |

## 3. 认证、CSRF 与幂等

登录成功后，后端设置 `ft_session` Cookie：`HttpOnly`、`Path=/`、
`SameSite=Strict`，`Secure` 由 `FT_COOKIE_SECURE` 控制。Live 启动要求
`FT_COOKIE_SECURE=true`。

登录按规范化邮箱与直接网络来源进行有界节流。连续五次凭证失败后，同一 key
锁定五分钟；后续请求返回 `429 rate_limited` 和 `Retry-After`。成功登录清除
该 key 的历史失败。节流状态有容量上限，不信任浏览器传入的转发地址 header。

除登录和只读接口外，写接口需要 `X-CSRF-Token`。Token 来自登录或当前会话
响应。执行写接口还需要调用方显式提供 `Idempotency-Key`；前端客户端不会替
调用方生成执行幂等键。

执行幂等响应会在 `meta` 中包含：

```json
{
  "requestId": "req-123",
  "replayed": false,
  "degraded": false,
  "recoverable": false
}
```

## 4. 模式与写入门

`FT_MODE` 只能是 `test` 或 `live`。读模型和执行服务使用后端当前配置的
mode 建立作用域，而不是信任会话中可能陈旧的 mode。浏览器不能通过查询参数
覆盖 mode；任何 `/api/v1/*?environment=...` 或 `/api/v1/*?mode=...` 请求都会
返回 `400 bad_request`。

Test mode 统一 Gateway-native Demo 与 Testnet。Gateway 返回的
`venueEnvironment` 仍保留在账户、instrument、执行 leg、fill 和 funding history
的合格身份中；它不是产品级 `mode`。

必要配置：

| 变量 | 用途 |
| --- | --- |
| `FT_DATABASE_URL` | PostgreSQL 连接 |
| `FT_GATEWAY_BASE_URL` | BolterTrader Gateway URL |
| `FT_GATEWAY_TOKEN` 或 `FT_GATEWAY_TOKEN_FILE` | Gateway bearer token |
| `FT_BOOTSTRAP_EMAIL` | bootstrap 登录邮箱 |
| `FT_BOOTSTRAP_PASSWORD` | bootstrap 登录密码，必须显式且不弱 |
| `FT_MARKET_SYMBOLS` | 市场符号列表，默认 `BTC-USDT,ETH-USDT,SOL-USDT` |
| `FT_PRODUCTION_WRITES_ENABLED` | Live 私有写入门，默认 `false` |

Live 默认拒绝真实私有写入：`FT_PRODUCTION_WRITES_ENABLED=false` 时 Gateway
client 会阻断 Live 下单。所有私有写入还必须通过后端按操作计算的 readiness，不能
依赖浏览器按钮状态授权。`POST /orders` 要求 `openReady=true`，其中包括执行服务、
私有事件 continuity 和两腿写入验收；两腿验收要求 Gateway 在当前 mode 允许的范围内
发现至少两个不同场所的 read-ready、write-ready 永续账户，且具备 place、cancel、
orders、fills、balances、account、positions、funding 和 private-stream 能力。

`POST /positions/{positionId}/close` 则要求 `closeReady=true`：目标必须是可解析的既有
头寸，权威私有流或 resnapshot 必须新鲜，执行服务允许 close，且 Gateway 具备对应的
reduce-only 写能力。close 不以“再次满足新开仓两场所验收”为前提，因此在 open 被阻断时
仍可安全减仓；但 Live 没有隐式紧急授权，只有显式批准的 Live emergency reduce-only
策略才可能令 `closeReady=true`。

Live Gateway bearer token 不得发送到远端明文 HTTP。配置层和 Gateway
client 都要求 Live 使用 HTTPS/WSS；只有 `localhost` 或真实 loopback IP
sidecar 可以使用 HTTP/WS。

## 5. 路由总览

| 方法 | 路径 | 认证 | 说明 |
| --- | --- | --- | --- |
| `GET` | `/health/live` | 否 | 进程存活 |
| `GET` | `/health/ready` | 否 | 依赖 readiness，可能返回 `503` 成功信封 |
| `GET` | `/metrics` | 是 | Prometheus 指标 |
| `POST` | `/api/v1/auth/login` | 否 | 登录并设置 Cookie |
| `GET` | `/api/v1/auth/session` | 是 | 当前会话 |
| `DELETE` | `/api/v1/auth/session` | Cookie + CSRF | 退出登录 |
| `GET` | `/api/v1/me` | 是 | 当前 principal |
| `GET` | `/api/v1/system/status` | 是 | 版本、构建、mode、uptime、ready |
| `GET` | `/api/v1/system/readiness` | 是 | 依赖 readiness，可能返回 `503` 成功信封 |
| `GET` | `/api/v1/opportunities` | 是 | 机会列表 |
| `GET` | `/api/v1/opportunities/{opportunityId}` | 是 | 机会详情 |
| `GET` | `/api/v1/funding-rate-alerts` | 是 | 资金费率预警 |
| `POST` | `/api/v1/execution-previews` | Cookie + CSRF | 创建服务端预览 |
| `POST` | `/api/v1/orders` | Cookie + CSRF + idem | 提交 open 执行 |
| `GET` | `/api/v1/executions/{executionId}` | 是 | 执行详情 |
| `GET` | `/api/v1/positions` | 是 | 持仓列表 |
| `POST` | `/api/v1/positions/{positionId}/close` | Cookie + CSRF + idem | 关闭持仓 |
| `GET` | `/api/v1/activities` | 是 | 活动列表 |
| `GET` | `/api/v1/audit-events` | 是 | 审计事件列表 |
| `GET` | `/api/v1/stream` | 是 | 浏览器 SSE |

## 6. 认证接口

### `POST /api/v1/auth/login`

请求：

```json
{
  "email": "owner@example.com",
  "password": "first secure password"
}
```

响应 `200`：

```json
{
  "data": {
    "session": {
      "authenticated": true,
      "expiresAt": "2026-07-29T17:00:00Z",
      "csrfToken": "csrf-token"
    },
    "principal": {
      "principalId": "principal-default",
      "mode": "test",
      "lastLoginAt": "2026-07-29T16:00:00Z"
    }
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

错误：`400 bad_request`、`401 invalid_credentials`、`429 rate_limited`、
`503 service_unavailable`。`429` 响应带 `Retry-After`，且不会回显邮箱或密码。

### `GET /api/v1/auth/session`

返回当前 `session` 和 `principal`，形状与登录响应相同。无效会话返回
`401 unauthenticated` 并清除 Cookie。

### `DELETE /api/v1/auth/session`

需要 `ft_session` Cookie 和 `X-CSRF-Token`。成功响应：

```json
{
  "data": {
    "loggedOut": true
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

缺 Cookie 或未知会话返回 `401 unauthenticated`。CSRF 缺失或不匹配返回
`403 csrf_invalid`。同一会话使用匹配 CSRF 重放退出请求返回 `200`。

### `GET /api/v1/me`

响应：

```json
{
  "data": {
    "principal": {
      "principalId": "principal-default",
      "mode": "test",
      "lastLoginAt": "2026-07-29T16:00:00Z"
    }
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

## 7. 系统、readiness 与指标

### `GET /api/v1/system/status`

响应 `200`：

```json
{
  "data": {
    "mode": "test",
    "version": "v1",
    "build": "local",
    "uptimeSeconds": 12,
    "ready": true
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

### `GET /health/ready` 与 `GET /api/v1/system/readiness`

ready 时返回 `200`，不 ready 时返回 `503`，两者都使用成功信封：

```json
{
  "data": {
    "ready": false,
    "components": {
      "database": {
        "ready": true
      },
      "gateway": {
        "ready": false,
        "reason": "not-initialized"
      },
      "execution": {
        "ready": false,
        "reason": "private-events-not-configured",
        "details": {
          "privateEventsReady": "false",
          "twoLegWriteReady": "false",
          "twoLegWriteReason": "insufficient-ready-perp-venues"
        }
      }
    }
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

核心组件包括 `process`、`database`、`gateway`、`publicData`、`accounts`、
`execution`；完整 App 还会暴露 `twoLegWrite`。

顶层 `ready` 表示服务读取、投影与执行恢复链路可用，不表示当前 mode 已授权任何
私有写入。新开仓还必须检查 `openReady` 以及 `components.twoLegWrite.ready`（同义的
`execution.details.twoLegWriteReady`）；既有头寸的 reduce-only 关闭必须单独检查
`closeReady`，不能用两腿 open 门或顶层 `ready` 代替。两类写接口都会在服务端重新
执行各自的失败关闭门，浏览器显示状态不是授权依据。

### `GET /metrics`

需要认证。成功时返回 `Content-Type: text/plain; version=0.0.4; charset=utf-8`。
当前指标不带 principal、mode、venueEnvironment、account、token 或 secret 标签：

```text
funding_trader_execution_state_current{state="partiallyHedged"} 0
funding_trader_execution_state_current{state="compensating"} 0
funding_trader_execution_state_current{state="uncertain"} 0
funding_trader_execution_state_current{state="manualInterventionRequired"} 0
funding_trader_resnapshot_required_events 0
```

指标读取失败返回 `503 metrics_unavailable`，不暴露原始数据库或 Gateway 错误。

## 8. 读模型接口

读模型列表接口返回数据库中已投影的 JSON payload，payload 字段在 `data[]` 中
保持顶层展开，不包一层 `payload`。通用查询参数：

| 参数 | 说明 |
| --- | --- |
| `limit` | 默认 `50`，HTTP 层允许 `1..100` |
| `cursor` | 后端签名的不透明游标 |

通用错误：`400 bad_request`、`400 invalid_cursor`、`404 not_found`、
`503 read_model_unavailable`、`503 read_model_error`。

### `GET /api/v1/opportunities`

查询参数：

| 参数 | 值 |
| --- | --- |
| `sort` | `netApr_desc` 默认，或 `netApr_asc` |
| `status` | 空、`all`、`executable`、`attention`、`blocked` |
| `shortVenueId` | 精确匹配 payload 中的 `shortVenueId` |

响应示例：

```json
{
  "data": [
    {
      "opportunityId": "opp-1",
      "symbol": "BTC-USDT",
      "netAprPct": 12.5
    }
  ],
  "meta": {
    "nextCursor": "",
    "hasMore": false,
    "asOf": "2026-07-29T16:00:00Z",
    "requestId": "req-123"
  }
}
```

### `GET /api/v1/opportunities/{opportunityId}`

返回单个机会 payload：

```json
{
  "data": {
    "opportunityId": "opp-1",
    "symbol": "BTC-USDT",
    "netAprPct": 12.5
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

### `GET /api/v1/funding-rate-alerts`

查询参数：`severity=all|critical|warning|watch`。响应 `meta.counts` 包含
`all`、`critical`、`warning`、`watch`。

### `GET /api/v1/positions`

查询参数：可重复 `state`。未提供时默认查询 `open` 和 `attention`。

### `GET /api/v1/activities`

查询参数：

| 参数 | 说明 |
| --- | --- |
| `kind` | 可重复活动类型 |
| `from` | RFC3339 起始时间 |
| `to` | RFC3339 结束时间，不能早于 `from` |

### `GET /api/v1/audit-events`

返回当前 principal 可见的审计事件。当前持久化实现会返回登录、退出、CSRF、
会话等审计动作；响应项形状：

```json
{
  "auditId": "1",
  "time": "2026-07-29T16:00:00Z",
  "principalId": "principal-default",
  "kind": "auth.login",
  "targetId": null,
  "outcome": "success",
  "requestId": "req-login",
  "executionId": null
}
```

## 9. 执行接口

执行接口不接受交易所凭证字段。请求体严格解码，未知字段和错误类型会返回
`400 bad_request`。

### `POST /api/v1/execution-previews`

需要 Cookie 和 `X-CSRF-Token`。请求：

```json
{
  "opportunityId": "opp-1",
  "capital": "5000",
  "leverage": 2
}
```

约束：`capital` 必须是正十进制字符串；`leverage` 只能是 `1` 或 `2`。后端生成
服务端 `previewId`，并以当前 read/execution scope 重新构造 open 预览。

响应 `200`：

```json
{
  "data": {
    "previewId": "preview-server",
    "opportunityId": "opp-1",
    "snapshotId": "snapshot-1",
    "canSubmit": true,
    "blockers": [],
    "warnings": [],
    "plannedLegs": [
      {
        "role": "long",
        "accountId": "account-1",
        "venue": "venue-a",
        "venueEnvironment": "demo",
        "symbol": "BTCUSDT",
        "instrumentId": "instrument-1",
        "side": "buy",
        "quantity": "49.75",
        "notional": "5000",
        "markPrice": "100.5",
        "reduceOnly": false
      }
    ],
    "estimatedFeesUsd": "1.25",
    "estimatedSlippageUsd": "2.5",
    "expectedNetAprPct": 12.345,
    "expectedNetPnL7dUsd": "8.75"
  },
  "meta": {
    "requestId": "req-123"
  }
}
```

### `POST /api/v1/orders`

需要 Cookie、`X-CSRF-Token` 和 `Idempotency-Key`。只支持 open：

```json
{
  "opportunityId": "opp-1",
  "previewId": "preview-1",
  "capital": "5000",
  "leverage": 2,
  "operationKind": "open"
}
```

成功通常返回 `202`：

```json
{
  "data": {
    "executionId": "execution-open",
    "operationKind": "open",
    "opportunityId": "opp-1",
    "state": "open",
    "latestEventAt": "2026-07-29T17:00:00Z",
    "gatewayOrderIds": [],
    "compensationState": null,
    "reconciliationState": "",
    "blockingReason": "",
    "manualInterventionRequired": false,
    "legs": [],
    "fills": []
  },
  "meta": {
    "requestId": "req-123",
    "replayed": false,
    "degraded": false,
    "recoverable": false
  }
}
```

### `GET /api/v1/executions/{executionId}`

返回同一 execution DTO。作用域内不存在返回 `404 execution_not_found`。公共状态：
`open`、`partiallyFilled`、`partiallyHedged`、`compensating`、`completed`、
`rejected`、`uncertain`、`manualInterventionRequired`。

执行 leg：

```json
{
  "legId": "long",
  "role": "long",
  "accountId": "account-1",
  "venue": "venue-a",
  "instrumentId": "instrument-1",
  "venueEnvironment": "demo",
  "side": "buy",
  "requestedQuantity": "1.5",
  "filledQuantity": "1.25",
  "state": "partiallyFilled",
  "gatewayOrderId": "gateway-order-1",
  "lastError": {
    "code": "PARTIAL",
    "message": "partially filled",
    "retryable": false
  },
  "updatedAt": "2026-07-29T16:30:00.123Z"
}
```

执行 fill：

```json
{
  "fillId": "fill-1",
  "legId": "long",
  "accountId": "account-1",
  "venue": "venue-a",
  "instrumentId": "instrument-1",
  "venueEnvironment": "demo",
  "gatewayFillId": "gateway-fill-1",
  "quantity": "1.25",
  "price": "101.5",
  "fee": {
    "amount": "0.2",
    "currency": "USDT"
  },
  "occurredAt": "2026-07-29T16:30:00.123Z"
}
```

内部字段如 principal、product mode、Gateway command id、applied event id 和原始
上游错误不会出现在公共执行 DTO 中。Gateway-native `venueEnvironment` 是 leg/fill
身份的一部分，会出现在公共执行 DTO 中。

### `POST /api/v1/positions/{positionId}/close`

需要 Cookie、`X-CSRF-Token` 和 `Idempotency-Key`。请求体必须为空，或为空 JSON
对象。`positionId` 使用标准 path segment URL 编码，例如 `position/1` 发送为
`/api/v1/positions/position%2F1/close`。成功返回 execution DTO，通常为 `202`。

执行错误映射：

| 错误码 | 状态码 | 含义 |
| --- | --- | --- |
| `execution_not_found` | `404` | 执行不存在 |
| `idempotency_conflict` | `409` | 同 key 与原请求冲突 |
| `preview_conflict` | `409` | 预览不匹配、过期或不再有效 |
| `execution_in_progress` | `409` | 同一执行仍在进行，可重试 |
| `preview_blocked` | `422` | 预览存在 blocker |
| `execution_unavailable` | `503` | 执行服务未装配、恢复中、未就绪或写入门未满足 |

## 10. SSE

### `GET /api/v1/stream`

需要认证，浏览器客户端使用 `EventSource` 且 `withCredentials=true`。

查询参数：

| 参数 | 说明 |
| --- | --- |
| `cursor` | 最近收到的 SSE cursor |
| `kind` | 可重复；只允许服务端可过滤事件 |

可过滤事件：`system.status`、`opportunity.upsert`、`alert.upsert`、
`execution.upsert`、`position.upsert`、`activity.append`、`audit.append`、
`resnapshot.required`。客户端仍会监听 `snapshot` 和 `heartbeat`，但不会把它们作为
过滤参数发送。

响应头：

```text
Content-Type: text/event-stream
Cache-Control: no-cache, no-store
Connection: keep-alive
X-Accel-Buffering: no
```

事件帧：

```text
id: opaque-cursor
event: opportunity.upsert
data: {"cursor":"opaque-cursor","occurredAt":"2026-07-29T16:00:00Z","data":{"opportunityId":"opp-1"}}
```

无 cursor 连接先收到 `snapshot`。cursor 无效或 replay 窗口失效时，后端发送
`resnapshot.required`，其中 data 顶层包含标准 `cursor`、`occurredAt` 和 `scope`。
SSE sink 在每次发送前重新校验会话；会话失效会终止流。

## 11. 当前未实现的接口

以下内容不是 V1 当前 API：

- 浏览器或 funding-trader 后端中的交易所凭证管理；
- `/api/v1/exchange-connections` 或 API Key 保存、校验、删除；
- 注册、角色、viewer/trader/admin 权限模型；
- `/api/v1/admin/*` 用户管理；
- 浏览器请求字段控制 `test`/`live` mode、Gateway-native `venueEnvironment` 或是否真实下单；
- 使用 mock 响应绕过 Gateway readiness、两腿写入验收或 Live 写入门。

这些能力只能作为未来扩展讨论；当前文档不声明它们已经实现。
