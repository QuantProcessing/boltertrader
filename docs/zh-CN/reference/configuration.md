# 配置

> 英文为规范版本。[English](../../reference/configuration.md)

有三个面在配置这个仓库，而把它们分开正是这套设计的全部要点。

| 面 | 谁来写 | 里面装什么 |
| --- | --- | --- |
| `.env.stack` | `make up`，每次都写 | 端口、uid，以及生成出来的密钥文件的路径 |
| `.env` | 你手写 | 供带凭证的测试使用的交易所 API 凭证 |
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

## `BT_LIVE_PRIVATE_WRITES_ENABLED` —— live 写开关

由 trading 和 gateway 两个服务分别读取。两个边界共用同一套严格解析和默认值，避免订单通过
一层后又被另一层拒绝。它是决定一次 private write 能否真正打到交易所的唯一运维开关，
而且**默认是开的，你不关它就是开的**。

| 取值 | 效果 |
| --- | --- |
| 未设置，或 `true` | 放行 private write。一笔订单会真的送到交易所 |
| `false` | 每一次 private write 都以 `LIVE_PRIVATE_WRITE_DISABLED` 被拒 |
| 其他任何值 | 两个服务都拒绝启动 |

这个默认值是"打开"而不是"关闭"，与本页其余所有变量相反。原因在于：准入曾经是一个
析取式 —— 进程处于 test 模式**或者**这个开关被打开，命令就放行；那时默认关掉它没有任何
代价，因为该跑的路径都由 test 模式承载。现在 mode 这条轴已经删掉了。如果默认仍然关着，
整个栈会正常启动、通过每一项就绪探针，然后拒绝每一笔订单，而没有任何健康检查会报告原因。

所以这个诚实的默认值同时也是危险的那个。这个开关背后已经什么都没有了 —— 没有 demo 交易所，
没有 testnet 钱包，也没有名义金额上限。被放行的订单就是真实订单，花的是真钱，
成交的就是调用方传进来的数量。把变量设为 `false` 是运维手上的熔断闸，
也是目前仅存的另一种姿态：只读。

拼错不会被猜着放过。把 `yes` 读成 `false`，你会以为写入是开着的；读成 `true`，
则等于用一个这个开关从未接受过的词把它打开了。

## `.env` —— 交易所凭证

只有测试会读这个文件，经由 `internal/testenv.LoadRepoEnv`，而没有任何 Make target 会跑那些
测试。它是可选的：找不到凭证的读测试会跳过，并说出它想要的那个变量名。你 shell 里已经设过的
变量始终优先。从 [`.env.example`](../../../.env.example) 开始抄。

这里的每一个名字都通向生产。它们背后没有 demo 也没有 testnet 层了——各交易所实现这两者的方式
差异太大，维持不住，用它们的那些套件已随凭证一起删除。应用默认以匿名只读模式运行，
不需要交易所凭证，并读取实时公共行情。

| 交易所 | 凭证 |
| --- | --- |
| Binance | `BINANCE_API_KEY`、`BINANCE_SECRET_KEY` |
| OKX | `OKX_API_KEY`、`OKX_API_SECRET`、`OKX_API_PASSPHRASE` |
| Bybit | `BYBIT_API_KEY`、`BYBIT_SECRET_KEY` |
| Bitget | `BITGET_API_KEY`、`BITGET_SECRET_KEY`、`BITGET_PASSPHRASE` |
| Hyperliquid | `HYPERLIQUID_PRIVATE_KEY`、`HYPERLIQUID_ACCOUNT_ADDR` |
| Lighter | `LIGHTER_PRIVATE_KEY`、`LIGHTER_ACCOUNT_INDEX`、`LIGHTER_KEY_INDEX` |
| Nado | `NADO_PRIVATE_KEY` |
| EdgeX | `EDGEX_STARK_PRIVATE_KEY`、`EDGEX_ACCOUNT_ID` |
| GRVT | `GRVT_PRIVATE_KEY`、`GRVT_API_KEY`、`GRVT_SUB_ACCOUNT_ID` |
| StandX | `STANDX_PRIVATE_KEY` |

每一块都是全有或全无。其中五个是钱包私钥，不是 API key：它们签得动的东西就是花得出去的，而且
再也没有一个 testnet 钱包可以拿来代替。key 只给测试用得到的那些权限，不给提币权限，并在交易所
提供这个选项的地方把它绑到你自己的 IP。

身份字段是边界，不是标签。Hyperliquid 的 `HYPERLIQUID_ACCOUNT_ADDR` 在由 agent 或 API 钱包私钥
签名时标识所有者，因此它与那把 key 不可互换。Nado 可选的 `NADO_SUBACCOUNT_NAME` 属于统一保证金
的身份，不是币种层面的余额分区。Lighter 除私钥外还需要那两个 index 变量。

有几个旧拼法仍然可解析：`OKX_SECRET_KEY`、`OKX_PASSPHRASE`、`EDGEX_PRIVATE_KEY` 与
`NADO_SUB_ACCOUNT_NAME` 是上面那些规范名的别名。两者都设时以规范名为准，包括规范名被设成空串
的情况——把一个变量清空是一条指令，不是一次遗漏。

## 闸门 —— 只走命令行

闸门不能来自文件。`internal/testenv` 拒绝导入任何匹配 `RUN_*`、`ENABLE_*`、`ALLOW_*`、
`*_REALTIME_WS`，或者名字中包含 `_ENABLE_`、`_ALLOW_` 的变量。理由很窄，也值得直说：一个被留在
文件里打开着的闸门是看不见的，而你下一次跑 `go test` 就会写到交易所去。

把它们内联传进来，让它们出现在用到它们的那条命令里：

```sh
BINANCE_ENABLE_LIVE_WRITE_TESTS=1 go test ./sdk/binance/spot/ -run TestClient_PlaceOrder
```

没有任何 Make target 会设置它们中的任何一个，将来也不会：
`TestMakefileRecipesNeverEnableCredentialedWrites` 会读 Makefile 里的每一条 recipe，一旦有一条
设置了就失败。闸门要么是你自己敲的，要么就没设。

| 闸门 | 效果 |
| --- | --- |
| `BOLTER_ENABLE_LIVE_READ_TESTS` | 允许读取交易所，带凭证或公开皆可 |
| `BINANCE_ENABLE_LIVE_WRITE_TESTS` | 允许 Binance 现货、杠杆与子账户写入 |
| `BINANCE_PERP_ENABLE_LIVE_WRITE_TESTS` | 允许 Binance 永续写入 |
| `OKX_ENABLE_LIVE_WRITE_TESTS` | 允许 OKX 写入 |
| `BYBIT_ENABLE_LIVE_WRITE_TESTS` | 允许 Bybit 写入 |
| `BITGET_ENABLE_LIVE_WRITE_TESTS` | 允许 Bitget 写入 |
| `HYPERLIQUID_ENABLE_LIVE_WRITE_TESTS` | 允许 Hyperliquid 写入 |
| `LIGHTER_ENABLE_LIVE_WRITE_TESTS` | 允许 Lighter 写入 |
| `BOLTER_ENABLE_NADO_UNSAFE_RAW_SDK_WRITES` | 允许一次绕开装置边界的裸 Nado SDK 写入 |
| `RUN_FULL` | 跑扩展验证测试 |
| `RUN_SOAK` | 跑长时间的流测试 |

写闸门与读闸门有一处不同，值得知道：写闸门开着而凭证缺失时，测试是失败，不是跳过。你是显式
要求写的，而一次静默的跳过看上去就像一个通过了的写测试。

最后那个 Nado 闸门的名字是故意起成那样的。

## 选择器

有几个写测试作用在一笔你已经挂出去的单上，或者作用在一个由你选定的数量上，而不是作用在某个
默认值上：`BINANCE_SPOT_TEST_ORDER_ID`、`OKX_TEST_ORDER_PRICE`、`BINANCE_PERP_TEST_ORDER_QTY`
以及它们的同类。每一个都只属于一个测试，所以本页不列举它们。带着闸门跑那个测试，它会失败并
准确说出它想要的那些变量。

原本在这里的两道安全机制已经没有了，而它们的缺席是刻意的，不是疏漏。**不再有按交易所的最大
名义额上限**：那些边界的存在是为了给 demo 交易所上的单封顶，而给一笔你有意识地对着生产开闸的
单封顶，并不是同一种保护。**也不再有 endpoint 覆盖**：那些变量每一个选的都是一个 demo 或
testnet 主机，主机没了，也就没有什么还能被指向别处。你传进去的数量就是会成交的数量。

## 还有两个

`GATEWAY_TEST_POSTGRES_DSN` 把 `make test-gateway-postgres` 与 `make test-gateway-restart` 指向一个
真实的 PostgreSQL。不设它，它们直接失败而不是跳过。`make test-compose-postgres` 会起一个用完即弃
的集群并替你设好。

`PROXY` 接受 `http`、`https` 或 `socks5` URL，可以带凭证，而那些凭证在任何可能露出来的地方都会被
脱敏。Binance、OKX、Aster 与 EdgeX 在 REST 上读它；Binance、OKX、Aster、Bybit、Bitget、Gate、
Hyperliquid 与 Nado 在 WebSocket 上读它。Backpack、GRVT、Lighter 与 StandX 完全不理它。

它的优先级并不统一，而当你在系统层面设了代理时，这个差别是要紧的。在 REST 上 `PROXY` 优先：
看见它的客户端会装上自己的 transport。没设时，那个客户端会落到 Go 的默认 transport，而后者
是认继承来的 `HTTP_PROXY` 与 `HTTPS_PROXY` 的——所以即便 `PROXY` 什么都没说，一个环境里的代理
依然会生效。在 WebSocket 上多数交易所把这个顺序反过来，让继承来的变量优先，Gate 又把它反回去，
`ALL_PROXY` 作为最后的兜底被读取，而每个交易所都会对 loopback 主机绕开代理。

由此带来的实际后果是：一个连不上交易所的环境代理，会让一些从头到尾没提过代理的包测试失败。
把那些变量为这次运行清掉，而不是去交易所那边找 bug。

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
