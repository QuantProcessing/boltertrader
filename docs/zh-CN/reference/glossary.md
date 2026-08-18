# 术语表

> 英文为规范版本。[English](../../reference/glossary.md)

本页拥有 BolterTrader 的规范术语。其他页面使用这里的含义并链接过来，而不在本地另立定义。

这些词在别处大多含义更宽松，在这里被刻意收窄。因为「我们实现了它」与「有一次记录在案的运行
证明了它」之间的距离，正是一条可以发布的断言与一条不可以之间的距离。

## 怎么指认一个可交易的东西

五个词按下面的顺序组合成一个标识符。

| 术语 | 含义 | 取值 |
| --- | --- | --- |
| Venue（交易所） | 一家交易所 | `binance`、`okx`、`lighter`、`hyperliquid`、`bybit`、`bitget`、`gate`、`aster`、`nado` |
| Environment（环境） | 该交易所的哪一张网 | `live` |
| Product（产品） | instrument 族 | `spot`、`perp` |
| Settlement asset（结算资产） | 合约以什么结算，用于一家交易所跑着不止一种的情况 | `USDT`、`USDC`、`USDT0` |
| Canonical symbol（规范 symbol） | 与交易所无关的交易对写法 | `BTC-USDT` |

**Native symbol（原生 symbol）** 是交易所自己对同一个 instrument 的叫法——`BTCUSDT`、一个数字
市场索引、一个合约代码。原生 symbol 留在 `exchange/internal/<venue>` 里面，exchange 层之上
不该有任何东西见到它。

**Instrument ID** 是 gateway 给一个可交易物的稳定名字：

```text
venue:environment:product:symbol
venue:environment:product:settlement:symbol
```

`binance:live:spot:BTC-USDT` 与 `nado:live:perp:USDT0:BTC-USDT` 都是合法的。结算段只在某个
交易所需要它来消歧时才出现。最后一段必须是规范 symbol——校验器坚持要那个连字符，正是因为
它是抓住「一个原生 symbol 漏到了上层」最便宜的办法。

Environment 那一段只有一个合法取值。它曾经也收 `demo` 与 `testnet`，而各交易所实现这两者的
方式差异大到那一层被整体删除。这一段本身留了下来，因为这个 ID 是按位置解析的：删掉它不是把
标识符变短，而是把每一个已经写下来的标识符重新解释一遍。

**Route profile** 是 venue、product 与 settlement 解析之后的组合，用形如 `binance.spot.v1` 或
`nado.perp.usdt0.v1` 的 ID 命名。**Route** 是绑定了某个账户凭证的 profile，而 **route ID** 在
一次写入里点名它。

**Mode（模式）** 曾经是一个部署属性、不是市场属性，现在它已经没有了。它比 environment 粗：
`test` 覆盖 `demo` 与 `testnet`，`live` 覆盖 `live`，而跑在其中一种模式下的进程解析不出另
一种的 profile。非生产那一层被删掉之后，这一对里有一边再没有东西可命名了，所以这条轴是被
整个删除的，而不是留下一个只剩单值的轴：数据库、密钥命名空间与 principal 都去掉了那个标签，
服务之间用来担保 mode 的那两个头也不复存在。

**Live 写开关** `BT_LIVE_PRIVATE_WRITES_ENABLED` 是它在运维上的替代物。它不是一种 mode：
它不改变一条 route 通往哪家交易所、也不改变一次写用哪套凭证，而且它的另一侧空无一物。
关着时，每一次私有写都以 `LIVE_PRIVATE_WRITE_DISABLED` 被拒。开着时，一次写就是真金白银
地发往真实交易所。它默认是开的 —— mode 这条轴删掉之后，它是准入检查里仅剩的一个子句，
默认关着就意味着整个栈干干净净地起来，然后拒掉每一笔订单。

## Product 行

**Product 行**是 gateway 对外暴露的一个「交易所 + product」组合，用三个字母的 **product code**
标识。一共二十行，而能力、证据与测试 target 都是按这个单位来数的：

| 交易所 | Spot | Perp |
| --- | --- | --- |
| Binance | `BNS` | `BNP` |
| OKX | `OXS` | `OXP` |
| Lighter | `LIS` | `LIP` |
| Hyperliquid | `HLS` | `HLP` |
| Bybit | `BYS` | `BYU`、`BYC` |
| Bitget | `BGS` | `BGU`、`BGC` |
| Gate | `GTS` | `GTU` |
| Aster | `ATS` | `ATP` |
| Nado | `NDS` | `NDP` |

[REST](exchange-rest-v1-operation-matrix.md) 与
[WebSocket](exchange-ws-v1-operation-matrix.md) 两张操作矩阵各有一列对应一个 code。一条点名了
product 行的断言，指的就是这二十行中的恰好一行——不是整个「Bybit」，也不是「永续」这个类别。

## 身份

| 术语 | 含义 |
| --- | --- |
| Principal | 谁在动作。Auth edge 从会话里推出它并作为 header 注入；服务永远不从浏览器拿它 |
| Account（账户） | 一个已注册的交易所账户：凭证、一个 route profile，以及属于它们的 gateway 状态 |
| Command ID | Trading 服务对一条手动命令的标识符。界面、活动流与命令查询端点用的就是它 |
| Gateway command ID | Gateway 自己的标识符，在它接受一次下单时铸出。与上面是两个命名空间，且不可由上面推导 |
| Gateway order ID | Gateway 持久的订单身份，与交易所之后返回什么无关 |
| Client order ID | 发给交易所的一个正十进制 `uint48` 字符串 |
| Idempotency key | 调用方给出的令牌，让一次重试的提交仍是同一次提交 |
| Write fence | 一次写入所依据的账户修订号、runtime generation、凭证版本与 route ID。带着过期 fence 的写入会被拒绝，而不是被重新解释 |

这两个命令命名空间是本系统里最常见的混淆来源。读 gateway 日志时引用 gateway 的标识符，读界面
时引用 trading 服务的。

## 状态词

大致按「各自允许你断言多少」排序。

| 术语 | 它断言了什么 | 它没有断言什么 |
| --- | --- | --- |
| Implemented（已实现） | 当前代码树里存在所指 surface 的代码 | 有任何东西跑过，或者某个交易所接受过它 |
| Admitted（已接纳） | 该操作在编译期出现在那个 product 行上——即操作矩阵里的 `A` | 交易所会兑现它，或者它被真正执行过 |
| Write-ready（可写） | 能力矩阵标记该行默认接受写入 | 关于某一个具体账户是否就绪的任何事 |
| Offline-conformant | 确定性、免凭证的离线套件有一次记录在案的零跳过运行通过，针对具名的 product 范围、具名的 target、具名的日期 | 该交易所今天仍然可用、主网行为相同，或者它现在仍然通过 |
| Production-ready（生产就绪） | 有独立的运维证据支持所述的生产范围。本仓库从不声明它 | 任何能从上面三行推出来的东西 |
| Deferred（推迟） | 出于明确理由，刻意排除在当前切片之外 | 交易所没有它，或者它被永久否决 |
| Unsupported（不支持） | Normalized surface 不存在，或返回一个 unsupported 错误 | 交易所自己的 API 没有这个功能 |
| SDK-only（仅 SDK） | 存在底层交易所代码，其上没有 product 行 | 任何形式的 normalized 覆盖 |

单独的 **supported** 与单独的 **certified** 不是状态。先点名 product 行与 surface，再从上表里
挑一个词。

## 证据词

| 术语 | 含义 |
| --- | --- |
| Acceptance harness（验收装置） | 一个 opt-in 的测试，在一道具名写闸门之后，对交易所做一次有界的真实交互。这次交互是真的——它背后没有非生产 target 了。它不是可复用的交易应用 |
| Zero-skip | 这次运行至少执行了一个测试，且一个都没跳过。skip 就是失败——缺凭证绝不能被读成通过 |
| Clean final state（干净终态） | 在精确的 symbol、product、account 与装置自有订单范围内的终态断言，而不是关于整个账户的陈述 |
| Ambiguous outcome（不明确的结果） | 交易所可能已经收到了这次写入，但没有权威结果能证明。自动重试不安全 |
| Open-only caveat | 一次完整的挂单读取能证明某单已不再挂着。它造不出终态原因，也重建不了缺失的成交 |
| Public-safe（可公开） | 适合放进这棵树：无凭证、无账户残留、无本地路径、无原始日志，且每条断言都由当前源码拥有 |

## 层的词

| 术语 | 含义 |
| --- | --- |
| SDK | `sdk/<venue>/`：某一家交易所的原生 API，形状照着那家自己的文档 |
| Exchange | `exchange/`：这些 SDK 之上与交易所无关的类型化 API。它本身就是一个入口，不是内部细节 |
| Gateway | `gateway/`：共享能力面——账户、凭证、catalog、能力与限额解析、持久订单身份、协调、私有事件 |
| 公开面 | Gateway 的 `/v1` surface，供各产品服务调用 |
| 内部面 | Gateway 的 `/internal/v1` surface，接受凭证材料，绝不挂在公网 |
| Service（服务） | `services/<name>/`：一个产品在 gateway HTTP surface 之上的行为。Trading 服务与 auth edge 是这两个 |
| Auth edge | `services/authedge/`：会话终结与按路由表转发。它不做任何聚合 |
| Web | `web/`：唯一的前端，与 API 同源提供 |

这些层之间怎么依赖见[架构](../concepts/architecture.md)，产出证据的那些 target 见
[测试](testing.md)，而这些词当前挂在什么东西上见[不支持的能力](unsupported.md)。
