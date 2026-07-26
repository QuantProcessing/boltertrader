# Exchange Gateway

Exchange Gateway 是构建在 `exchange` 驱动之上的自托管、长期运行 Go
微服务。服务统一管理交易所 client、凭证、instrument Catalog、持久订单身份、
重启 reconciliation、私有事件和运维状态；调用方只使用稳定的 Gateway
资源，不需要构造各交易所 SDK 请求。

V1 是交易网关，不是统一清算系统。它不提供跨所 shared margin、托管、
smart order routing、资金划转、充值或 withdrawal endpoint。服务 **no Redis**，
也 **no gRPC**；PostgreSQL 是唯一必需的持久化依赖。

## 服务形态

```text
REST + 私有 WebSocket + /metrics
                 |
          Exchange Gateway
        /        |          \
   Catalog   Order engine   Event hub
        \        |          /
        Account/client manager
                 |
            exchange clients
```

进程提供：

- `/v1` 下带 Bearer 认证的版本化 REST；
- `/v1/ws` 私有事件流；
- `/metrics` Prometheus 指标；
- `/v1/health/live` 存活检查和 `/v1/health/ready` 就绪检查。

TLS 应在部署 ingress 终止。Bearer token 与全部交易所凭证只能通过明确的
secret reference 解析，YAML 不得包含 secret 明文。

## 配置

每个交易所产品账户配置一个 account。V1 覆盖 Binance、OKX、Lighter、
Hyperliquid、Bybit、Bitget、Gate、Aster、Nado 共二十个产品行。

```yaml
server:
  listen: 127.0.0.1:8080
  auth_token_ref: env://GATEWAY_AUTH_TOKEN
postgres:
  dsn_ref: env://GATEWAY_POSTGRES_DSN
accounts:
  - account_id: binance-demo-spot
    venue: binance
    product: spot
    environment: demo
    credentials:
      api_key_ref: env://BINANCE_DEMO_API_KEY
      api_secret_ref: env://BINANCE_DEMO_API_SECRET
```

不同交易所使用严格类型化的 credential 字段。未知字段、重复 account ID、
无效 venue/product/environment 组合或明文 secret，都会在 listener 打开前
阻止启动。

启动：

```sh
go run ./cmd/exchange-gateway -config ./gateway.yaml
```

## 资源与订单

先从服务端解析 instrument：

```sh
curl -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  "http://127.0.0.1:8080/v1/instruments?account_id=binance-demo-spot"
```

下单必须提供 account、Gateway instrument ID 和 idempotency key：

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: strategy-a-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-demo-spot",
    "instrument_id":"binance:demo:spot:BTC-USDT",
    "idempotency_key":"strategy-a-10001",
    "side":"buy",
    "type":"limit",
    "quantity":"0.001",
    "limit_price":"50000",
    "limit_policy":"resting"
  }'
```

Gateway 在首次交易所 write 前先提交 command intent。相同 idempotency key
和相同规范 payload 会重放原 command；相同 key 配不同 payload 返回冲突。
write handoff 后超时会进入 `uncertain`，绝不盲目重发。

查询包括 `/v1/orders`、`/v1/open-orders`、`/v1/fills`、`/v1/balances`、
`/v1/positions`、`/v1/funding-rates`、`/v1/accounts`、
`/v1/capabilities`、`/v1/rules` 和 `/v1/risk-limits`。完整接口见
[OpenAPI 3.1](../../../api/exchange-gateway/v1/openapi.yaml)。

Gateway V1 不执行 leverage mutation。稳定的 leverage 路由会返回 `501`，
并记录安全的 unsupported audit；只有当 leverage 具备与订单相同的持久
idempotency 和重启 reconciliation 模型后才会开放。底层
`exchange.PerpClient` 的直接驱动能力不受影响。

## 私有事件

连接：

```text
GET /v1/ws?account_id=binance-demo-spot
Authorization: Bearer <token>
```

事件携带 boot ID、账户内 sequence、event ID、resource version、标准化 payload
和 kind。订单、成交、余额、仓位、账户健康及 `resync_required` 都是统一模型。
boot cursor 过期、replay 被淘汰、stream gap 或进程重启时会发出
`resync_required`；调用方必须重新获取 REST snapshot。

## 恢复与 reconciliation

PostgreSQL 持久化 idempotency reservation、command handoff 状态、标准订单、
durable event、交易所精确 correlation、reconciliation task 和 audit。
启动顺序为：

1. 校验配置与 secret reference；
2. 连接 PostgreSQL，在 advisory lock 下完成 migration 校验；
3. 获取 account ownership；
4. 构造长期 exchange client 并加载 Catalog；
5. 对 prepared、dispatching、uncertain command 做精确只读 reconciliation；
6. 建立私有 stream；
7. 所有 gate 证明后才开放 write readiness。

若交易所在进程崩溃前已接受订单，重启后使用原 portable client order ID 和
venue correlation 精确查询；禁止 generic write retry。

## 指标、日志与审计

`/metrics` 使用独立 Prometheus registry，包含账户 read/write readiness、
command outcome、uncertain 数量与最老 reconciliation age、stream
gap/resync/eviction、Catalog age/version、PostgreSQL latency 与安全错误分类、
in-flight command，以及 graceful shutdown outcome。

label 仅允许固定的 venue/product/environment、operation、outcome 和安全错误
分类。account ID、instrument ID、idempotency key、order ID、native message
和 credential 派生值都不能进入 label。

进程日志是结构化 JSON，只包含有界 event/code 与可选的已校验 correlation
ID。PostgreSQL audit 包含 principal、operation、Gateway resource、安全
outcome、correlation 和时间，不保存请求 payload、签名或凭证。

## migration 与 graceful shutdown

migration 随 binary 内嵌并只向前执行。数据库 schema 版本更新于 binary，或
历史 checksum 不一致时，启动会 fail closed。每个版本部署前应对临时
PostgreSQL 执行：

```sh
GATEWAY_TEST_POSTGRES_DSN='postgres://...' make test-gateway-postgres
GATEWAY_TEST_POSTGRES_DSN='postgres://...' make test-gateway-restart
```

SIGINT/SIGTERM 会触发 graceful shutdown：先拒绝新 write，排空或持久化
in-flight command identity，再关闭私有 stream、exchange client，释放 account
ownership，最后关闭 PostgreSQL。若 drain 超时，下个进程必须完成
reconciliation 后才能重新开放 write readiness。

## 验证

```sh
make test-gateway-contract
make test-gateway-offline
make test-gateway-race
make test-gateway-security
```

`make test-gateway-demo-testnet` 会使用凭证并改变非生产交易所状态，只能串行
运行，且不属于默认离线测试。Lighter Testnet Spot 可能只有单边盘口；其明确
waiver 只验证 read 与非法 command barrier，执行零次有效下单，且不会把该行
标记为 write-ready。
