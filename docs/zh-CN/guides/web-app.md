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

Caddy 双向转发单条 `/api/v1/realtime/ws` WebSocket upgrade，
Caddy 会双向转发。

## 路由

| 路由 | 是什么 |
| --- | --- |
| `/login` | 登录；公共交易与资费页面也允许匿名访问 |
| `/trade` | 行情条、K 线图、订单簿、下单票、blotter |
| `/funding` | 跨所资金费率矩阵，每个 venue 一列 |
| `/portfolio` | 持仓、余额、PnL |
| `/activity` | 带时间戳的命令链条 |
| `/settings` | Account、凭证、runtime 就绪 |

侧栏提供这五个页面。`/trade` 和 `/funding` 无需账户即可读取公共市场数据；
私有账户读取和交易操作需要登录。

品种选择器同时切换图表、订单簿和下单票的市场。

## 数据流

HTTP 用于首次快照和明确的操作请求。浏览器的持续更新全部复用 Auth Edge 的
**一条** `/api/v1/realtime/ws`：公共 topic 提供深度、K 线、标记价、指数价、资费和连接状态；
`account.events` 提供余额与持仓；`trading.orders` 提供已持久化订单、规范成交及相关账本、活动投影。
重复消费者合并订阅，取消时释放；心跳与重连恢复当前订阅集。Vite 开发热更新连接另计。

深度以原生 WS 快照开始，应用 delta/full，不拼接 REST 订单簿。账户实体按身份、时间戳和版本
合并，订单状态由 Trading 的规范命令身份决定。旧 REST 响应不能把终态订单退回“已提交”。
断线保留最后指数估值并标记延迟；重连后重读账户快照，平时每 60 秒 REST 修复作为兜底。

线性仓位以同交易所指数价格和精确十进制计算未实现盈亏。标记价保持独立，图表价格线不再
显示大块文字。缺失指数价不以标记价替代；反向合约等未支持公式保留交易所盈亏。汇总按原币种
分别展示。侧栏、浅色/深色/系统主题、紧凑指标和响应布局遵循 `design.md`。

下单确认使用已有账户、catalog 和行情在本地生成，只提交一次命令请求。响应立即更新订单，
后续持久化结果经 WS 到达。Gateway 的订单/成交事件唤醒 Trading 只读核对，不重发订单，
不缩短未知交接的安全窗口。凭证只写入一次，提交后从表单清除且不会通过读取返回。

## 验证

```sh
make test-browser
```

它会按 lockfile 安装依赖、做类型检查与构建并运行 Vitest 单元套件。它不会启动应用、
访问 venue，也不会执行带凭证的浏览器流程。

无凭证交互预览使用 `npm --prefix web run dev:mock`。账户与成交为本地模拟；行情来自交易所
公开接口，需要外网。模拟实现不读取密钥、不签署交易所请求、不产生真实订单。
详见 [测试](../reference/testing.md)。
