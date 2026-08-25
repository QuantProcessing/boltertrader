# 架构

> 英文为规范版本。[English](../../concepts/architecture.md)

本页定义 BolterTrader 的分层与依赖边界。提交语义由[执行与风险](execution-risk.md)
说明，状态收敛由[状态与 reconciliation](state-reconciliation.md)说明，account 与
instrument 身份由 [Account 与 instrument](accounts-instruments.md) 说明。

## 分层与依赖方向

| 层 | 负责 | 可以 import |
| --- | --- | --- |
| `sdk/<venue>/` | 单个 venue 的原生 API：传输、签名、endpoint profile 与线协议类型 | 标准库及其自身的传输依赖 |
| `exchange/` | 基于 SDK 的 venue-neutral typed client 与模型，以及通过 `exchange/factory` 的构造 | 仅在 `exchange/internal/**` 中 import `sdk/**`，另加 `credential`、`routeprofile` |
| `gateway/` | 共享能力面：account、加密凭证、catalog、能力与限额解析、持久订单身份、reconciliation 与私有事件投递 | `gateway/**`、`credential`、`exchange`、`exchange/factory`、`routeprofile`、`secretstore` |
| `services/` | 构建在 gateway HTTP 面之上的产品行为：命令账本、projection、浏览器会话与转发 | 自身目录树、`internal/**`、`credential`、`routeprofile`、`secretstore` |
| `web/` | 唯一的前端 | auth edge 的 `/api/v1` 面 |

依赖严格向下。任何一层都不会 import 它上面的层，也不会 import 同级的内部实现。

其中两层本身就是入口，而不只是上一层的实现细节：
[统一交易所 API](../exchange/README.md) 可嵌入任意 Go 进程，
[Exchange Gateway](../guides/exchange-gateway.md) 服务于任意产品，不限于本仓库
里的这一个。

## 被强制的边界

有三个测试把上表钉在代码上。正因为如此，边界违规会让 `make test` 变红，而不是
只能靠 review 发现。

- `gateway/contracts/import_boundary_test.go` 遍历 `gateway/` 下每个非测试
  `.go` 文件，拒绝任何不属于 `gateway/...` 的仓库 import，例外只有五个 package：
  `credential`、`exchange`、`exchange/factory`、`routeprofile`、`secretstore`。
  因此 gateway 连误引用 venue SDK 都做不到。
- `exchange/boundary_test.go` 拒绝公开的 `exchange` 与 `exchange/factory` 文件中
  任何含 `/sdk/`、`/adapter/` 或 `/runtime/` 的 import，也拒绝名为 `rawParams`、
  `nativeParams`、`sdkClient`、`nativeClient` 的公开逃逸符号。SDK 接线位于
  `exchange/internal/**`，因此调用方无法通过规范化 API 拿到原生句柄。
- `exchange/docs_manifest_test.go` 与 `exchange/docs_system_contract_test.go` 把两张
  operation matrix 和 venue 差异表钉在
  `exchange/testdata/public_surface_manifest.json` 上，因此文档里的能力声明不会
  与支撑它的代码脱节。

services 层的边界由构造方式保证，而非由测试保证：`services/` 下没有任何生产文件
import `gateway/**` 或 `exchange/**`。产品服务通过 HTTP 访问 gateway——正是这一点
让 gateway 可替换、可独立部署，并可被不在本仓库中的产品使用。

## 进程与网络拓扑

`make up` 会启动八个容器：PostgreSQL、三个运行完即退出的 migrator，以及四个
常驻服务。所有发布出来的端口都只绑定 `127.0.0.1`。

```text
浏览器 ──► web (Caddy)
              │  同源：/api/* 被反向代理
              ▼
         services/authedge ──┬──► gateway            /internal/v1/accounts…
                             └──► services/trading   其余全部
                                        │
                                        └──► gateway /v1
                                                 │
                                            exchange ──► sdk ──► venue
```

浏览器只与一个 origin 通信。由于前端产物与 `/api/*` 同源，会话 cookie 得以保持
`HttpOnly` 与 `SameSite=Strict`，也不存在任何 CORS 面。auth edge 终结该会话，并
按路径前缀转发，同时注入服务令牌与调用者身份头；它不做响应聚合。gateway 的账户
生命周期只在其内部面可达，因为它接收凭证材料。

Postgres 只跑一个实例。gateway、trading 服务与 auth edge 在其中各自拥有独立
schema，彼此从不读对方的表。

## 交易所差异应放在哪里

无法表达为可移植策略的差异要尽可能下沉：

- `sdk/<venue>` 负责线协议表示、签名与 endpoint profile。
- `exchange/internal/<venue>` 负责原生 symbol 与 asset index、支持的订单字段、
  account 与 position mode 发现、stream gap 解释、报告覆盖范围，以及向规范化
  错误的映射。
- `exchange` 只暴露每个已实现 venue 都能表达的东西。某个 venue 无法满足的维度
  会成为 `exchange.ErrUnsupported`，而不是调用方里的 venue 专用分支。

在 `exchange` 之上，venue 名称是数据——instrument identifier 或能力记录的一部分。
它绝不能用来选择代码路径。真正的差异集中记录在
[venue 与 account-mode 差异](../reference/exchange-venues-and-account-modes.md)。

## 共享能力应放在哪里

gateway 刻意做厚，产品服务刻意做薄。任何不止一个产品会需要的东西都放在 gateway：
凭证存储、账户生命周期、catalog、能力与限额解析、持久订单身份、reconciliation
与私有事件投递，以及标准化公共行情事实。资金费、mark、index 和 OI 只采集一次，
保存在 Gateway 内存并通过内部流发送，不会因为产品或浏览器请求增加而重复查询。
完整订单簿继续使用按 instrument 的实时流，不进入这份跨市场投影。

产品服务只负责自己那些产品的决策——命令账本、预览、预留，以及其界面所需的
projection。两个产品可以共用一个服务和这一整套机器：手动交易与资金费套利都住在
`services/trading` 里，它们的差别在于端点、以及封签意图的形状，而不是把账本再抄
一份。Trading 持有 Gateway 衍生品事实投影的本地副本，并负责策略相关计算；历史序列
仍按需读取，可执行套利预览则刻意绕过投影，即时读取两条腿的 BBO。
市场能力矩阵是生产运行配置而不是说明文档：每个 dataset 的 transport、原生
market family 边界、cadence 与 rate-limit operation 都由它选择。每个
`venue + environment + native market family` 只有一个 feed owner；route profile
只是该 owner 的输出投影，热点订单簿、BBO 与 K 线 topic 也共用该 owner 的一个
原生 socket。Feed owner 将字段级 `DerivativePatch` 写入同一个内存事实仓库，
资费看板、Trader 标记价、仓位 PnL 与套利投影都读取这份事实。
当一个产品服务长出了第二个产品也会需要的能力，这就是把它下沉进 gateway 的
信号，而不是复制一份的信号。

## 贡献者边界检查

新增抽象之前，先判断哪一层能在不泄漏下一层的前提下表达它：

- 如果它需要 SDK 类型，它属于 `exchange/internal/<venue>`。
- 如果它需要在 `exchange` 之上写 venue 分支，说明归一化还没做完。
- 如果第二个产品也会需要它，它属于 `gateway/`，而不是某个 service。
- 如果它需要在 `services/` 内使用 gateway 的 Go 类型，说明 gateway 的 HTTP 面缺了
  东西；应该扩展那个面，而不是扩展 import 图。

接下来可阅读 [Account 与 instrument](accounts-instruments.md)、
[venue 贡献指南](../contributing/venues.md)，以及服务指南：
[gateway](../guides/exchange-gateway.md)、
[trading 服务](../guides/trading-service.md)、
[auth edge](../guides/auth-edge.md) 和 [web 应用](../guides/web-app.md)。
