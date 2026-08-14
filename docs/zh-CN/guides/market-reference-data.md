# 市场与参考数据

> 英文为规范版本。[English](../../guides/market-reference-data.md)

有两样不同的东西都顶着「市场数据」这个名字，而把它们混为一谈，通常正是图表加载不出来
或者价格不动的原因。

**参考数据**描述一个 instrument **是什么**：它的 tick size、lot size、最小名义额，以及
该 venue 是否支持某种订单类型。它很少变化，以 Catalog snapshot 的形式加载。

**市场数据**描述一个 instrument **正在做什么**：盘口、深度、mark price、funding。它时刻在
变，并且从不被存储。

两者穿过的是同一条边界。浏览器问 trading 服务，trading 服务问
[gateway](exchange-gateway.md)，而只有 gateway 持有 venue client。gateway 之上的任何一层都
不知道原生 symbol 长什么样。

## 参考数据

Gateway 在启动时按 account 加载 Catalog 并持续刷新。它提供 `/v1/instruments`、`/v1/rules`、
`/v1/capabilities` 和 `/v1/risk-limits`。

Trading 服务把界面需要的部分重新发布为 `/api/v1/catalog/venues` 与
`/api/v1/catalog/instruments`。Instrument 标识全程使用规范的分段形式——venue、
environment、product、可选的结算资产，然后才是规范 symbol——绝不使用某个 venue 自己的 symbol
字符串。见[Account 与 instrument](../concepts/accounts-instruments.md)。

`/v1/risk-limits` 给的是诚实的答案而不是近似的答案：交易所风控限额没有被规范化，于是它报告
「不可用」并说明原因。它是一个附了理由的占位，而不是一份你可以据以交易的限额表。

规则在知道它的地方被执行，也就是 gateway 这一层，以及 venue 那一层。下单票在封签预览之前
就用它们做校验，所以操作者是在编辑过程中看到「低于最小名义额」，而不是确认之后才收到一个
拒绝。

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

`GET /api/v1/candles` 服务于图表。它接受同样的 instrument 坐标，外加一个周期和一个上限，
最多返回五百根 K 线——这与 gateway 自己的上限一致，所以更长的一页就说明它不是来自一个对上限
有共识的 gateway。

每根 K 线以字符串携带 open/high/low/close/volume，带自己的开盘与收盘时间，以及一个
`complete` 标记。实时序列的最后一根是未完成的，它还会变；把它画成最终值，正是图表看起来与
行情条对不上的原因。

周期刻意不属于市场数据的缓存键。把它折进去会让一个 instrument 占据八个缓存条目，而这些条目
的新鲜度规则彼此并不一致。

## 实时更新

上面两者都不由界面轮询。一条 server-sent 流 `/api/v1/realtime/stream` 承载已订阅内容的更新。
在它下面供给数据的是 gateway 自己的 `/v1/public/ws`，而那条流按设计没有 cursor replay：每个
连接都从一份全新的权威 snapshot 开始，遇到 venue gap 时发出 `resync_required` 并关闭，而不是
默默跳过。

## 什么在哪里可用

并非每个 venue 都提供每一种读取，而且这些差异是被记录下来的，不是运行时才发现的：

- [REST 操作矩阵](../reference/exchange-rest-v1-operation-matrix.md)
- [WebSocket 操作矩阵](../reference/exchange-ws-v1-operation-matrix.md)
- [Venue 与 account mode](../reference/exchange-venues-and-account-modes.md)

那两张矩阵里的 `N/A` 是刻意的推迟，不是疏漏。见[不支持的能力](../reference/unsupported.md)。
