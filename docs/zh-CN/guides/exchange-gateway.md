# Exchange Gateway

Exchange Gateway 是构建在 `exchange` 驱动之上的自托管、长期运行 Go
微服务。服务统一管理交易所 client、凭证、instrument Catalog、持久订单身份、
重启 reconciliation、私有事件和运维状态；调用方只使用稳定的 Gateway
资源，不需要构造各交易所 SDK 请求。

V1 是交易网关，不是统一清算系统。它不提供跨所 shared margin、托管、
smart order routing、资金划转、充值或 withdrawal endpoint。服务 **no Redis**，
也 **no gRPC**；PostgreSQL 是唯一必需的持久化依赖。

## 选择这个产品

BolterTrader 在交易所之上提供两种同级路径：

- 调用方在 Go 进程内自行负责 lifecycle、缓存与持久化时，直接使用规范化
  `exchange` client；
- 需要常驻进程集中管理 account、凭证、Catalog、订单状态、reconciliation
  和私有事件时，使用 Exchange Gateway。

Gateway 构建在 `exchange` 之上，并不替代它。产品服务位于 Gateway 之上而非
之侧：trading 服务持有自己的命令账本并调用这里的资源，它也是 Gateway 唯一
面向的调用方类型。见 [trading 服务](trading-service.md)。

## 服务形态

```text
REST + 公共/私有 WebSocket + /metrics
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

YAML 文件只配置进程本身：监听地址、bearer token 引用与 PostgreSQL DSN
引用。账户属于持久化状态而非文件状态，通过内部监听器的
`PUT /internal/v1/accounts/desired/{accountId}/operations/{operationId}`
声明并存入 PostgreSQL，重启后自动加载，无需改动任何文件。

```yaml
server:
  listen: 127.0.0.1:8080
  auth_token_ref: env://GATEWAY_AUTH_TOKEN
  internal_listen: 127.0.0.1:8081
  internal_auth_token_ref: env://GATEWAY_INTERNAL_AUTH_TOKEN
postgres:
  dsn_ref: env://GATEWAY_POSTGRES_DSN
```

配置文件严格解码。未知字段、未开启 `allow_non_loopback_listen` 的非回环
监听、非回环的内部监听器或明文 secret，都会在 listener 打开前阻止启动。

V1 为 Binance、OKX、Lighter、Hyperliquid、Bybit、Bitget、Gate、Aster、Nado
建模共二十个产品行，其中十九行默认 write-ready；Lighter Spot 在对着 live 盘口
证明双边流动性和 exact lookup 之前保持 read-only。不同交易所使用严格
类型化的 credential 字段，在账户声明时严格解码。

启动：

```sh
go run ./cmd/exchange-gateway -config ./gateway.yaml
```

## 资源与订单

先从服务端解析 instrument：

```sh
curl -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  "http://127.0.0.1:8080/v1/instruments?account_id=binance-spot"
```

下单必须提供 account、Gateway instrument ID 和 idempotency key：

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: manual-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-spot",
    "instrument_id":"binance:live:spot:BTC-USDT",
    "idempotency_key":"manual-10001",
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
`/v1/positions`、`/v1/funding-rates`、`/v1/funding-rates/history`、
`/v1/order-book`、`/v1/bbo`、`/v1/mark-price`、`/v1/accounts`、
`/v1/capabilities`、`/v1/rules` 和 `/v1/risk-limits`。完整接口见
[OpenAPI 3.1](../../../api/exchange-gateway/v1/openapi.yaml)。

`/v1/fills` 可选传入 `gateway_order_id` 精确查询某个 Gateway 订单的成交。
Gateway 会先验证调用方拥有该订单，再映射到内部 venue order id 并过滤交易所
fills；响应不暴露 venue order id。如果同时传入 `account_id` 或
`instrument_id`，它们必须与订单保存的身份一致。只有当交易所分页明确证明还有
下一页时才返回 `next_cursor`。

Gateway V1 不执行 leverage mutation。稳定的 leverage 路由会返回 `501`，
并记录安全的 unsupported audit；只有当 leverage 具备与订单相同的持久
idempotency 和重启 reconciliation 模型后才会开放。底层
`exchange.PerpClient` 的直接驱动能力不受影响。

## 公共行情

公共行情读取仍必须携带 Gateway Bearer token、`account_id` 和规范化
`instrument_id`。账户决定使用哪个由 Gateway 持有的交易所 client，并把请求
绑定到已配置的 product/environment。调用方既不提交也不会收到交易所凭证、
native symbol、native ID、instrument encoding 或原始交易所错误。

`/internal/v1/market/ws` 是 Gateway 的多路复用服务间行情协议，公开 topic 包括订单簿、
BBO、K 线、标记价、资费看板和交易所连接状态。Market Hub 按 venue、environment、原生
market family 和 dataset 唯一持有上游 feed，维护可重建的最新 snapshot，并扇出给所有
消费者。出现 gap 时 generation 增加，发送 `resync_required` 并重建 snapshot。

内部监听器上的公共行情操作是这条「必须带账户」规则的例外：`/internal/v1/market/ws`、
`/internal/v1/market-facts`、`/internal/v1/funding-board`、
`/internal/v1/funding-history` 与 `/internal/v1/venues/connectivity`。它们不收 `account_id`，因为 Gateway 使用
免凭证的交易所 client 回答。它们仍由内部 bearer token 保护，也不会暴露凭证、
原生交易所错误等内部细节。

资金费看板是常驻 `DerivativeFact` 的纯业务投影。Gateway 启动时对所有支持的永续 route
做一次 bootstrap，随后通过交易所 WebSocket 持续合并资金费、mark、index 与 OI。
Binance 和 Aster 的 OI 以一分钟滚动周期异步补齐；Nado 未流式提供的价格和 OI 由
一次有界的全产品查询补齐。HTTP 看板接口只复制内存，所以前端数量不会放大交易所
请求。完整订单簿不进入这份投影。

资费 topic 首帧发送完整 `snapshot`，后续最多每秒发送一次合并更新。慢消费者或流缺口会
收到 `resync_required`。资金费、mark、index 与 OI 分别计算 freshness；过期值保留用于诊断，
并通过 `staleFields` 明确标注，但不会进入套利计算。缺失值保持 absent，禁止写成零。

历史读取仍是按需调用。它收 venue、environment、symbol，以及一对明确的
`since`/`until`，返回
一条资金费点位序列，结算周期由点位之间的间隔量出来。规范化 symbol 会从看板
常驻投影的对应列里解析成该交易所的原生标识，所以原生形态在这里同样不越界。
没有历史 surface 的交易所会用一个明说此事的错误码回答，而不是回一条空序列；
不在该列上的 symbol 则用另一个错误码——「这家不留历史」和「这家没上这个标的」
是两件不同的事实，分不清的调用方会重试错的那一个。区间由调用方自己选、也由
调用方自己把握：trading 服务只放行两个固定窗口，为的正是让一次读取就是一次
交易所调用、不需要翻页。历史 K 线和资金费不会被看板采集器预热或持久化。

## 私有事件

连接：

```text
GET /v1/ws?account_id=binance-spot
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

## Docker single-host 部署

本地栈需要 Docker Engine 和 Compose v2。它是 single-host 运维示例：
PostgreSQL、三个一次性 migrator 和四个服务，外加一个 PostgreSQL named
volume；不包含 Kubernetes、多主机调度、registry publishing 或 high
availability。

启动方式只有一条命令：

```sh
make up
```

它会在 `.secrets/stack/` 下生成栈的凭证、把变量写入 `.env.stack`、启动全部服务，
最后执行 `make doctor`。该命令是幂等的：再次运行会复用已有凭证。出问题时用
`make doctor` 定位，用 `make logs` 查看全部服务日志。

不需要手工复制任何文件，也不能把 secret 明文写入 `.env.stack`、image、build
argument 或 `docker/gateway.docker.yaml`。Bearer token 和 Gateway PostgreSQL DSN
以 0600 保存在 `.secrets/stack/` 下的文件里，`.env.stack` 只保存指向它们的路径。
两者通过 Docker secrets 到达 Gateway，挂载点为
`/run/secrets/GATEWAY_AUTH_TOKEN` 与 `/run/secrets/GATEWAY_POSTGRES_DSN`。

Makefile 中每一次 Compose 调用都显式传入 `--env-file .env.stack`，包括只读取
元数据的那些。不传时 Compose 会自动加载仓库的 `./.env`——那里保存着真实交易所
API 凭证，会被插值进栈的环境变量。

`docker/gateway.docker.yaml` 默认使用 `accounts: []` 安全启动。配置真实账户时，
应复制该文件到 operator 自己管理的位置，并让 `GATEWAY_CONFIG_FILE` 指向副本；
每个新增的 `env://NAME` credential 也必须在 Compose 中声明同名 secret，并挂载到
`/run/secrets/NAME`。Docker helper 只解析这些显式名称，随后 exec Gateway，使
Gateway 成为 PID1。

默认 host publication 为 `127.0.0.1:18080`。container 内的
`allow_non_loopback_listen: true` 仅显式允许 `0.0.0.0:18080`；普通非 Docker
配置仍默认只允许 loopback。

检查带认证的 health，命令不会打印 bearer token：

```sh
docker compose --env-file .env.stack -f compose.yaml exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind live
docker compose --env-file .env.stack -f compose.yaml exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind ready
```

停止服务会向作为 PID1 的进程发送 SIGTERM。栈给出 15 秒停止窗口，高于 Gateway 的
10 秒 drain budget。升级时先构建新的不可变 image，执行容器验证目标，再 recreate
服务；服务会在重新开放 write readiness 前完成 migration 和 reconciliation。

PostgreSQL persistence 使用 named volume `postgres_data`。`make down` 会保留它，
`make down-hard` 会明确删除它。应使用常规 PostgreSQL 工具备份与恢复。

`down-hard` 也是让 initdb 脚本改动生效的唯一方式：PostgreSQL image 只在数据目录
为空时执行这些脚本一次，因此 volume 存留就意味着改过的角色集永远不会被应用。

使用 external PostgreSQL 时，应脱离内置 PostgreSQL service 单独部署 image，并让
`GATEWAY_POSTGRES_DSN_FILE` 指向启用 TLS 的外部 DSN。上线前先在临时数据库运行
migration 与 restart gate，并保持数据库网络私有。

TLS 应终止在同一主机的 ingress 或 reverse proxy。默认 loopback publication
适合该形态。若要发布到其他网卡，必须显式配置 firewall、TLS、bearer token
轮换和 access-log 审查。此 single-host 栈没有 leader election 或自动
failover；每个交易所 account 只能有一个 Gateway owner。

容器验证。下面每个目标都会用生成的凭证和内核分配的端口创建各自的一次性 Compose
project，因此既不需要先跑 `make up`，也不会干扰已经启动的栈：

```sh
make test-compose-config
make test-compose-build
make test-compose-security
make test-compose-smoke
make test-compose-persistence
make test-compose-postgres
make test-compose
```

## 验证

```sh
make test-gateway-contract
make test-gateway-offline
make test-gateway-race
make test-gateway-security
```

这下面过去还有第五条 target，`make test-gateway-demo-testnet`。它会使用凭证并改变
交易所状态，在各交易所的 demo 或 testnet 环境下串行跑完二十个产品行，且不属于默认
离线测试。它已经和那些环境一起删除，没有任何东西接替它——同一套跑法放到 live 上就是
真实下单。上面这四条不会碰到任何交易所，因此它们都无法确立某一行 write-ready；那条
断言需要[新增一个 venue](../contributing/venues.md)所指名的证据。
