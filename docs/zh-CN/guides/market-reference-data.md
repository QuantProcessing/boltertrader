# 市场与参考数据

> 英文为规范版本。[English](../../guides/market-reference-data.md)

有两样不同的东西都顶着「市场数据」这个名字，而把它们混为一谈，通常正是图表加载不出来
或者价格不动的原因。

**参考数据**描述一个 instrument **是什么**：它的 tick size、lot size、最小名义额，以及
该 venue 是否支持某种订单类型。它很少变化，以 Catalog snapshot 的形式加载。

**市场数据**描述一个 instrument **正在做什么**：盘口、深度、mark price、funding。它时刻在
变，并且从不被存储。

两者终止在同一条边界。浏览器经 Auth Edge 直达 [gateway](exchange-gateway.md)，而只有
gateway 持有 venue client。Trading 消费同一批 Gateway 事实来生成业务投影，但不再充当浏览器
代理。gateway 之上的任何一层都不知道原生 symbol 长什么样。

## 参考数据

Gateway 直接按每个受信 route profile 加载并刷新公开 Catalog；这个发现过程不需要账户，也不需要
凭证。之后配置账户只会增加交易资格，不会增加 instrument。Gateway 还提供账户范围内的
`/v1/rules`、`/v1/capabilities` 和 `/v1/risk-limits`。

Auth Edge 将 `/api/v1/catalog/instruments` 转发到 Go 客户端使用的同一
`/internal/v1/catalog/instruments` 契约；`data` 包含 `revision`、`observedAt` 与
`instruments`。凭证表单从 `/api/v1/products/manual/venues` 获取交易所描述。Instrument 标识全程使用规范的分段形式——venue、
environment、product、可选的结算资产，然后才是规范 symbol——绝不使用某个 venue 自己的 symbol
字符串。见[Account 与 instrument](../concepts/accounts-instruments.md)。

### Instrument 身份边界

输入身份和输出格式各自只有一套：

- 浏览器或 product API 只提交 `instrumentId`，不再并列提交 `venue`、`product`、
  `settlement` 或 `symbol`。Gateway Catalog 负责解析这些事实，因此不可能在一个字段写 Binance、
  另一个字段却写 OKX 合约。
- Gateway 的公开 instrument directory 把这个 ID 映射到唯一的 route profile，以及交易所的
  `nativeId`/`nativeSymbol`。原生身份到 Gateway 为止，绝不再根据展示 symbol 猜回来。
- 响应保留 `instrumentId` 用于关联、命令和审计。界面有结构化事实时显示
  `baseAsset` + `quoteAsset`，否则统一由规范 ID formatter 降级解析。原始 ID 只作为诊断 title，
  不作为主要标签。
- 账户只增加可交易性，不负责创造 instrument，也不负责判断它是否上架。因此
  `account_unreachable` 与 `instrument_identity_unresolved` 是两种独立状态。

Instrument picker、行情、K 线、资金费历史、手工下单、持仓、订单和成交都遵守这份
契约；任何消费方再单独添加 symbol parser，都属于越过身份边界。

`/v1/risk-limits` 给的是诚实的答案而不是近似的答案：交易所风控限额没有被规范化，于是它报告
「不可用」并说明原因。它是一个附了理由的占位，而不是一份你可以据以交易的限额表。

规则在知道它的地方被执行：浏览器负责即时提示，Trading 在接受命令时校验，Gateway 与 venue
再次作为最终权威。需要可执行价格才能判断的市价单规则仍由 venue 确认，系统不会为了允许提交
而增加一次阻塞式报价请求。

## 市场数据

`GET /api/v1/market-data` 为每个 instrument 返回一份 fact：带数量的最优买卖价、每侧至多二十
档深度；对永续还包括 mark price、当前 funding rate 及其观测时间与下次 funding 时间、至多二十
个历史 funding 点，以及 open interest。在现货 instrument 上，永续那几个字段是缺席，而不是空值。

每一个小数从 venue 一路到浏览器都是字符串。这条路径上没有任何地方会把价格解析成浮点数。

有三个性质值得知道，因为它们能解释你在压力下会看到什么：

**它带 TTL 缓存。** 缓存键是 venue、environment、product 和 instrument。到手时就已超过 TTL、
或者时间戳在未来的 fact，会被判为陈旧而拒绝，而不是照样提供。

**并发读会被合并。** 十个浏览器标签页在同一刻请求同一个 instrument，只会产生一次 gateway
取数，而不是十次。其余的等在上面。

**取数失败会驱逐。** 这里没有「出错就给陈旧值」这条路径。venue 读失败时，该条目被移除，调用
方拿到错误——一个悄无声息地已经过时几分钟的价格，比一个看得见的缺口更糟，因为有人会拿它去
交易。

读取还按 venue 在固定窗口内做预算。窗口用尽时返回限流错误而不是排队，剩余预算是可观测的，
因此一个正在被打爆的栈会明说，而不是悄悄降级。

## K 线

`GET /api/v1/candles` 服务于图表。它接受同样的规范 `instrumentId`，外加一个周期和一个上限，
最多返回五百根 K 线——这与 gateway 自己的上限一致，所以更长的一页就说明它不是来自一个对上限
有共识的 gateway。

每根 K 线以字符串携带 open/high/low/close/volume，带自己的开盘与收盘时间，以及一个
`complete` 标记。实时序列的最后一根是未完成的，它还会变；把它画成最终值，正是图表看起来与
行情条对不上的原因。

周期刻意不属于市场数据的缓存键。把它折进去会让一个 instrument 占据八个缓存条目，而这些条目
的新鲜度规则彼此并不一致。

## 资金费率

资金费出现在两个 surface 上，而它们不是同一个读取。

`/api/v1/market-data` 上那几个 funding 字段——当前费率、它的观测时间与下次 funding 时间，
以及上面说的至多二十个历史点——属于某一个公开 instrument snapshot。它们与盘口、K 线走同一条
无凭证 route，和有没有配置账户无关。

`GET /api/v1/funding-board` 与 `GET /api/v1/funding-history` 则完全不需要账户。Gateway 是用
不带凭证构建出来的交易所 client 回答它们的，所以在一个账户都还没声明之前它们就解析得出。
看板是整张跨所矩阵——按 venue、product 与结算资产各占一列——每列带着自己的当前费率、结算
周期，以及该交易所提不提供历史。历史输入是一个规范 `instrumentId` 和一个窗口，只有 `7d` 与
`30d` 两档；Gateway 先把 ID 解析成精确的 route profile 和交易所原生地址，再调用交易所。
开放区间意味着要对交易所接口翻页，而这个读存在的意义就是只做一次调用。

各家公布出来的费率不能直接比，因为它们的结算节奏本就不同。任何要给它们排名的东西，都要先
归一成日率。这段算术，以及它是干什么用的，在 [trading 服务](trading-service.md)里。

这里有两种「没有」是承重的。没有历史 surface 的交易所会把序列报成「不可用」，而不是回一条
空序列——「我们不留」和「当时就没有」是两个不同的答案，只有其中一个才是不必再问的理由。而
资金费实际收付了多少的按账户流水，又是第三个 surface，只在四个 product 行上解析得出；见
[不支持的能力](../reference/unsupported.md)。

## 实时更新

公共行情、私有账户和 Trading 订单投影复用浏览器的 `/api/v1/realtime/ws`。底层行情与账户流仍由
Gateway 拥有。最新值 topic 首帧为快照；订单簿使用原生增量/full，gap 后重新快照。指数价与标记价
保留各自来源时间和新鲜度，指数订阅同时携带同一衍生品事实中可用的资费、持仓量等信息。
账户事件不回放历史：重连时重读账户快照，并保留 60 秒 REST 修复兜底。见 [Web 应用](web-app.md)。

## 什么在哪里可用

并非每个 venue 都提供每一种读取，而且这些差异是被记录下来的，不是运行时才发现的：

- [REST 操作矩阵](../reference/exchange-rest-v1-operation-matrix.md)
- [WebSocket 操作矩阵](../reference/exchange-ws-v1-operation-matrix.md)
- [Venue 与 account mode](../reference/exchange-venues-and-account-modes.md)

那两张矩阵里的 `N/A` 是刻意的推迟，不是疏漏。见[不支持的能力](../reference/unsupported.md)。
