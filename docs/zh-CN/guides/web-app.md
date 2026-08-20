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

流式在这里不需要任何配置：Caddy 会立即 flush `text/event-stream` 响应，而
`/api/v1/realtime/stream` 依赖的正是这一点。一个会缓冲的代理会把事件一直攥到连接关闭，
看起来跟数据流死掉一模一样。

## 路由

| 路由 | 是什么 |
| --- | --- |
| `/login` | 唯一无需会话即可抵达的路由 |
| `/trade` | 行情条、K 线图、订单簿、下单票、blotter |
| `/funding` | 跨所资金费率矩阵，每个 venue 一列 |
| `/funding/arbitrage` | 排过名的价差、资金费历史图、对冲下单票、已开的对儿 |
| `/portfolio` | 持仓、余额、PnL |
| `/activity` | 带时间戳的命令链条 |
| `/settings` | Account、凭证、runtime 就绪 |

顶部导航承载这全部六项；操作者要做的每一件事，在任何位置都只有一次点击的距离。没有会话时，
每条路由都会跳转到 `/login`。

`/funding` 是唯一一个在任何 account 存在之前就能渲染的页面——看板是公共数据，不需要凭证
——所以在一套全新安装上它就是可读的。`/funding/arbitrage` 不是：它要在操作者选中的那些
account 之间给对儿排名，一个都没选中时它就没有什么可排的。

整个外壳上还有一个命令面板，用于在 instrument 与路由之间跳转。

## 数据流

读取经由 `react-query` 打到 `src/api/endpoints.ts` 里的那些端点。实时更新走一条
server-sent 流——`/api/v1/realtime/stream`——由外壳持有并分发给已挂载的组件，而不是每个组件
各订阅一次。

下单是那个两步的票据：**Review order** 请求一份封签预览并开始它的有效期倒计时，
**Confirm** 提交这个封签对象。封签过期时按钮会直接这么说。理由见
[trading 服务](trading-service.md)。

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
