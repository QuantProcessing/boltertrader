# BolterTrader

> 英文为规范版本。[English](README.md)

BolterTrader 是一个 Go 原生、自托管的交易系统，面向单个操作者和他们自己的
交易所账户。它是一个分层的整体：venue SDK 被归一化为 venue-neutral 的
`exchange` API，Exchange Gateway 在其上提供带持久订单身份的常驻能力面，
上层的产品服务构建在 gateway 之上，而不是与它并列。

## 运行

运行整套栈只需要 Docker 与 Docker Compose；在容器外构建或测试需要 Go 1.26
或更高版本（由 `go.mod` 要求）。

```sh
make up
```

首次运行会生成凭证与 `.env.stack`，构建并启动全部容器，等待它们健康，然后
运行下面的检查。首次运行会**只打印一次**生成的 `operator` 密码，同时也保存在
`.secrets/<mode>/bootstrap_password`。应用入口是 <http://127.0.0.1:18090>。

```sh
make doctor   # 什么起来了、什么没起来、每个服务在哪个端口
make logs     # 每个服务最近 200 行日志
make down     # 停止整套栈（down-hard 会连 postgres 卷一起删）
```

这里没有任何命令会读取仓库的 `.env`：那个文件保存的是验收 target 用的交易所
API 凭证，而这套栈自己的变量——端口、uid、密钥路径——生成在 `.env.stack` 里。
所有对外端口都只绑定 `127.0.0.1`。

## 一分钟了解架构

```text
web            React 19 + Vite，由 Caddy 托管，与 auth edge 同源
  │
  ├── services/authedge     会话终结 + 路径转发
  │      │                  （浏览器唯一直接对话的对象）
  │      ├──────────────────────────┐
  │      ▼                          ▼
  │   gateway                  services/trading
  │   账户、凭证、目录、能力、    手动下单编排、命令账本、
  │   订单、行情                持仓、预留、活动流
  │      ▲──────────────────────────┘
  ▼
exchange       覆盖 9 个交易所的 venue-neutral client 与模型
  │
sdk            13 个交易所，各自忠实表示其原生 API
```

每一层只负责一件事，边界由 `gateway/contracts/import_boundary_test.go` 强制：

- `sdk/<venue>/` 忠实表示 venue API。仅存在 SDK package 不意味着 gateway
  支持该交易所。
- `exchange/` 把各交易所归一化为一套 typed client 与模型。生命周期、缓存和
  持久化由调用方负责；它也可以脱离整套栈，单独嵌入 Go 进程使用。
- `gateway/` 是 `exchange/` 之上的共享能力面：账户、加密凭证、catalog、
  能力与风控限额解析、持久订单身份、reconciliation 和私有事件投递。它不导入
  venue SDK package。
- `services/trading` 与 `services/authedge` 是构建在 gateway `/v1` 之上的
  产品服务。它们自身不含任何交易所集成，各自在同一个 postgres 实例中拥有
  独立 schema。
- `web/` 是唯一的前端。它只与 auth edge 通信，且同源，因此会话 cookie 得以
  保持 `HttpOnly` 与 `SameSite=Strict`。

auth edge 是刻意做薄的：它终结浏览器会话，然后按路径前缀转发到 gateway 或
trading，注入服务令牌与身份头，并剥离浏览器自己带来的这两个头。它**不做响应
聚合**——前端用 TanStack Query 并发 fan-out。

## 验证

```sh
make test                 # Go，不需要凭证，short mode
make test-compose         # compose 配置、镜像构建、加固、冒烟、持久化
cd web && npm run build && npm test && npm run test:e2e
```

上面每一套都是离线的、不需要凭证的、确定性的，没有任何一套会碰到交易所。已经不存在
可供显式启用的带凭证 target——`TestMakefileRecipesNeverEnableCredentialedWrites`
会读 Makefile 里的每一条 recipe，一旦某条为写入开闸就失败。想不接交易所把产品点着走
一遍，跑 `npm --prefix web run dev:mock`，它由 `web/src/mocks/` 应答整个 API 面。

## 当前支持

exchange gateway 实现了 20 个 product 行，覆盖 Aster、Binance、Bitget、Bybit、Gate、
Hyperliquid、Lighter、Nado 和 OKX。`sdk/` 另外还包含 Backpack、EdgeX、GRVT 和 StandX，
它们是 SDK-only，不是 gateway product。支持以 product 为单位，而某一行处于 Implemented
状态并不等于 production readiness：套件跑绿只能证明规范化实现与所记录的 fixture 依然
一致，不能更宽。可以被断言的那些状态，定义在[术语表](docs/zh-CN/reference/glossary.md)。

- [统一交易所 API](docs/zh-CN/exchange/README.md)
- [Exchange Gateway](docs/zh-CN/guides/exchange-gateway.md)
- [Venue 指南](docs/zh-CN/venues/README.md)
- [测试与证据语义](docs/zh-CN/reference/testing.md)
- [文档索引](docs/zh-CN/README.md)

## 安全

已经没有非生产环境可以退守了。在这里配好的凭证直连真实交易所，你传进去的数量就是会
成交的数量，所以请签发一把专用 key、不要给它提现权限、并从小额开始。本仓库里没有任何
测试会下单——会下单的那些演练，已经和它们所依赖的 Demo/Testnet 环境一起删除了。订单来自
运行中的产品，用的是你自己的凭证。一旦某一单出了问题，检查精确的 validation ID 和权威的
product-scoped 状态；绝不要盲目取消或平掉无关 exposure。

凭证在整条链路上只写不读：经应用提交、由 gateway 加密静态存储，任何读取路径都
不会把它返回。

## 贡献

请从[公开文档契约](docs/zh-CN/contributing/documentation.md)开始。development
plan、原始 validation evidence、凭证和本地执行产物不属于公开文档。
