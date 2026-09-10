# Web 应用

> 英文为规范版本。[English](../../guides/web-app.md)

Web 应用是操作者界面。它是一个以静态文件形式提供的 React 单页应用，并且只跟一个
origin 对话。

## 技术栈

React 19，搭配 Vite 7 与 TypeScript。路由、数据与表格都来自 TanStack：`react-router`
负责路由，`react-query` 负责每一次请求与缓存，`react-table` 负责 blotter 和 portfolio
表格。图表是 `lightweight-charts`。样式是 Tailwind 4，配合 Radix primitive 之上的
shadcn 风格组件。

这个应用里没有任何交易引擎。它渲染 trading 服务投影出来的东西，提交操作者确认过的东西；
关于「什么可以被下单」的每一条规则都在 API 后面。

## 单一 origin

Caddy 提供构建产物，并把 `/api/*` 反向代理到 `authedge:18091` 的 auth edge。只有两条规则，
且彼此之间没有 fallthrough：edge 没有路由的 `/api` 路径会以 edge 自己的 `404` 返回，而不是
带着 `200` 的 `index.html`。收到 HTML 的 JSON 客户端会报解析错误，而不是报路由缺失——那是
一个糟糕得多的 bug。

其余全部落到 `index.html`，因为那些路径归客户端路由所有——在 `/portfolio` 上刷新必须得到
一个页面，而不是 `404`。

正是这个单一 origin 让会话 cookie 能保持 `HttpOnly` 与 `SameSite=Strict`，也因此没有任何
CORS 面需要配置。Caddy 保留浏览器的 `Host` 头，所以 edge 看到的正是 cookie 被设置时的那个
origin。见 [auth edge](auth-edge.md)。

实时连接不需要特殊的 Caddy 缓冲配置：Gateway 市场和账户流使用标准 WebSocket upgrade，
Caddy 会双向转发。

## 路由

| 路由 | 是什么 |
| --- | --- |
| `/login` | 唯一无需会话即可抵达的路由 |
| `/trade` | 行情条、K 线图、订单簿、下单票、blotter |
| `/funding` | 跨所资金费率矩阵，每个 venue 一列 |
| `/portfolio` | 持仓、余额、PnL |
| `/activity` | 带时间戳的命令链条 |
| `/settings` | Account、凭证、runtime 就绪 |

顶部导航提供这五个页面。`/trade` 和 `/funding` 无需账户即可读取公共市场数据；
私有账户读取和交易操作需要登录。

整个外壳上还有一个命令面板，用于在 instrument 与路由之间跳转。

## 数据流

读取经由 `react-query` 打到 `src/api/endpoints.ts` 里的那些端点。实时连接按归属共享，而不是
每个组件各建一条：Gateway 市场数据使用 `/api/v1/market/realtime/ws`，Gateway 账户通知使用
`/api/v1/account/realtime/ws`。同一 topic 的组件
订阅仍会合并。订单簿从 WS 快照开始，由浏览器应用连续增量或自包含 full 事件重建；Gateway
不保存完整深度，也不会把交易所 REST 订单簿拼进实时流。
账户实体按实体 ID、来源时间戳和 resource version 覆盖，因此较旧的飞行中 REST 响应不能把
较新的 WebSocket 状态回滚。REST reconciliation 固定每 60 秒执行，不会由每个事件或重连触发。
每条归属连接还带应用层 ping/pong 看门狗。Gateway 仍可达、但某条交易所市场流或账户流正在恢复
时，外壳显示 `degraded`；只有浏览器连接本身不可用时才显示 reconnecting。账户降级按账户分别
记录，只能由同一账户的健康恢复事件清除。

下单票使用屏幕上已有的账户、catalog 与公共价格在本地生成确认信息。**Confirm** 只发送一次
命令请求，不存在预览请求、服务端预览状态或过期倒计时。Trading 仍会在该命令请求中执行权威
的账户、路由与规则校验。详见 [trading 服务](trading-service.md)。

界面中的 credential value 是只写的。它们只提交一次，之后绝不返回——会从表单、浏览器存储
以及后续每一个响应中消失。

## 验证

```sh
make test-browser
```

它会按 lockfile 安装依赖、做类型检查与构建并运行 Vitest 单元套件。它不会启动应用、
访问 venue，也不会执行带凭证的浏览器流程。

另有一份 live spec，会真实下一笔挂单再撤掉。它背后已经没有非生产 venue 了——它打的是
真交易所，花的是真钱。它不属于 `make test-browser`；要么明确地去跑它，要么就别跑。
正常应用不再有 mock 模式：匿名只读页面经由运行中的真实栈读取实时公共数据。
