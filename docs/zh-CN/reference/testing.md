# 测试

> 英文为规范版本。[English](../../reference/testing.md)

本页是一份清单：你能跑什么，以及跑绿了能证明什么。默认路径是离线的、不需要凭证的、确定性的；
所有会碰到交易所的东西，都是单独的、被显式命名的 target。

## 默认路径

```sh
make test
```

对整个仓库跑 `go test -short ./...`。它不需要凭证、不需要 Docker、不需要网络。这是每次提交
之前该跑的那道门。

| Target | 范围 |
| --- | --- |
| `make test` | 整个仓库，short 模式 |
| `make test-sdk` | 只跑 `./sdk/...` |
| `make test-exchange-offline` | `./exchange/...`，带 `-count=1` |
| `make test-exchange-race` | `./exchange/...` 跑在 `-race` 下 |
| `make test-exchange-redaction` | 那六个「凭证绝不会进入错误字符串」的测试 |
| `make test-exchange-quality` | 先 offline，再 redaction，再 race |
| `make test-gateway-offline` | `./gateway/...` 与 `./cmd/exchange-gateway` |
| `make test-gateway-contract` | `contracts`、`domain`、`capability`、`store` |
| `make test-gateway-security` | 引用边界、OpenAPI、配置脱敏、验收对照表 |
| `make test-gateway-race` | gateway 各包跑在 `-race` 下 |
| `make test-gateway` | 先 offline，再 race，再 security |
| `make test-browser` | Web 应用：安装、构建、Vitest、Playwright |

不带 `-short` 的 `go test ./...` 不是安全的默认值。那些能够碰到交易所的测试，会在 short 模式
关闭时去检查环境。

有两个 gateway target 需要一个真实的 PostgreSQL，并从环境里读 `GATEWAY_TEST_POSTGRES_DSN`；
变量没设时它们直接失败，而不是跳过。`make test-gateway-postgres` 覆盖迁移、命令存储、审计存储
与账户归属。`make test-gateway-restart` 覆盖协调器的进程崩溃窗口。`make test-compose-postgres`
会替你起一个用完即弃的集群并把这两者都跑掉，那条路更省事。

## 容器门禁

```sh
make test-compose
```

六个子 target，按顺序：

| Target | 它证明什么 |
| --- | --- |
| `test-compose-config` | Compose 文件能渲染，各二进制能加载自己的配置 |
| `test-compose-build` | 两个镜像都能构建 |
| `test-compose-security` | 非 root 用户、固定入口点、镜像里没有源码与密钥、七个二进制齐全 |
| `test-compose-smoke` | 迁移跑完、服务起来、gateway 能答自己的 live 与 ready 探针，且每个服务在收到 `SIGTERM` 后 15 秒内以 `0` 退出 |
| `test-compose-persistence` | 数据能挺过一次容器重建 |
| `test-compose-postgres` | 真实 PostgreSQL 的那两套，跑在用完即弃的集群上 |

每一个都会建自己的 Compose 项目，用生成的凭证和由内核分配的端口。它们都不要求你先跑过
`make up`，也都不会打扰一个已经起着的栈。

## 外部验收

下面的每一条都会在真实的非生产交易所下真实的单。每个 target 都在命令内联地设置自己的写闸门
——写闸门也只该这么设。见[配置](configuration.md)。

### Exchange 层

九个交易所，聚合为 `make test-exchange-acceptance`。

| 环境 | 聚合 target | Product target |
| --- | --- | --- |
| Binance Demo | `test-exchange-binance-demo-acceptance` | `-spot`、`-perp` |
| OKX Demo | `test-exchange-okx-demo-acceptance` | `-spot`、`-perp` |
| Bybit Demo | `test-exchange-bybit-demo-acceptance` | `-spot`、`-usdt-perp`、`-usdc-perp` |
| Bitget Demo | `test-exchange-bitget-demo-acceptance` | `-spot`、`-usdt-perp`、`-usdc-perp` |
| Gate Testnet | `test-exchange-gate-testnet-acceptance` | `-spot`、`-usdt-perp` |
| Aster Testnet | `test-exchange-aster-testnet-acceptance` | `-spot`、`-usdt-perp` |
| Nado Testnet | `test-exchange-nado-testnet-acceptance` | `-spot`、`-usdt0-perp` |
| Hyperliquid Testnet | `test-exchange-hyperliquid-testnet-acceptance` | `-spot`、`-perp` |
| Lighter Testnet | `test-exchange-lighter-testnet-acceptance` | `-spot`、`-perp` |

Product target 的名字就是聚合 target 的前缀加上后缀，所以 Bybit 的 USDC 永续是
`make test-exchange-bybit-demo-usdc-perp`。

### Gateway product 行

二十个 target，一个 product 行一个，聚合为 `make test-gateway-demo-testnet`：

| 交易所 | Target |
| --- | --- |
| Binance | `test-gateway-binance-spot-demo`、`test-gateway-binance-usd-perp-demo` |
| OKX | `test-gateway-okx-spot-demo`、`test-gateway-okx-usdt-perp-demo` |
| Lighter | `test-gateway-lighter-spot-testnet`、`test-gateway-lighter-perp-testnet` |
| Hyperliquid | `test-gateway-hyperliquid-spot-testnet`、`test-gateway-hyperliquid-perp-testnet` |
| Bybit | `test-gateway-bybit-spot-demo`、`test-gateway-bybit-usdt-perp-demo`、`test-gateway-bybit-usdc-perp-demo` |
| Bitget | `test-gateway-bitget-spot-demo`、`test-gateway-bitget-usdt-perp-demo`、`test-gateway-bitget-usdc-perp-demo` |
| Gate | `test-gateway-gate-spot-testnet`、`test-gateway-gate-usdt-perp-testnet` |
| Aster | `test-gateway-aster-spot-testnet`、`test-gateway-aster-usdt-perp-testnet` |
| Nado | `test-gateway-nado-spot-testnet`、`test-gateway-nado-usdt0-perp-testnet` |

这些 target 是按 gateway 对外暴露的 product 行命名的，而每一个跑的都是该行对应的 exchange 层
验收测试。这套命名的意义在于：一个关于某个 gateway product 行的断言，能映射到恰好一条命令
——而不是让同一个测试被数两遍。

## 闸门、skip 与串行执行

**写闸门是命令内联的。** 每个外部 target 自己在命令前面加
`BOLTER_ENABLE_<VENUE>_<ENV>_WRITES=1`。把其中任何一个导出到 shell 配置或 `.env` 文件里，
都会让此后每一次 `go test` 都变成一次潜在的交易所写入。别这么干。

**skip 就是失败。** 外部 target 都经由 `internal/testenv/cmd/noskipgotest` 运行：一个测试都没
跑到，它失败；有任何测试被跳过，它也失败。因此缺凭证、product 不可用、或者选择器什么都没匹配
上，都不可能被误认为通过。

**执行是串行的。** Makefile 声明了 `.NOTPARALLEL`，它把同一次调用内部的前置目标串起来。它并
不串行化彼此独立的 `make` 进程。一次只跑一个会写入的 target，等到终态验证完成再跑下一个。

## 跑绿了能证明什么

退出码为零，只能证明那个装置所编码的 product、symbol 与 account 范围，不能更宽。它不证明账户
整体为空，不证明主网行为，也不证明生产就绪持续成立。

非零、超时、skip，或者一次不明确的提交，都不能证明 cleanup 跑过了。在做补救之前，先查清由装置
持有的那些订单 ID 和精确的 product 范围；绝不盲目撤销无关订单，也绝不平掉未经证实的持仓。见
[运维与恢复](../guides/operations-recovery.md)。

## 发布证据

一条 Demo/Testnet 认证的断言——这个术语在[术语表](glossary.md)里有定义——必须写明环境与精确的
product 范围、精确的 Make target、zero-skip 结果及其日期，以及任何会收窄它的限制。

发布这份摘要，别的都不发。不含凭证、不含原始日志、不含账户残留、不含本地路径。「已实现」「有
能力」「有外部证据」始终是三条彼此独立的断言。
