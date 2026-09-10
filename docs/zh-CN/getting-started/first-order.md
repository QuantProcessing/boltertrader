# 下第一笔订单

> 英文为规范版本。[English](../../getting-started/first-order.md)

本页在浏览器里走完整个闭环：登录、注册 venue account、下一笔挂着的限价单、撤单、
确认终态。前提是栈已经跑起来了——见
[运行本地栈](local-stack.md)。

**这会在真实 venue 上下真实订单，花的是真钱。** 已经没有 demo 或 testnet 环境
可以退守了。请使用专用、刻意限制资金的 account，它的凭证不要带提现权限，并确保
你即将触及的 product scope 中没有无关订单或持仓。下面那一单的定价是为了挂住而
不是成交，第 8 步会把它撤掉。如果只想查看实时公共行情而不启用账户访问，请停留在
默认只读模式。如果还没读，先读[前置条件](prerequisites.md)。

## 1. 打开界面

```sh
open http://127.0.0.1:18090
```

只有一个 origin。前端与 `/api/*` 由同一个 Caddy 实例提供，后者把 API 反向代理到
auth edge。正因如此，会话 cookie 才能保持 `HttpOnly` 与 `SameSite=Strict`，也才
不存在任何 CORS 面。

在有会话之前，任何路由都会跳转到 `/login`。

## 2. 登录

使用 bootstrap operator——默认是 `operator`——以及首次 `make up` 打印过一次的
密码。它也在 `.secrets/stack/bootstrap_password` 里。

登录后，顶部 header 会带上四个路由：**Trade**、**Portfolio**、**Activity**、
**Settings**。

## 3. 注册一个 venue account

进入 **Settings**，选择 **Add account**。你需要提供：

- 交易所和可选的账户名称，
- venue 的凭证字段，其标签来自 gateway 而不是前端，因为各 venue 各不相同。

Credential value 是只写的。它们只发送一次，加密进 gateway 的凭证库，之后绝不返回
——它们会从表单、浏览器存储以及后续每一个响应中消失。

有哪些 venue 与 product 行见 [Venue 支持](../venues/README.md)。各 venue 需要哪些
凭证字段见[配置](../reference/configuration.md)。

## 4. 完成余额验证

添加时会检查凭证格式并查询交易所余额。成功后 Gateway 保存加密凭证、接通账户；
余额为零也可以添加。失败时在表单显示交易所返回的错误。

无需另行激活、更新权限或轮换凭证。API key 能否交易由交易所决定，权限不足时返回
交易所错误。更换凭证时删除后重新添加；删除只移除本地凭证和连接，不撤单、不平仓。
当前持仓直接读取 Gateway 的交易所快照，无需迁移历史订单。

## 5. 打开交易终端

**Trade** 针对所选 instrument 展示：行情条、K 线图、订单簿、下单票和 blotter。
市场更新通过 Gateway 的 `/api/v1/market/realtime/ws` 到达；账户通知使用另一条需要认证的
Gateway WebSocket。

选择该账户交易所支持的交易标的。

## 6. 确认后下单

填写下单票：**Limit**、方向、数量，以及一个离盘口足够远、能挂住而不会立刻成交的
价格。然后点下单按钮。若设置中启用了下单确认，确认面板会立即使用当前账户、方向、数量、
价格和名义价值展示本地复核信息，不会先等待后端预检。

点击 **Confirm** 后只发送一次命令请求。Trading 在该请求内读取当前账户 runtime、route 和
catalog 规则，再创建预留并交给 Gateway；Gateway 与交易所仍会做最终校验。网络重试由幂等键
保护，不依赖会过期的 preview ID。见[执行与风险](../concepts/execution-risk.md)。

## 7. 看它挂住

订单会出现在 blotter 的 **Orders** 标签页，带 instrument、方向、类型、价格、数量、
状态和最后更新时间。状态迁移通过同一条流到达。

界面上显示的标识是 trading 服务的 `commandId`。Gateway 在下面维护它自己的持久
订单身份；两者是刻意分开的命名空间，
[trading 服务指南](../guides/trading-service.md)说明了什么时候该引用哪一个。

## 8. 撤单，并确认终态

在 **Orders** 那一行撤单。该行会在 venue 确认之后进入终态——不是请求被接受的
那一刻。

然后确认两件事：

- **Orders** 标签页是空的。没有任何属于你的订单还挂着。
- **Activity** 展示该订单的完整链条：命令、其确认、以及其终态，每一项都带时间戳。

让订单挂在那儿是本演练唯一可能造成的故障。如果那一行没有进入终态——请求超时、
中途关闭了页面、流断了——不要再次下单。先按
[操作与恢复](../guides/operations-recovery.md)处理。

## 这证明了什么，没证明什么

它证明了某个 account 的某一个 product 行能端到端走完完整的手动生命周期，并且终态
是被观察到的，而不是被假定的。

它没有证明该 venue 已可用于生产，没有证明相邻的 product 行可用，也没有证明其他
account mode 行为相同。那些声明各有各的证据，
[测试与证据](../reference/testing.md)说明了每一条由什么来确立。

## 后续

- [架构与边界](../concepts/architecture.md)——你刚刚穿过的那些层，以及边界为什么
  划在那里。
- [Trading 服务](../guides/trading-service.md)——下单票背后的命令账本。
- [交易所网关](../guides/exchange-gateway.md)——account 背后的共享能力面。
