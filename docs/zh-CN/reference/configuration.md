# 配置

> 英文为规范版本。[English](../../reference/configuration.md)

有三个面在配置这个仓库，而把它们分开正是这套设计的全部要点。

| 面 | 谁来写 | 里面装什么 |
| --- | --- | --- |
| `.env.stack` | `make up`，每次都写 | 端口、uid，以及生成出来的密钥文件的路径 |
| `.env` | 你手写 | 供验收套件使用的交易所 API 凭证 |
| 命令行 | 你，每次调用时 | 执行闸门 |

它们永不相交。`make up` 不读 `.env`，`make test` 不读 `.env.stack`，而任何闸门都不能从这两个
文件里设置。

## `.env.stack` —— 本地栈

由 `go run ./internal/devsetup/cmd/bootstrap` 在每次 `docker compose` 调用之前生成，并且每一次
调用都显式点名它：

```sh
docker compose --env-file .env.stack -f compose.yaml up -d --build --wait
```

那个 `--env-file` 不是装饰。没有它，Compose 会把仓库自己的 `.env` 自动加载进插值，而那里面
是真实的交易所凭证。

下面这些可以在启动栈的那条命令上覆盖。值会被持久化进 `.env.stack`，于是之后的 `make doctor`、
`make logs` 与 `make down` 都与之一致：

| 变量 | 默认值 | 含义 |
| --- | --- | --- |
| `POSTGRES_PORT` | `15432` | PostgreSQL 的 loopback 端口 |
| `GATEWAY_PORT` | `18080` | Gateway 公开面的 loopback 端口 |
| `AUTHEDGE_PORT` | `18091` | Auth edge 的 loopback 端口 |
| `TRADING_PORT` | `18092` | Trading 服务的 loopback 端口 |
| `WEB_PORT` | `18090` | Web 应用的 loopback 端口——你要打开的是这个 |
| `BT_BOOTSTRAP_USERNAME` | `operator` | Bootstrap operator 的用户名 |
| `BT_SECRET_DIR` | `.secrets` | 生成出来的密钥放在哪 |
| `BT_HOST_UID`、`BT_HOST_GID` | 你自己的 | 容器以哪个 uid 运行 |

```sh
POSTGRES_PORT=15433 WEB_PORT=18095 make up
```

Bootstrap 还会写下它生成的那些文件的路径——`GATEWAY_AUTH_TOKEN_FILE`、
`GATEWAY_INTERNAL_AUTH_TOKEN_FILE` 与 `GATEWAY_POSTGRES_DSN_FILE`。这些是路径，不是值：
任何密钥都不会被写进 `.env.stack`，任何服务也都不从自己的环境变量里拿密钥。每一个都去读一个
权限为 `0600` 的文件。

`BT_HOST_UID` 与 `BT_HOST_GID` 之所以存在，是因为 gateway 的凭证存储是一个宿主机 bind mount，
而不是命名卷。Docker 建出来的命名卷属主是 `root`，一个以你的 uid 运行的容器写不进去。

## `.env` —— 交易所凭证

只有验收套件会读这个文件，经由 `internal/testenv.LoadRepoEnv`。它是可选的：找不到凭证的套件
会说出它想要的那个变量名。你 shell 里已经设过的变量始终优先。从
[`.env.example`](../../../.env.example) 开始抄。

| 环境 | 写入所需 |
| --- | --- |
| Binance Demo | `BINANCE_DEMO_API_KEY`、`BINANCE_DEMO_API_SECRET` |
| OKX Demo | `OKX_DEMO_API_KEY`、`OKX_DEMO_API_SECRET`、`OKX_DEMO_API_PASSPHRASE` |
| Bybit Demo | `BYBIT_DEMO_API_KEY`、`BYBIT_DEMO_API_SECRET` |
| Bybit Testnet | `BYBIT_TESTNET_API_KEY`、`BYBIT_TESTNET_API_SECRET` |
| Bitget Demo | `BITGET_DEMO_API_KEY`、`BITGET_DEMO_SECRET_KEY`、`BITGET_DEMO_PASSPHRASE` |
| Gate Testnet | `GATE_TESTNET_API_KEY`、`GATE_TESTNET_API_SECRET` |
| Hyperliquid Testnet | `HYPERLIQUID_TESTNET_PK` |
| Lighter Testnet | `LIGHTER_TESTNET_PRIVATE_KEY`、`LIGHTER_TESTNET_ACCOUNT_INDEX`、`LIGHTER_TESTNET_API_KEY_INDEX` |
| Aster Testnet | `ASTER_TESTNET_USER_ADDRESS`、`ASTER_TESTNET_SIGNER_PRIVATE_KEY` |
| Nado Testnet | `NADO_TESTNET_PRIVATE_KEY` |

每一块都是全有或全无。Bybit Demo 与 Bybit Testnet 是两个不同的环境、两对不同的凭证，其中一个
被显式拒绝作为另一个的替代。

这里有四个是钱包私钥，不是 API key。它们签得动的东西就是花得出去的，所以请用一个只装了测试网
资产的钱包。绝不要把生产凭证放进上面任何一个名字里。

身份字段是边界，不是标签：

- Aster 把用户地址、签名私钥，以及可选的 `ASTER_TESTNET_EXPECTED_SIGNER_ADDRESS`（用来核对
  两者一致）分开。
- Nado 的 `NADO_TESTNET_SUBACCOUNT_NAME` 属于统一保证金的身份，不是币种层面的余额分区。
- Hyperliquid 的 `HYPERLIQUID_ACCOUNT_ADDRESS` 在由 agent 或 API 钱包私钥签名时标识所有者。
  `HYPERLIQUID_TESTNET_VAULT` 能被解析，但 Vault 与 SubAccount 这两种用户角色在验收路径上被拒绝。
- Lighter 除私钥外还需要那两个 index 变量。

Bitget 的 `BITGET_TESTNET_*` 是上面 `BITGET_DEMO_*` 的兼容别名，不是第二个环境。两者都设时以
规范名为准。

## 闸门 —— 只走命令行

闸门不能来自文件。`internal/testenv` 拒绝导入任何匹配 `RUN_*`、`ENABLE_*`、`ALLOW_*`、
`*_REALTIME_WS`，或者名字中包含 `_ENABLE_`、`_ALLOW_` 的变量。理由很窄，也值得直说：一个被留在
文件里打开着的闸门是看不见的，而你下一次跑 `go test` 就会写到交易所去。

把它们内联传进来，让它们出现在用到它们的那条命令里：

```sh
BOLTER_ENABLE_BINANCE_DEMO_WRITES=1 make test-exchange-binance-demo-spot
```

通常你根本不用自己敲——每个外部 Make target 都设了自己的那个。

| 闸门 | 效果 |
| --- | --- |
| `BOLTER_ENABLE_LIVE_READ_TESTS` | 允许带凭证读取交易所 |
| `BOLTER_ENABLE_BINANCE_DEMO_WRITES` | 允许 Binance Demo 写入 |
| `BOLTER_ENABLE_OKX_DEMO_WRITES` | 允许 OKX Demo 写入 |
| `BOLTER_ENABLE_BYBIT_DEMO_WRITES` | 允许 Bybit Demo 写入 |
| `BOLTER_ENABLE_BYBIT_TESTNET_WRITES` | 允许 Bybit Testnet 写入 |
| `BOLTER_ENABLE_BITGET_DEMO_WRITES` | 允许 Bitget Demo 写入 |
| `BOLTER_ENABLE_GATE_TESTNET_WRITES` | 允许 Gate Testnet 写入 |
| `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES` | 允许 Hyperliquid Testnet 写入 |
| `BOLTER_ENABLE_LIGHTER_TESTNET_WRITES` | 允许 Lighter Testnet 写入 |
| `BOLTER_ENABLE_ASTER_TESTNET_WRITES` | 允许 Aster Testnet 写入 |
| `BOLTER_ENABLE_NADO_TESTNET_WRITES` | 允许 Nado Testnet 写入 |
| `BOLTER_ALLOW_OKX_DEMO_CUSTOM_WRITES` | 额外允许一个自定义 OKX endpoint profile 写入 |
| `BOLTER_ALLOW_BITGET_DEMO_CUSTOM_WRITES` | 额外允许一个自定义 Bitget endpoint profile 写入 |
| `BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES` | 允许一次绕开装置边界的裸 Nado SDK 写入 |

最后那个的名字是故意起成那样的。

## 边界与选择器

每个交易所都有一个最大名义额上限，且自带一个不大的默认值——`BINANCE_DEMO_MAX_NOTIONAL_USDT`、
`HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`、`NADO_TESTNET_MAX_NOTIONAL_USDT0`，以此类推。计价资产
是名字的一部分，各交易所并不相同。

上限是一个安全边界，不是你要下的数量。它是「打错的数量」和「交易所真会成交的单」之间的最后一道
东西，所以要有意识地调高，也要记得调回来。

多数交易所还接受 symbol 覆盖。不设 symbol 通常意味着装置会从发现到的 instrument 里挑一个合适的。
`internal/testenv/testenv.go` 把每一个变量连同它回落到的默认值一起列了出来，那个文件是权威
——本页不重述它。

## Endpoint profile

Endpoint 变量是高级安全输入。绝不要把一个 Demo 或 Testnet 的写 target 指向生产。

**固定。** Binance、Bybit、Hyperliquid 与 Lighter 选用内置的非生产 profile，不暴露验收用的覆盖变量。

**只接受与官方完全一致的值。** Aster 接受 `ASTER_TESTNET_SPOT_REST_URL`、
`ASTER_TESTNET_SPOT_WS_URL`、`ASTER_TESTNET_SPOT_USER_WS_URL` 及其 `PERP` 对应项——但只在每个值
都与内置的官方 Testnet endpoint 完全一致时才接受。Nado 对
`NADO_TESTNET_GATEWAY_URL`、`NADO_TESTNET_GATEWAY_V2_URL`、`NADO_TESTNET_ARCHIVE_URL`、
`NADO_TESTNET_ARCHIVE_V2_URL`、`NADO_TESTNET_GATEWAY_WS_URL`、`NADO_TESTNET_WS_URL` 与
`NADO_TESTNET_TRIGGER_URL` 应用同一条规则。Gate 的读接受 `GATE_TESTNET_REST_BASE_URL`、
`GATE_TESTNET_SPOT_WS_URL` 与 `GATE_TESTNET_USDT_FUTURES_WS_URL`，但带凭证的写要求这三个都解析
到官方 Testnet profile。

**有守卫的自定义。** `OKX_DEMO_HOST_PROFILE` 取 `global`（默认）、`eea` 或 `custom`。官方 profile
直接禁止覆盖；`custom` 需要同时给出 `OKX_DEMO_REST_BASE_URL` 与 `OKX_DEMO_WS_BASE_URL`，加上前面
那个命令内联的允许开关，而带凭证的写还额外要求 HTTPS 与 WSS，并拒绝可识别的官网与生产 WebSocket
主机。Bitget 经由 `BITGET_DEMO_REST_BASE_URL`、`BITGET_DEMO_PUBLIC_WS_URL` 与
`BITGET_DEMO_PRIVATE_WS_URL` 走同样的逻辑，这三个必须一起给出。

## 还有两个

`GATEWAY_TEST_POSTGRES_DSN` 把 `make test-gateway-postgres` 与 `make test-gateway-restart` 指向一个
真实的 PostgreSQL。不设它，它们直接失败而不是跳过。`make test-compose-postgres` 会起一个用完即弃
的集群并替你设好。

`PROXY` 接受 `http`、`https` 或 `socks5` URL，可以带凭证，而那些凭证在任何可能露出来的地方都会被
脱敏。设了它时，验收用的 HTTP 客户端就用它；没设时，它们会禁用继承来的 `HTTP_PROXY`、
`HTTPS_PROXY` 与 `ALL_PROXY`，而不是默默照单全收。WebSocket 的路由是各交易所各自的事，不能由这条
规则推断。

## 把密钥挡在外面

凭证值、私钥、代理凭证与账户残留，都不进源码、不进示例、不进日志、不进版本控制。配置错误以及
配置的每一种字符串呈现都会脱敏密钥与 URL 用户信息——这件事有测试守着，`make test-exchange-redaction`
可以单独跑它。

`.env`、`.env.stack` 与 `.secrets/` 都被 Git 忽略。只有 `.env.example` 会被提交，而它里面每个值
都是空的。

## 相关

- [先决条件](../getting-started/prerequisites.md)
- [本地栈](../getting-started/local-stack.md)
- [测试](testing.md)
- [不支持的能力](unsupported.md)
