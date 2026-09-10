# SDK 错误语义与实际 HTTP 限流

本批落实 F10：将请求预算接入 SDK 真正发送 HTTP 的位置，保留错误类型和上游状态，并纠正 Gateway 把普通传输错误直接当成订单拒绝的问题。

## 边界与删除

- `sdk/<venue>` 负责发出原生请求、解析原生错误；不负责 Gateway 进程内的共享配额。
- `internal/venuehttp` 是小型 HTTP 协议公共包：携带 venue、识别具体请求动作及成本、保存 HTTP 状态与等待时间。它不管理账户、订单状态或调度任务。
- `exchange/internal/factoryclient` 将 SDK 错误归一化，保留 `HTTPStatus`、`RetryAfter`、安全错误码和必要的未发送证明；原始错误链止于此层。
- `gateway/ratelimit.Transport` 拥有共享预算，生产入口创建一次，再注入公共行情、资金费率看板、连通性探测及账户构建器。账户重建及密钥轮换继续使用同一预算实例；账户接口桶按稳定的本地 account ID 区分，不使用密钥作为身份。
- 删除未接入生产的 `RequestVectorProvider`、public/private scope 包装和阻塞式 `Acquire`。删除 `MarketCoordinator` 中每次包装调用固定扣一次的 1200/600 虚拟配额，它只保留查询合并、应用错误冷却及观测。
- 不再重复配置一个与交易所总额度完全相同、永远无法单独起作用的账户分钟桶。有效维度是交易所共享额度和接口额度；私有客户端的接口桶区分本地账户。
- Binance 在注入共享 HTTP transport 后关闭 SDK 独立限流器，避免重复扣减和双重等待；独立使用 SDK 时保留原行为。catalog、资金费率读取也走这一入口。

Gateway 的 SDK 禁止导入规则仍有效。只有 `ratelimit` 和 `connectivity` 可以使用 `internal/venuehttp`；业务 handler 和应用不能借此调用交易所 SDK。

## 错误与订单结果

HTTP 429、418、5xx、解码错误与原生业务错误分别保留证据。Bybit 的 HTTP 200 / `retCode=10006`、Bitget 的 `code=42900` 不再退化成普通字符串。OKX 的顶层业务错误在持有 HTTP 响应时解析。Lighter 原先直接解码 JSON 的 GET 路径补上 HTTP 状态检查；Nado 的 GET/POST 查询路径都保留响应元信息。

SDK 仍允许 `errors.As` 读取原生错误。本次 HTTP 错误包装的文本不输出响应正文、请求查询串或底层 transport 的任意文本；exchange 错误不携带原生错误链。不要把 SDK 原生结构直接序列化给上层。

| 证据 | Gateway 下单处理 |
| --- | --- |
| 明确的原生拒绝，且 acknowledgement 身份与内容有效 | `rejected` |
| 下单请求本身被本地预算拦截，尚未发送 | `rejected`，保留幂等结果 |
| 下单后的查询被本地预算拦截 | `uncertain`，继续对账 |
| 5xx、响应丢失、超时、解码错误、未知异常或 ambiguous acknowledgement | `uncertain`，建立恢复任务并关闭账户写入 gate |

`CommandNotSent` 只在具体 wire action 与下单/撤单操作相符时产生，不能因为函数返回本地限流错误就推断整个订单用例没有副作用。Bybit/Bitget 的原生错误码即使看起来像参数拒绝，若 HTTP 状态为 5xx，也不能作为未成交证明。

本批没有增加自动下单重试。相同幂等请求仍返回已有结果；未知结果恢复只读对账。撤单只有取得有效取消确认才进入 canceled，否则仍沿原有对账路径处理。

## 本地请求预算

以下是此进程的默认运行上限，使用固定窗口；它们不是完整的交易所官方 IP/UID 配额模型。数值集中在 `gateway/ratelimit/transport.go`，本批没有增加动态配置系统。

| venue | 每分钟本地成本单位 |
| --- | ---: |
| Binance、Aster、Bybit | 1200 |
| Bitget、OKX、Gate、Hyperliquid | 600 |
| Lighter、Nado | 300 |

普通请求最多使用共享额度的 80%，剩余 20% 留给已识别的撤单请求。接口默认每秒 10 次，Lighter/Nado 每秒 5 次；Bybit cancel-all 每秒 1 次，fee-rate 和 set-margin-mode 每秒 5 次。撤单仍受接口额度和远端冷却约束。

Binance 的 depth、K 线、批量 ticker、订单查询等按端点和参数计算成本；未知 GET 端点暂按 20 个本地单位计费。Hyperliquid 区分 info 与 exchange：低成本查询为 2、普通 info 为 20、userRole 为 60；已知最多 2000 条的成交/历史订单查询预扣 120，candleSnapshot 预扣 105。批量写入按数组规模计算。预扣上限不会在收到小响应后退款。Lighter 从 multipart 的 `tx_type` 识别下单与撤单，不消耗或改写待发送请求体。未识别的动作不产生“订单未发送”的证明。

额度不足立即返回本地限流错误及剩余等待时间，避免已签名请求在 HTTP 层排队过期。维度扣减在同一锁中原子完成，不会出现接口桶拒绝但共享桶已经扣减。重复配置不清空已消耗额度。

HTTP 429/418 使用 `Retry-After` 建立交易所共享冷却，缺失时采用 1 秒；支持秒数和 HTTP-date。Bybit 的剩余额度为零时使用 reset timestamp 冷却对应接口，原生 10006 错误也保留该时间。并发响应只能延长冷却，不能缩短。Gateway 的 429 响应按整秒向上取整输出可选 `Retry-After` header；该 header 不改变订单重发规则。

规则参考：[Binance Spot REST 市场数据](https://developers.binance.com/en/docs/catalog/core-trading-spot-trading/api/rest-api/market)、[Binance USD-M REST 市场数据](https://developers.binance.com/en/docs/catalog/core-trading-derivatives-trading-usd-s-m-futures/api/rest-api/market-data)、[Bybit 限流规则](https://bybit-exchange.github.io/docs/v5/rate-limit)、[Hyperliquid 请求权重](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/rate-limits-and-user-limits)、[Hyperliquid 返回条数](https://hyperliquid.gitbook.io/hyperliquid-docs/for-developers/api/info-endpoint)。核对日期：2026-09-10。

## 范围与后续校准

覆盖当前 Gateway 的九家交易所 REST 调用。此处不覆盖 WebSocket 握手/消息额度、多个 Gateway 进程、其他程序共享出口 IP、交易所 UID 对应多个本地 account 的合并额度，或交易量相关的写入配额。Binance 特殊历史/批量端点、Hyperliquid 部分按返回条数增加的权重、其他 venue 的不同账户档位仍需要继续校准；当前固定窗口也不等同官方滚动窗口。不能据此保证永远不会收到交易所限流。

这批先保证实际请求经过预算、限流信息能够向上传递、未知执行结果不会被误判；没有为个人单进程平台引入分布式配额协调系统。

## 验证

最终 `go test -short ./...`：99 个 package 报告通过（包含缓存），退出码 0。`-race` 覆盖 10 个包：venuehttp、ratelimit、order、account、connectivity、httpapi、factoryclient、Bybit SDK、Bitget SDK、Lighter SDK，全部通过。`git diff --check` 通过。

最终日志分别为 `f10-full-final.log` 和 `f10-race.log`。验证命令与结果见本次任务的 `output/architecture-review-2026-09-10/f10-*.log`。回归覆盖九家交易所真实 SDK/loader 到内存 HTTP transport 的 429/503 链路、原生 HTTP 200 限流、签名体不被消耗、跨客户端共享与并发额度、冷却不缩短、撤单预留、账户重建、分层导入规则，以及订单不确定状态在恢复后仍不重发。

本批没有数据库迁移，不变更订单持久化结构或前端。测试不连接交易所私有接口；默认短测跳过未配置独立 DSN 的数据库验收，不等同于实盘验收。
