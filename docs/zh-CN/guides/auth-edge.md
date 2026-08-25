# Auth edge

> 英文为规范版本。[English](../../guides/auth-edge.md)

Auth edge 是浏览器唯一会直接对话的服务。它只终结会话，别的什么都不做：不持有领域状态，
除了自己的会话表之外不拥有任何表，也不做任何交易决策。凡是它转发的东西，都原样转发。

它的职责是一次翻译。入站一侧，请求由浏览器自动携带的 cookie 认证；出站一侧，请求由服务
令牌和一个被担保的 principal 认证。这次翻译就是安全边界，而且它只朝一个方向运行——浏览器
能提供的头，永远不可能变成上游会信任的那个头。

## 它自己处理的部分

有四条路由从不离开这个进程：

| 路由 | 效果 |
| --- | --- |
| `POST /api/v1/auth/login` | 校验凭据，签发会话，设置 cookie |
| `GET /api/v1/auth/session` | 返回当前 principal 与会话 |
| `GET /api/v1/me` | 同一个答案，用的是前端使用的那个名字 |
| `DELETE /api/v1/auth/session` | 吊销会话并让 cookie 过期 |

会话 cookie 名为 `bt_session`，带 `HttpOnly`、`SameSite=Strict` 和 `Path=/`。部署走 TLS
时会标记 `Secure`。会话令牌本身从不被序列化进任何响应体——它只存在于 cookie 里。

登录会在响应体里单独返回一个 `csrfToken`。因为 cookie 是 `HttpOnly`，脚本读不到会话令牌，
所以前端回传的是这个 CSRF token，放在 `X-CSRF-Token` 里。每一次会改变状态的转发都需要它；
缺失或不对就是 `403`，错误码 `CSRF_REQUIRED`，且发生在任何转发之前。

认证失败一律是 `401`，错误码 `AUTH_REQUIRED`，消息也一样——无论是用户名不存在、密码错误，
还是会话已过期。这个区别不关调用方的事。

## 它转发的部分

其余全是查表。这张表是显式的——每个方法加路径一条——而不是前缀规则，这是刻意的。

一共声明了四十条路由。三十五条去 trading 服务，五条去 gateway：

| 前缀 | 上游 | 说明 |
| --- | --- | --- |
| `/api/v1/accounts`、`/api/v1/accounts/{accountId}` 及其动作 | Gateway | 被重写到内部面 |
| `/api/v1/accounts/{accountId}/runtimes` | Trading | 这是投影，不是 account 本身 |
| `/api/v1/catalog/*`、`/api/v1/market-data`、`/api/v1/candles` | Trading | |
| `/api/v1/realtime/ws`、`/api/v1/venues/connectivity` | Trading | 单条多路复用 WebSocket 与 REST 快照 |
| `/api/v1/commands`、`/api/v1/reservations`、`/api/v1/products/manual/*` | Trading | |
| `/api/v1/positions`、`/api/v1/ledger`、`/api/v1/pnl`、`/api/v1/fills`、`/api/v1/activity` | Trading | |
| `/api/v1/system/status`、`/api/v1/system/readiness`、`/api/v1/audit` | Trading | |

值得盯着看的是中间那一对。`/api/v1/accounts/{accountId}/runtimes` 是 trading 服务的投影，
而其余每一条 `/api/v1/accounts` 路由都是 gateway 的 account 生命周期——其中包括携带 venue
凭证的那些。前缀规则一旦猜错，就会把凭证材料送到错误的服务，所以这张表逐条指名，而不是
去推导。

Gateway 的这些路由也是唯一会改变路径的：`/api/v1/accounts` 在出站时变成
`/internal/v1/accounts`。Account 生命周期位于 gateway 那个需要认证的内部面，而不是 trading
服务读取的资源面。见[交易所网关](exchange-gateway.md)。

## 什么能过去，什么不能

三组头定义了这条边界。

**从入站请求中剥离，且总是剥离。** `Authorization`、`X-Bolter-Principal`、
`X-Bolter-Principal-Mode`、`X-Bolter-Mode` 和 `Cookie`。这些恰好是 edge 即将自己断言的头，
所以发送它们的浏览器必须无法影响它们。剥离是无条件的——不取决于值看起来是否可疑。

那两个 mode 头留在清单上，尽管已经没有任何东西再断言它们了。当初有两种 mode 时，上游正是
靠它们得知一次请求属于哪一种；今天还带着它们抵达的请求只可能是有人在探测，万一哪个上游
以后又长出同名的头，它也绝不能存活到被当作授权来读。

**按允许清单原样传递。** `Accept`、`Content-Type`、`Idempotency-Key`、`If-Match`、
`Last-Event-ID` 和 `X-Request-Id`。仅此而已。用允许清单而不是拒绝清单，是因为没人想到过的
头不应该默认就能抵达上游。

**出站时注入。** `Authorization: Bearer <服务令牌>`、带已认证 principal ID 的
`X-Bolter-Principal`，以及调用方没提供时补上的 `X-Request-Id`。

返回方向上，逐跳头会被移除，`Set-Cookie` 也会。任何上游都不允许在浏览器里设置 cookie。
只有 edge 会，而且只为会话。

## 实时 WebSocket

`GET /api/v1/realtime/ws` 是浏览器唯一的实时连接。Edge 将 WebSocket upgrade 双向转发给
Trading，升级后的连接不使用普通请求超时。公共行情 topic 可以匿名订阅；未认证连接订阅
`account.events` 时，Trading 只拒绝该 topic，并返回 `AUTH_REQUIRED`。

## 它不做的事

它不做聚合。一个浏览器请求至多变成一个上游请求，上游的响应体原样返回。edge 里没有任何
地方会合并两个服务的答案，因为那会把产品逻辑放进这个唯一必须「笨到可以被托付会话」的
进程里。

它不做会话之外的授权。某个 principal 能否触及某个 account 或某个 product，由拥有它的那个
服务决定，依据是 edge 担保的 principal。

## 验证

```sh
curl -i http://127.0.0.1:18091/api/v1/me
```

没有会话时这必须是 `401`。`make doctor` 跑的正是这条检查，并把任何其他答案视为失败——这里
返回 `200` 就意味着 edge 在替一个它从未认证过的 principal 作答。

服务令牌和上游地址的配置变量见[配置](../reference/configuration.md)。整个栈如何组合在一起
见[架构](../concepts/architecture.md)。
