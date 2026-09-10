# Exchange Gateway

Exchange Gateway 是构建在 `exchange` 驱动之上的自托管、长期运行 Go
微服务。服务统一管理交易所 client、凭证、instrument Catalog、持久订单身份、
重启 reconciliation、私有事件和运维状态；调用方只使用稳定的 Gateway
资源，不需要构造各交易所 SDK 请求。

V1 是交易网关，不是统一清算系统。它不提供跨所 shared margin、托管、
smart order routing、资金划转、充值或 withdrawal endpoint。服务 **no Redis**，
也 **no gRPC**；PostgreSQL 是唯一必需的持久化依赖。

Go 脚本与监控应用可以使用[独立 Gateway 客户端](gateway-client.md)，无需引用 Trading 或交易所 SDK。

## 选择这个产品

BolterTrader 在交易所之上提供两种同级路径：

- 调用方在 Go 进程内自行负责 lifecycle、缓存与持久化时，直接使用规范化
  `exchange` client；
- 需要常驻进程集中管理 account、凭证、Catalog、订单状态、reconciliation
  和私有事件时，使用 Exchange Gateway。

Gateway 构建在 `exchange` 之上，并不替代它。产品服务位于 Gateway 之上而非
之侧：trading 服务持有自己的命令账本并调用这里的资源。个人脚本、监控和预警应用也应复用 Gateway 能力；当前稳定公开接口与内部市场接口仍需逐步统一。见 [trading 服务](trading-service.md)。

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

TLS 应在部署 ingress 终止。服务 token 通过 secret reference 解析；交易所凭证
在登录后添加并加密存入 PostgreSQL，YAML 不包含 secret 明文。

## 配置

YAML 文件只配置进程本身：监听地址、bearer token 引用与 PostgreSQL DSN
引用。登录后在设置中添加账户：内部接口 `POST /internal/v1/accounts` 只接受
venue、displayName 和 credentialBundle。余额查询成功后保存加密凭证，返回 201；
零余额可以通过。GET 查看，DELETE 只删除本地凭证和连接，保留交易所订单及持仓。
没有停用、轮换、更新权限或期望状态接口。

API 权限由交易所判断，拒绝操作时返回交易所的安全错误消息。重启恢复已保存的
连接，当前持仓直接查询交易所，不导入历史订单。旧安装需要重新添加凭证，
不会导入旧账户记录；迁移 012 只建立新账户表并解除旧账户外键依赖。

`BT_SECRET_MASTER_KEY_FILE` 指向保存 32 字节主密钥（64 位十六进制、0600 权限）的绝对路径文件，保持其私密且
持久化。它用于加密账户记录，不是用户需要额外申请的权限。详见
[简单账户设计](../../design/simple-accounts-2026-09-10.md)。

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

下单必须提供 account、route、Gateway instrument ID、idempotency key，以及账户当前的 `runtime_id`。下面的运行时占位符必须替换为 `/v1/accounts/{account_id}` 返回的值：

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: manual-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-spot",
    "route_id":"spot",
    "runtime_id":"REPLACE_WITH_CURRENT_ACCOUNT_RUNTIME_ID",
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

`runtime_id` 是下单的运行时所有权屏障，旧的三个版本计数字段不再接受。撤单使用 `DELETE /v1/orders/{gateway_order_id}`，无需请求体，账户和路由取自已保存的订单。改单仍返回 `501`。曾经确认接单的订单，即使多次查询不到，也保持待对账状态，不能仅凭缺失推断拒单。

成交的 `fill.fill_id` 是交易所原始 ID。去重身份必须同时包含 `account_id` 和 `instrument_id`；跨品种或跨账户相同的原始 ID 表示不同成交。

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

独立应用现在可以通过 `/v1/market` 访问公共行情：

- `GET /v1/market/instruments`：公共商品目录，明确报告覆盖不完整。
- `GET /v1/market/candles`：按 `instrument_id`、`interval` 读取有界历史。
- `GET /v1/market/snapshot`：读取一次共享 topic 的完整快照。
- `GET /v1/market/ws`：订阅订单簿、BBO、K 线与标记价。

这些入口需要现有 Gateway Bearer token，但不需要交易所账户或凭证。该 token
仍拥有完整 Gateway 权限；本批没有引入只读 token。`complete=false` 表示目录
覆盖或新鲜度不完整，不代表实时行情中断；已知商品仍可从过期目录继续读取行情。
请求方式、断流恢复、范围与示例见[应用行情 API 说明](../../design/market-api-2026-09-10.md)。

原 `/v1/order-book`、`/v1/bbo`、`/v1/mark-price`、`/v1/candles` 和资金费率
接口保持账户范围，兼容现有调用方；新行情应用应使用 `/v1/market`。

`/internal/v1/market/ws` 是 Gateway 的多路复用服务间行情协议，公开 topic 包括订单簿、
BBO、K 线、标记价、资费看板和交易所连接状态。生产能力矩阵决定每个 dataset 的
transport、原生 market family 边界、cadence 与 rate-limit operation。Gateway 按
venue、environment 与原生 market family 唯一持有一个物理 `VenueFeedOwner`；route
profile 只是输出投影，不会增加连接。Market Hub 维护低频最新值，但不会物化完整订单簿。
Exchange 适配层只通过 WebSocket 建立订单簿，校验原生连续性，并只保存共享 topic 为后来
消费者重放所需的有限深度快照。Gateway 随后转发 `snapshot`、`delta` 或自包含的 `full`
事件，不再把 REST 深度拼进另一个 WS 序列。出现 gap 时 generation 增加并发送
`resync_required`，原生流重开并产生新快照。无法仅靠 WS 提供初始订单簿的 venue（目前为
Nado）会明确标记为不支持深度，不会静默退化到 REST。

OI 必须保留交易所归属。Hyperliquid 与 Lighter 从全市场流提供 OI；Bybit、Bitget、Gate、
OKX 和 Nado 使用各自批量能力。Binance 与 Aster 只有逐 symbol 当前 OI，因此全市场 feed
将该字段留空，不再遍历 catalog 触发大量请求，也不会把其他交易所 OI 冒充为其 OI。

热点订单簿、BBO、K 线与 mark topic 在每个 feed owner 内共用一个原生 socket。引用计数
hotset 依次按 position、open-order、execution、browser 与
funding-history 排序；position、open-order 与 execution 不会因交易所 topic 上限被淘汰，
最后一个原因释放后仍保留 30 秒。全市场轮询与 catalog 刷新共用按 venue operation
编排的 rate-limit coordinator 和 singleflight。FreshBBO 直接加入 Market Hub 中同一条
保留的 BBO WebSocket，不依赖浏览器是否订阅，也不再退化到 REST 报价。

内部监听器继续为现有服务消费者保留公共行情操作：`/internal/v1/market/ws`、
`/internal/v1/market-facts`、`/internal/v1/funding-board`、
`/internal/v1/funding-history` 与 `/internal/v1/venues/connectivity`。它们不收 `account_id`，因为 Gateway 使用
免凭证的交易所 client 回答。它们仍由内部 bearer token 保护，也不会暴露凭证、
原生交易所错误等内部细节。

Auth Edge 将 Gateway 行情/账户 topic 与 Trading 订单投影复用到浏览器的
`/api/v1/realtime/ws`。私有 topic 要求登录。Gateway 保留内部市场/账户流，启动或重连时
REST 快照修复私有状态；Trading 消费订单/成交唤醒，不转发浏览器行情。浏览器每 20 秒发送一次应用层
ping，并要求 10 秒内收到 pong，因此浏览器到 Edge 的半开连接不会长期显示为在线。交易所
原生 client 独立执行 read-idle deadline；heartbeat 或读取失败会关闭原生 socket，并进入 owner
有上限的指数退避重连。

资金费看板是常驻 `DerivativeFact` 的纯业务投影。Gateway 启动时对所有支持的永续原生
family 做一次 bootstrap，随后按能力矩阵选择全市场 WebSocket 或有界批量 REST，持续
合并资金费、mark、index 与 OI。宽表与热点 patch 按字段独立时钟合并，迟到的资金费更新
不会覆盖较新的 mark。Binance 和 Aster 的 OI 以一分钟滚动周期异步补齐，热点品种提高到
15 秒；Nado 未流式提供的价格和 OI 由一次有界的全产品查询补齐。Bybit 与 Bitget 从同一条
原生 ticker 保留 mark、index、funding 与 OI，OKX 的四类 reference topic 在 owner 内合并为
一个逻辑热点流。Gate 在 catalog 成员变化时更新现有 `futures.tickers` payload，不替换 owner
或 generation。HTTP 看板接口只复制内存，所以前端数量
不会放大交易所请求。完整订单簿不进入这份投影。

资费 topic 首帧发送完整 `snapshot`，后续最多每秒发送一次合并更新。慢消费者或流缺口会
收到 `resync_required`。资金费、mark、index 与 OI 分别计算 freshness；过期值保留用于诊断，
并通过 `staleFields` 明确标注，但不会进入费差计算。缺失值保持 absent，禁止写成零。

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

每个账户 route 只持有一个产品/账户级原生流，数量不随 catalog 增长。订单、成交、余额和
仓位都携带稳定实体 ID、resource version 与来源事件时间。消费者按实体身份和来源时间合并；
私有事件没有 sequence、generation、gap phase，也不需要 snapshot barrier。

内部 `/v1/ws` 保留给服务投影。浏览器经 Auth Edge 使用 Gateway 的 live-only
`/internal/v1/account/ws`，重连时不会回放一段无界的浏览器 backlog。每个 route 只发布
`connected` 或 `disconnected`，并以带 jitter 的指数退避重连。连接状态只用于展示：不会暂停
下单、不会改写仓位 freshness，也不会触发 REST。固定的 60 秒 reconciliation 负责修复漏掉的状态。

## 恢复与 reconciliation

PostgreSQL 持久化 idempotency reservation、command handoff 状态、标准订单、
durable event、交易所精确 correlation、reconciliation task 和 audit。
启动顺序为：

1. 校验配置与 secret reference；
2. 连接 PostgreSQL，在 advisory lock 下完成 migration 校验；
3. 获取 account ownership；
4. 构造长期 exchange client 并加载 Catalog；
5. 对 prepared、dispatching、uncertain command 做精确只读 reconciliation，之后每 60 秒重复；
6. 建立私有 stream；
7. 写就绪由账户与路由能力决定，不依赖私有 stream 是否在线。

若交易所在进程崩溃前已接受订单，重启后使用原 portable client order ID 和
venue correlation 精确查询；禁止 generic write retry。

## 指标、日志与审计

`/metrics` 使用独立 Prometheus registry。除账户 read/write readiness、command、
reconciliation、Catalog、PostgreSQL 与 shutdown 指标外，还包含交易所 probe
状态/延迟/失败、行情 feed 状态/reconnect/gap/resnapshot、字段 age/coverage、热点订阅数，
以及按 venue 和 operation 分类的 REST outcome。连接 probe、行情 feed 健康与字段
freshness 相互独立。

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
