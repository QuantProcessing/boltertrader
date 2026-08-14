# Trading 服务

> 英文为规范版本。[English](../../guides/trading-service.md)

Trading 服务就是手动交易这个产品。它持有命令账本、下单票流程、portfolio 与
ledger 投影，以及那个把命令带向终态的 reconciler。它不持有任何 venue 凭证，也不
建立任何 venue 连接：每一次写都经过[交易所网关](exchange-gateway.md)。

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
- `/api/v1/realtime/stream`——界面持有的那一条 server-sent 流。

集合响应的包封是 `{"data":{"items":[…]}}`，`data` 旁边还有一个 `meta` 对象。单资源响应把
资源直接放在 `data` 里。在单资源上读 `data.items`、或在集合上读 `data`，读的都是错的形状。

## 存储

服务持有自己的数据库 `bolter_trading_<mode>`，并以同名角色连接。`cmd/trading` 在两者不
完全匹配时拒绝启动，因此一个生产进程打不开测试数据。

它的表名不带 schema 限定：schema 来自连接的 `search_path`，所以同一个二进制不改代码就能
跑在 `trading`、一次性测试 schema 或 `public` 之上。Go 代码里没有任何地方写死 schema 名。

这里不保存任何凭证材料。Trading 服务从不接触 secret——account 凭证在 gateway 后面，而它
也从不索取。

## 验证

```sh
make test
make test-browser
```

离线套件覆盖账本的状态机、预览封签和各个投影。外部写覆盖按 venue 和 product 行划分，每个
target 证明什么见[测试与认证](../reference/testing.md)。
