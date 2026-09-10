# 面向独立应用的公共行情 API

本批把 Gateway 已有公共目录、行情所有者和 Market Hub 接到稳定的 `/v1/market`
入口。监控或其他独立应用无需创建交易账户，也无需通过浏览器内部 API 读取行情。
没有新增采集服务、行情数据库或第二份缓存。

## 入口与身份

所有入口沿用 Gateway 的 `Authorization: Bearer …` 鉴权，WS token 也必须放在
header 中，不能放入 URL。这里的“公共”指交易所公开市场数据，不指匿名访问。
个人部署使用同一 Gateway 服务身份，不再配置行情只读 token 或本地权限等级。

| 方法与路径 | 输入 | 输出 |
| --- | --- | --- |
| `GET /v1/market/instruments` | 无 query | 公共目录、revision、observed_at、complete、unavailable_profiles |
| `GET /v1/market/candles` | instrument_id、interval，可选 limit | 历史 candles 与分页证据 |
| `GET /v1/market/snapshot` | instrument_id、topic，以及 topic 需要的 depth/interval | 与 WS 共用的完整快照 |
| `GET /v1/market/ws` | 无 query；升级后发送 Control | ack、snapshot、delta、full、resync_required、error、pong |

先从目录取得 `instrument_id`，再原样传回。它包括 venue、environment、product、
settlement 和规范化 symbol，但调用方应把它当成不透明标识。HIP-3 等命名空间可能
在 ID 中含 `%3A`，放进 URL query 时仍须正常 URL 编码，不能先自行解码 ID。
目录不包含账户标识、交易所凭证或原生句柄。

目录返回全部已知商品；部分目录失败时，健康范围继续返回，`complete=false`，
缺失范围列在 `unavailable_profiles`。过期目录中的商品仍返回 `ready=false`。
这些信息不是下架证明，也不等同于实时行情停止；已知商品可继续读取公开行情。
全部目录不可用返回 503。`observed_at` 是最早一份参与目录的观察时间；`revision`
标识目录内容与覆盖，不应作为所有健康状态的唯一变化标志。

REST 错误沿用公开 API 的错误 envelope 和 correlation_id：非法输入 400、在有效
目录中找不到商品 404、明确不支持 501、上游或共享快照不可用 503。原生限流仍沿用
429 / Retry-After 语义。一个目录不可用或已过期时，不会用 404 声称它不存在某商品。

## 最小接入示例

以下命令只读行情。`GATEWAY_API_URL` 应指向 Gateway 的公共监听地址，
`GATEWAY_MARKET_READ_TOKEN` 是已配置的行情只读 token。示例不会创建账户或发出交易请求。

```sh
curl --fail-with-body \
  -H "Authorization: Bearer $GATEWAY_MARKET_READ_TOKEN" \
  "$GATEWAY_API_URL/v1/market/instruments"

curl --fail-with-body --get \
  -H "Authorization: Bearer $GATEWAY_MARKET_READ_TOKEN" \
  --data-urlencode 'instrument_id=binance:live:perp:USDT:BTC-USDT' \
  --data-urlencode 'topic=market.order_book' \
  --data-urlencode 'depth=20' \
  "$GATEWAY_API_URL/v1/market/snapshot"

curl --fail-with-body --get \
  -H "Authorization: Bearer $GATEWAY_MARKET_READ_TOKEN" \
  --data-urlencode 'instrument_id=binance:live:perp:USDT:BTC-USDT' \
  --data-urlencode 'interval=1m' \
  --data-urlencode 'limit=100' \
  "$GATEWAY_API_URL/v1/market/candles"
```

WS 客户端连接 `/v1/market/ws` 并携带相同鉴权 header，发送：

```json
{
  "op": "subscribe",
  "requestId": "watch-btc",
  "topics": [
    {"topic": "market.bbo", "instrumentId": "binance:live:perp:USDT:BTC-USDT"},
    {"topic": "market.mark_price", "instrumentId": "binance:live:perp:USDT:BTC-USDT"}
  ]
}
```

每个 topic 的 ack 返回不同 `subscriptionId`，用它关联后续消息以及原始请求中的
instrument。ack 只表示分配了订阅标识，首次 snapshot 才表示该 topic 有数据。

```json
{"op":"ping","requestId":"heartbeat-1"}
```

```json
{"op":"unsubscribe","requestId":"stop-btc","topics":[{"subscriptionId":"market-1"}]}
```

REST 参数采用现有公开 API 的 snake_case，WS 复用现有 realtimewire 的 camelCase，
不创建第二种流协议。精确定义位于 `api/exchange-gateway/v1/openapi.yaml` 的
MarketControl、MarketTopicRequest、MarketMessage 及各 REST response schema。

## Topic 与恢复规则

| Topic | 参数 | 数据语义 |
| --- | --- | --- |
| `market.order_book` | instrumentId、depth（1–100） | snapshot/full 替换可见盘口，delta 只修改指定价位，数量 0 删除价位 |
| `market.bbo` | instrumentId | bid/ask 为完整最新值；更新覆盖旧报价 |
| `market.candles` | instrumentId、interval | snapshot 为 candles 数组，更新为 candle，按 openTime 更新对应蜡烛 |
| `market.mark_price` | 永续 instrumentId | 替换当前 price，并保留接收时间与年龄 |

K 线 interval 范围为 `1m/5m/15m/30m/1h/4h/12h/1d`，历史 limit 为 1–500，
默认 100。不同交易所仍可能不支持某个具体产品、topic 或参数，不会自动降级为
另一个数据来源。特别是无法从原生 WS 建立盘口的交易所，不会偷偷拼 REST 盘口。

每个连接最多 64 个活跃订阅，单个控制消息最多 64 个 topic、64 KiB。`cursor` 与
`internalPurpose` 不接受非空值；账户事件、资费看板和内部系统 topic 不在此入口。
新增应用按现有普通需求优先级进入 hotset，不能伪装成 execution/position 抢占容量。
无效公共 topic 的整个订阅批次返回 `INVALID_TOPIC`，不分配任何订阅。无效 JSON、
无效 Control 结构或超出控制帧限制会断开连接。

消费者应执行以下规则：

1. 状态以 `subscriptionId + generation` 隔离。snapshot 建立或替换状态。
2. 只在已有同代 snapshot 后处理 delta/full。`sequence` 是共享 topic 在该代内
   的本地顺序，`sourceSequence` 是不透明的交易所序号，不是持久化事件游标。
3. 收到 `resync_required / UPSTREAM_GAP`，立即废弃旧状态，等待同一逻辑订阅的
   新一代 snapshot；Hub 会自动恢复该订阅，不需要另开一份采集。
4. `UPSTREAM_UNAVAILABLE` 表示该逻辑订阅已终止，应有界退避后重新订阅。
   `INSTRUMENT_UNKNOWN`、`UNSUPPORTED_TOPIC` 是明确错误，应修正请求或商品选择。
5. 消费者落后会触发重同步或断开连接，最后一条通知可能来不及送达。因此任意
   断连都必须清空本地连续性状态，再连接、再订阅、再等 snapshot。
6. 退订后忽略该 subscriptionId 的剩余在途消息。不要把不同连接的相同订阅 ID
   或不同进程中的相同 generation 当成同一段连续数据。

该接口没有跨进程、跨重连的持久重放能力。快照 REST 只临时订阅现有 Hub，收到
首个完整快照便释放；遇到 gap 或无法取得快照时返回失败，不把缺失盘口表示为空。

## 新鲜度与数据质量

行情静默与控制连接活着是不同事实。pong 不能证明价格仍在更新，HTTP 200 也不
自动证明数据足够新；监控必须为每个数据项设置新鲜度条件。

标记价原先把 `ageMs` 固化进缓存，晚订阅者可能再次收到旧的 `ageMs=0`。本批在
原始行情数据中加入 `receivedAt`，公共与内部流投递时据此重新计算 `ageMs`，不会
改变原始观察时间。`staleAfterMs` 为该数据源的既有新鲜度窗口；客户端接收后继续
让数据年龄随本地时间增长。此修复也覆盖内部浏览器行情流，无需新建行情缓存。

盘口/BBO 的 observedAt 保留来源事件时间；K 线内容同时包含 openTime/closeTime，
不能把尚未结束蜡烛的 closeTime 当成“刚收到一次报价”。在时钟偏差、稀疏成交等
情况下，应用还应独立观测数据到达间隔。数据不足或断流应成为监控状态，而不是
被替换成 0、空仓或“市场没变化”。

## 删除、装配与后续边界

- 删除 `InternalPublicMarketStreams` 及 InternalOptions.PublicMarkets。它没有参与
  采集，仅被拿来做非空判断，使 HTTP 层看似需要原始 adapter 流能力。
- 公共和内部 WS 共用一个会话处理器和一个 Hub 订阅消费者，保留各自的 topic
  范围与活跃订阅限额。HTTP handler 无法绕过 Hub 直接打开原生流。
- 按 instrument ID 的明确目录范围定位商品，避免为了 Bybit USDT 商品串行探测
  其他交易所或 USDC 范围。单个交易所故障不会拖慢不相关商品查询。
- 修复竞态检查暴露的衍生品采集生命周期问题：关闭会取消并等待 Run 退出，
  不再一边复制可变 generation 一边启动采集；拒绝重复启动、关闭后启动和关闭后
  新增热点需求，并发 Close 共用同一次关闭及其结果。
- 生产入口将现有 Source、MarketHub 和 Hotset 注入公开 HTTP handler；不依赖
  internal listener 是否开启，不依赖账户是否存在或订单写 gate 是否就绪。
  migration-validation 模式没有行情源，新入口统一返回 503，不返回伪造空快照。
- 原账户范围行情 API 和内部浏览器路径保留兼容；本批没有迁移调用方、修改数据库
  或恢复已删除的资费套利应用。`MarketHub/MarketKeys/MarketHotset` 接口去掉
  Internal 前缀，`MarketHub` 删除未使用的 Latest 要求。

当前独立应用入口先覆盖四类单商品 topic。公开成交、全市场衍生品字段、资金费
历史的独立应用契约仍未在这批开放。行情只读 token 已由后续批次补齐。告警规则、持久
行情日志和外部通知也不是本批实现内容；后续应继续复用现有事实仓库与数据源。

## 验证

新增本机协议回归验证无账户读取、鉴权优先、参数与 topic 隔离、目录部分失败、
范围定位、公共/内部/REST 共用采集、快照与增量、断流重建、退订释放、活跃订阅
额度与缓存标记价年龄。测试替身没有可用的账户或交易方法，意外调用会直接失败。

最终验证结果：

- `GOCACHE=/tmp/bolter-audit-go-cache go test -short ./...`：99 个含测试的包通过，
  另有 6 个包没有测试文件。
- `GOCACHE=/tmp/bolter-audit-go-cache go test -short -race ./gateway/httpapi ./gateway/publicmarket ./gateway/marketowner ./gateway/markethub ./cmd/exchange-gateway`：5 个包全部通过。
- `git diff --check`：通过。

首次竞态检查发现了上述衍生品采集启动/关闭问题，修复并补充生命周期回归后，
重新完成了全量短测与上述竞态检查。最终日志位于
`output/architecture-review-2026-09-10/market-api-full-final.log` 和
`output/architecture-review-2026-09-10/market-api-race-final.log`。
测试使用本机协议替身，不读取交易所凭证或发送实盘订单；本批未进行实盘验收。
