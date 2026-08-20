# 测试

> 英文为规范版本。[English](../../reference/testing.md)

本页是一份清单：你能跑什么，以及跑绿了能证明什么。每个 target 都是离线的、不需要凭证的、
确定性的。没有任何 target 会碰到交易所，也没有任何 target 会下单。

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

不带 `-short` 的 `go test ./...` 不是安全的默认值。`sdk/` 下有少数几个测试会在 short 模式关闭
时去读交易所的 public endpoint。没有任何 Make target 会跑它们。

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

## 外部验收去哪了

这里原本还有第三层：九个按交易所划分的验收聚合、二十个 gateway product 行 target，以及为它们
开闸的 `BOLTER_ENABLE_<VENUE>_<ENV>_WRITES` 变量。它们全都会在交易所的 demo 或 testnet 环境下
真实的单。

这一整层已经删除，和它所依赖的那些环境一起。每个交易所实现 demo 与 testnet 的方式都不一样
——这家要单独的 WebSocket 后端，那家靠一个请求头，再换一家又是另一套签名域或 chain ID——
维持它能跑的成本，高于它产出的证据的价值。Go 侧没有任何东西接替它，因为接替者必须花真钱。

只剩一条交互路径，而且它不是测试 target：运行中的真实栈。匿名浏览器会话为只读，无需
交易所凭证，读取实时公共行情；登录并配置账户后才会启用私有面和写入面，而这些调用会
直达生产环境。本仓库不对外部可用性作任何断言。

## 闸门与串行执行

**没有任何 recipe 会为带凭证的写入开闸。** `TestMakefileRecipesNeverEnableCredentialedWrites`
会读 Makefile 里的每一条 recipe，一旦某条设置了开闸变量或导出了凭证材料就失败。默认路径是
离线的——这是构造上的性质，不是约定俗成，所以 `make test` 不需要先读一遍再跑。

**执行是串行的。** Makefile 声明了 `.NOTPARALLEL`，它把同一次调用内部的前置目标串起来。它并
不串行化彼此独立的 `make` 进程。

## 跑绿了能证明什么

退出码为零，只能证明规范化实现与套件所携带的 fixture 依然一致，不能更宽。它不证明交易所今天
还会这么答，不证明某个 endpoint 还存在，也不证明某份凭证会被接受。每一份 fixture 都是过去某次
响应的一张照片。

PostgreSQL 与 Compose 那几套是例外：它们跑在真实数据库和真实容器上，因此它们对这两者所断言
的内容，是真的被证明了的。

如果在真实交易所那边确实出了问题，恢复规则并没有随这套装置一起改变。见
[运维与恢复](../guides/operations-recovery.md)。

## 发布证据

一条 Offline-conformant 的断言——这个术语在[术语表](glossary.md)里有定义——必须写明精确的
product 范围、精确的 Make target、zero-skip 结果及其日期，以及任何会收窄它的限制。

zero-skip 这一半不是跑绿了就能知道的：一个 test 被跳过和它通过，`go test` 的退出码同样是零；
`-run` 模式一个都没匹配上时，退出码也还是零。`internal/testenv/cmd/noskipgotest` 读 `-json`
流，这两种情况都判失败——把你要断言的那个 target 的命令体传给它：

```bash
go run ./internal/testenv/cmd/noskipgotest -- -short ./exchange/... -count=1
```

这条命令就是 `make test-exchange-offline`。这个 wrapper 过去用来跑外部验收 target，那时一次
skip 意味着某份凭证不在；现在它服务于离线断言，一次 skip 意味着证据比 target 名字看上去的更窄。

发布这份摘要，别的都不发。不含凭证、不含原始日志、不含账户残留、不含本地路径。「已实现」「有
能力」「有外部证据」始终是三条彼此独立的断言。
