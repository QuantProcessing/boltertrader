# Trading 服务

> 英文为规范版本。[English](../../guides/trading-service.md)

Trading 服务是装产品的地方。目前有两个：**手动交易**，一次下一笔；以及**资金费套利**，
一次在两家交易所各下一笔。两者站在同一套命令账本、同一套预留、同一个 reconciler 和同一
组投影之上——所谓一个产品，是一组端点加一种意图形状，而不是把这套机器再抄一份。

服务不持有任何 venue 凭证，也不建立任何 venue 连接：每一次写都经过
[交易所网关](exchange-gateway.md)。

它从不直接面向浏览器。请求由 [auth edge](auth-edge.md) 转发而来，携带服务令牌和
已认证的 principal。

## 命令账本

任何可能抵达 venue 的意图，在被尝试之前先被写下来。命令创建时是 `accepted`，并在
一组固定状态之间迁移：

| 状态 | 含义 |
| --- | --- |
| `accepted` | 已记录、已预留，尚未交给 gateway |
| `submitted` | 已交接；现在由 gateway 持有 |
| `uncertain` | 交接的结果未知 |
| `filled` / `canceled` / `failed` | 终态 |

迁移是被枚举出来的，不是靠推断：`accepted` 可以去往任何状态，`submitted` 不能退回
`accepted`，`uncertain` 可以恢复为 `submitted` 或直接落定。终态不可再变。

`uncertain` 才是这个账本真正的价值所在。当请求已经发出、交接却超时了，服务并不知道
venue 是否接受了这笔订单，而向任何一个方向猜都要付出真实代价：猜「被拒绝」就会下两次
单，猜「被接受」就会报告一笔并不存在的订单。所以它把这份歧义记下来，交给
reconciliation 去用 venue 自己的答案解决。

## 两个标识，两个命名空间

一笔手动订单有两个身份，而且它们不能互换。

**`commandId`** 属于 trading 服务。它是界面上显示的东西，是活动流所用的键，也是
`GET /api/v1/commands/{commandId}` 接受的参数。

Gateway 在接受这次下单时会铸造**它自己的** command 标识，以及其下的持久订单身份。
这些保存在命令的执行证据里，而它们才是撤单要指名的东西。

这两个命名空间是刻意分开的。要求它们相等会让每一次撤单都不可能，因为它们从来不相等。
撤单真正需要的，是要指名的那个 gateway 订单，以及产生它的那个 gateway command——所以
服务在尝试撤单前断言的正是这两样。读 gateway 日志或审计行时，引用 gateway 的标识；
读界面或活动流时，引用 `commandId`。

## 下单票

手动下单是两步流程，而这个拆分本身就是要点。

`POST /api/v1/products/manual/execution-previews` 返回一份**封签过的**预览。服务先对
意图定价，按 instrument 规则校验，向该 account 取得预留，然后对得到的对象签名，并给
它一个很短的有效期。

`POST /api/v1/products/manual/commands` 接受的正是这份封签预览。它无法接受别的任何东西
——不接受被改过的副本，不接受过期的，也不接受预览从未定过价的意图。其结果是：「操作者
确认的东西」与「实际发往 venue 的东西」是同一个对象，而不是同一份表单的两次渲染。

预留是独立资源（`/api/v1/reservations`），可以创建、消费或释放。没有被消费的预留会被
释放，而不是继续占着额度。

## 套利产品

第二个产品交易的是资金费价差，而不是价格。它取一个标的在两家交易所上的两条腿，在资金费
最便宜的那家做多、最贵的那家做空，持有一个 delta 中性的对儿去赚这个差。它就是跨所永续
对冲，除此之外什么都不是：没有单所基差，没有现货腿，没有交割合约。

各家公布出来的费率是不能直接比的。每家按自己的节奏结算资金费——这家四小时，那家八小时
——所以一个原始费率在被归一之前什么都说明不了。所有费率先换算成日率（`rate × 24 ÷
intervalHours`）再两两相比，价差是 `shortDaily − longDaily`，标题上的 APR 就是这个价差
乘 365。一家说不出自己结算周期的交易所是排不了名的，服务会直说，而不是默认按八小时算。

`GET /api/v1/products/arbitrage/opportunities` 在请求点名的那些 account 之间给这些对儿
排名。每个对儿带着自己的 `blockers`，只要有一条就不可执行：`funding_interval_unknown`、
`account_unreachable`、`instrument_identity_unresolved`、`board_stale`。是否上架由公开
instrument directory 决定，账户可达性是另一条独立的执行事实，二者不能互相推断。只有空列表
才会让下单票活过来。

下单完全复用手动那套形状。`POST /api/v1/products/arbitrage/execution-previews` 返回一份
封签预览——同一种封签、同一个有效期——只不过它给两条腿都定价、也向两个 account 都取预留。
它还带着 `warnings`，这些不是 blocker、也拦不住确认：`fees_excluded` 表示报出来的价差是
毛的，`negative_spread` 表示排名在读取和预览之间翻了过来，`funding_rate_unavailable` 与
`board_stale` 表示某条腿的数字比它看上去的更旧。

`POST /api/v1/products/arbitrage/commands` 接过这份预览，并且承载全部三个动词，靠
`action` 区分：

| Action | 做什么 |
| --- | --- |
| `open` | 按封签预览一次请求下出两条腿 |
| `close` | 一次请求把两条腿都 reduce-only 平掉 |
| `cancel` | 撤掉指名的那一条挂着的腿 |

三者都要求一个 `lineageId`，以及一个与 body 相符的 `Idempotency-Key`。**lineage** 就是这
个对儿的身份：两家交易所上的两笔订单、两行命令，被一个标识拴在一起。正是它让「平掉这个
对儿」成为一次操作，而不是要操作者自己按对顺序去撤两次；也正是它，是
`GET /api/v1/products/arbitrage/positions` 用来分组的东西。一个读不回腿载荷的对儿会报
`leg_payload_unreadable` 并拒绝平仓——它描述不出来的平仓，就是它证明不了的平仓。

两条腿仍然是一条一条过账本的。这里没有跨两家的原子写，服务也不假装有：一个对儿完全可能
一条腿成交、另一条腿 `uncertain`，而这正是账本存在要兜住的状态。Reconciliation 会各自
对着各自的交易所把每条腿解决掉，对儿最终是什么样，取决于它的腿最终是什么样。

这三个 action 和其他每一次私有写一样，都挡在同一道 live 写开关后面：开关关着时，
`open`、`close`、`cancel` 在任何东西抵达交易所之前就返回 `403 LIVE_PRIVATE_WRITE_DISABLED`。

它们过去是被无条件拒绝的，理由是：在这个产品拿到它当时还没有的证据之前，一个对儿可以先在
test 那一层上练手。那一层已经没有了，所以这道拒绝再没有地方可以把人送过去，而开关就是
一次 `open` 与两笔真实订单之间唯一挡着的东西。它当年替代的那个证据缺口并没有补上——套利
背后的东西比手动产品少，而开关并不知道这件事。

## Reconciliation

一个由 ticker 驱动的 reconciler 在租约下认领到期任务，并向 gateway 索取权威答案。它每
秒运行一次，每次最多认领二十个任务，每个持有十五秒。

学到 venue 最终答案的是 reconciler，而不是 HTTP handler。Handler 在交接完成时就返回了；
成交或撤销要更晚才到。所以终态到达活动流只有一条路径：注册在 reconciler 上的结算观察者
——当命令落入 `filled`、`canceled` 或 `failed` 时，由它发布该命令。

没有这个观察者，审计链就会停在「已提交」，永远不会记录真正发生了什么。如果账本里命令
已是终态、活动流里却看不到，该看的就是这个观察者。

Gateway 在它自己那一层、对它自己的持久订单，也跑着自己的 reconciliation。两者互相独立，
谁也不能替代谁。见[状态与 reconciliation](../concepts/state-reconciliation.md)。

## 投影

读取由服务自己维护的投影提供，而不是实时调用 venue：

- `/api/v1/positions`、`/api/v1/pnl`——portfolio 状态。
- `/api/v1/fills`、`/api/v1/ledger`——发生了什么，以及它的账务。
- `/api/v1/activity`——命令链条，按顺序，带时间戳。
- `/api/v1/accounts/{accountId}/runtimes`——界面在每个 account 上显示的读写就绪。
- `/api/v1/catalog/venues`、`/api/v1/catalog/instruments`——可交易的东西。
- `/api/v1/market-data`、`/api/v1/candles`——行情条与 K 线图。
- `/api/v1/products/arbitrage/opportunities`、
  `/api/v1/products/arbitrage/positions`——排过名的价差，和已开的对儿。
- `/api/v1/realtime/stream`——界面持有的那一条 server-sent 流。

有两个读不是投影，也不假装是。`/api/v1/funding-board` 与 `/api/v1/funding-history` 由
gateway 那条免凭证的公共面提供，它不需要任何 account 就能回答。看板是整张矩阵，每个
venue-product-settlement 一列；历史是某家交易所上某个标的在一个有界窗口内的序列，只有
`7d` 和 `30d` 两档、别的都不接——开放区间意味着要对交易所接口翻页，而这个读正是为了避开
翻页才这么设计的。没有历史 surface 的交易所会被报成「不可用」，而不是报成「空」。参见
[市场与参考数据](market-reference-data.md)。

集合响应的包封是 `{"data":{"items":[…]}}`，`data` 旁边还有一个 `meta` 对象。单资源响应把
资源直接放在 `data` 里。在单资源上读 `data.items`、或在集合上读 `data`，读的都是错的形状。
按这条规则，看板和历史都是单资源而不是集合：`data` 就是那份快照或那条序列本身，它的列在
`venues` 下，它的读数在 `points` 下。这两个响应还各带第三个顶层键 `rateLimit`，也就是这次
读花掉之后、该交易所预算还剩多少。

## 存储

服务持有自己的数据库 `bolter_trading`，并以同名角色连接。`cmd/trading` 把两者都钉死，而不
是照单全收：别的地方都不会去校验它们，所以一个拼写错误会让这个服务把订单对着「碰巧应答的
那个 schema」去做 reconciliation。

它的表名不带 schema 限定：schema 来自连接的 `search_path`，所以同一个二进制不改代码就能
跑在 `trading`、一次性测试 schema 或 `public` 之上。Go 代码里没有任何地方写死 schema 名。

这里不保存任何凭证材料。Trading 服务从不接触 secret——account 凭证在 gateway 后面，而它
也从不索取。

## 验证

```sh
make test
make test-browser
```

离线套件覆盖账本的状态机、预览封签和各个投影。没有任何测试会下单：外部写入阶梯已经
和它所依赖的 Demo、Testnet 环境一起删除。离线套件证明了什么、又没证明什么，
见[测试与证据](../reference/testing.md)。
