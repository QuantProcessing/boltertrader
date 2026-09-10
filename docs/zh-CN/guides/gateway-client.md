# Gateway Go 客户端

> 英文为规范版本。[English](../../guides/gateway-client.md)

`clients/gateway` 让 Go 应用直接使用 Gateway，不依赖 Trading、HTTP handler、交易所
适配器或 SDK。共享协议类型位于 `api/exchange-gateway/v1`，与 OpenAPI 放在一起。
公共行情需要 Gateway 服务 token，无需交易所账户或交易所凭证。添加账户继续使用现有
登录与设置页面。

## 只读示例

按[服务指南](exchange-gateway.md)启动 Gateway，通过 `GATEWAY_TOKEN` 提供能力接口
的服务 token，然后运行：

```sh
go run ./cmd/gateway-market -gateway http://127.0.0.1:18080
```

该命令列出公共标的目录。从结果中选择 `instrument_id`，再读取或订阅 BBO：

```sh
go run ./cmd/gateway-market -instrument 'binance:live:spot:BTC-USDT'
go run ./cmd/gateway-market -instrument 'binance:live:spot:BTC-USDT' -stream -duration 1m
```

默认最多运行 30 秒，Ctrl-C 会关闭订阅。示例只读行情，不加载交易所凭证，不提交订单。
目录内容取决于当前 Gateway 的公开行情连接。

需要持久运行的应用，可使用同一订阅能力构建的独立[价格与断流监控](gateway-monitor.md)。

## 在应用中使用

```go
import gateway "github.com/QuantProcessing/boltertrader/clients/gateway"

client, err := gateway.New(gateway.Config{
    URL:   "http://127.0.0.1:18080",
    Token: gatewayToken,
})
if err != nil { return err }

snapshot, err := client.MarketSnapshot(ctx, gateway.MarketTopic{
    Topic:        gateway.BestBidOffer,
    InstrumentID: instrumentID,
})
if err != nil { return err }
quote, err := gateway.DecodeBBO(snapshot.Snapshot)
```

价格、数量和费用保持十进制字符串，运算使用任意精度 decimal。标的 ID 视作不透明
身份，不从原生 symbol 拼接，也不自行解码其中的 `%3A`；客户端负责传输编码。

| 方法 | 范围 |
| --- | --- |
| `Accounts`、`Account`、`AccountConnection` | 已添加账户、路由和本地连接状态 |
| `Balances`、`PositionSnapshot` | 当前余额、持仓和明确的数据新鲜度 |
| `PlaceOrder`、`Order`、`OrderByKey`、`CancelOrder`、`Orders` | 订单身份、限定范围的查询和显式交易操作 |
| `Fills` | 遍历指定账户/标的/订单的成交；保留费用正负号与原币种 |
| `MarketCatalog`、`MarketCandles`、`MarketSnapshot` | 公共目录、有限历史 K 线和共享快照 |
| `SubscribeMarkets` | 在同一 WebSocket 中订阅盘口、BBO、K 线和标记价格 |

账户目录和连接查询目前仍位于 `/internal/v1`。Gateway 分开监听时，同时配置
`InternalURL` 和 `InternalToken`；其他能力使用普通监听地址。两种 token 都不是
交易所 API key。账户添加、删除、权限管理和私有账户 WebSocket 不属于本批客户端接口。

## 错误、交易与恢复

HTTP 请求与 WebSocket 握手默认超时 30 秒，响应/消息默认最多 8 MiB，可通过
`Timeout`、`ResponseLimit` 配置。客户端不跟随重定向。响应允许新增字段，但拒绝
错误 JSON、重复字段、身份缺失、串账户/订单响应和未知订单状态。

`*gateway.Error` 提供 HTTP 状态、服务错误码、安全消息、交易所错误码、关联 ID、
重试提示和请求交接结果。默认错误字符串不打印响应文本，展示时可以读取结构化字段。
取消与超时仍可通过 `errors.Is` 识别。

- 发送前拒绝或取消：`not_attempted`。
- HTTP 明确拒绝（除 408 外的 4xx）：`rejected`。
- 网络异常、不明确的 HTTP 响应或无效成功响应：`unknown`。
- 查询失败始终是错误；`retryable` 或 `RetryAfter` 不能用来自动重发订单。

客户端没有自动写重试，也禁止 HTTP transport 自动重放带幂等键的 POST。一次下单
意图使用一个固定键。响应丢失时，通过 `OrderByKey` 携带原键、账户和标的查询；404
不代表原请求绝不可能完成。`prepared`、`dispatching`、`uncertain` 也仍需查询确认。

撤单前先查询并核对不可变的订单、账户与标的身份；不匹配时不会发出 DELETE。客户端
不自行推导成交，不产生补单或替代订单。

## 分页与数据新鲜度

订单列表返回有界页和游标。成交最多遍历 100 页，每页 100 条；遇到游标循环或任何错误，
不返回可能被误认为完整证据的部分结果。余额接口改为完整读取，要求响应包含 `complete: true`。最多返回 10,000 条非零余额，
超过上限明确报错，不再分页或静默截断。各路由依次读取，不表示跨交易所原子快照。

持仓提供 `freshness`、`verified_at` 和逐条观测时间。账户不可用且没有数据，不代表
已确认空仓。行情保留原始 `observedAt`；HTTP 成功或 pong 都不能证明行情新鲜。
可接受的数据年龄由应用决定。历史 K 线只读取一个有限窗口，`Page` 描述来源完整性。

## 订阅行为

`SubscribeMarkets` 返回最多包含 64 个固定主题的 `MarketStream`。读取 `Events()`，
通道关闭后检查 `Err()`；结束时调用 `Close()`。事件同时包含请求的标的与服务端消息。

- 首次 `snapshot` 后才接受更新；逐订阅检查 generation/sequence，忽略重复更新。
- BBO 更新包含完整报价，Gateway 每 50 毫秒最多推送一次增量，允许序号递增但跳号。
  该流不保证保留每个原生报价；订单簿增量仍要求序号连续。
- 上游缺口发送 `resync_required`，旧状态失效，等待新 generation 快照。
- 断线或非 BBO 主题序号缺口会通知缺口，按有界退避重新连接订阅，默认间隔 1–30 秒；不重放丢失 tick。
- 持续 pong 却不确认订阅或不提供初始快照的连接同样受客户端超时限制；之后的数据陈旧
  判断由应用负责。
- 心跳检测无声断线。不支持的主题、协议异常或鉴权失败会以错误结束。
- 消费过慢时以 `ErrConsumerLagged` 结束，剩余缓冲数据不能视作完整流；应用可以重新订阅建快照。
- 关闭或取消上下文时关闭连接，释放该连接上的全部服务端订阅。

## 验证

```sh
make test-gateway-client
go test -race -short ./clients/gateway ./gateway/httpapi ./services/trading/gateway
```

测试使用本地假交易所、真实 Gateway handler 和真实 Market Hub，覆盖协议兼容、身份、
精度、错误结果、响应丢失、分页、重连、generation 缺口、心跳、慢消费者和关闭。
没有使用真实凭证，不代表真实交易所验收完成。
