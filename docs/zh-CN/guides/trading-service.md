# Trading 服务

> 英文为规范版本。[English](../../guides/trading-service.md)

Trading 服务承载**手动交易**、命令恢复、持仓账务与浏览器投影。
执行过程使用同一套持久命令账本。

服务不持有任何 venue 凭证，也不建立任何 venue 连接：每一次写都经过
[交易所网关](exchange-gateway.md)。

它从不直接面向浏览器。请求由 [auth edge](auth-edge.md) 转发而来，携带服务令牌和
已认证的 principal。

## 命令账本

任何可能抵达 venue 的意图，在被尝试之前先被写下来。命令创建时是 `accepted`，并在
一组固定状态之间迁移：

| 状态 | 含义 |
| --- | --- |
| `accepted` | 已记录命令及恢复任务，尚未交给 gateway |
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

手动下单只发送一次请求。浏览器可以使用屏幕上已有的账户、仓位、catalog 与公共价格显示
本地确认，确认后将订单意图直接提交到 `POST /api/v1/products/manual/commands`。

HTTP handler 负责认证、CSRF 和请求解码。manual 应用服务负责参数校验、账户和路由解析、
命令创建与执行。新请求读取当前账户 runtime，校验无需可执行报价即可判断的 catalog 规则，
在同一事务中保存命令与初始恢复任务。只有创建成功的请求会尝试执行，并须先通过
`BeginExecution` 认领发送权。Gateway 和交易所仍负责最终就绪与接单判断。
市价单不会为了获准提交而额外请求一次 REST 报价。

重试首先按 principal、method、path 和幂等键查原命令。用户下单参数相同，即使账户或
catalog 离线、runtime 已变更或写入已关闭，也返回原命令。原命令记录的 route/runtime
保持不变。重放不会执行，包括仍在等待原请求发送的 `accepted` 命令；用户参数改变则返回
幂等冲突，不会创建另一笔订单。

通用命令集合只读，`POST /api/v1/commands` 和全部 `/api/v1/reservations` 操作已经移除。
Trading 不预留余额或保证金。历史预留行和命令关联保留用于追溯，运行中的服务不再创建、
消费或检查这些预留。

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

资金费看板和行情流由 Gateway 提供。Trading 不再保留第二份行情缓存，也不转发浏览器
市场订阅。参见
[市场与参考数据](market-reference-data.md)。

集合响应的包封是 `{"data":{"items":[…]}}`，`data` 旁边还有一个 `meta` 对象。单资源响应把
资源直接放在 `data` 里。在单资源上读 `data.items`、或在集合上读 `data`，读的都是错的形状。
按这条规则，看板和历史都是单资源而不是集合：`data` 就是那份快照或那条序列本身，它的列在
`venues` 下，它的读数在 `points` 下。

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

离线套件覆盖账本的状态机、手动执行和各个投影。没有任何测试会下单：外部写入阶梯已经
和它所依赖的 Demo、Testnet 环境一起删除。离线套件证明了什么、又没证明什么，
见[测试与证据](../reference/testing.md)。
