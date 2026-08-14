# 前置条件

> 英文为规范版本。[English](../../getting-started/prerequisites.md)

本页归属[运行本地栈](local-stack.md)之前需要安装和确定的内容。venue 凭证见
[配置](../reference/configuration.md)；一次测试运行能证明什么、不能证明什么见
[测试与认证](../reference/testing.md)。

## 工具

| 工具 | 用于 | 说明 |
| --- | --- | --- |
| Docker Engine 与 Compose v2 插件 | 整套栈 | `make doctor` 会执行 `docker compose version --short`；该插件与引擎是两次独立安装 |
| Go | `make up`、`make doctor`、`make test` 以及全部验收目标 | `go.mod` 声明 Go 1.26。容器自带 Go 构建，因此这里只用于容器外执行的命令 |
| Node 22 | 仅 `make test-browser` | web 镜像自己构建前端 |
| Git 与 Make | 全部 | 所有命令都从仓库根目录执行 |

先确认工具链：

```sh
go version
docker compose version
make test
```

`make test` 是离线门禁——`go test -short ./...`。它不访问任何 venue，不需要凭证，
也不需要栈在运行。

## 端口

`make up` 会发布五个端口，且每一个都只绑定 `127.0.0.1`。栈中没有任何东西可以从
另一台机器访问。

| 端口 | 服务 |
| --- | --- |
| 15432 | PostgreSQL |
| 18080 | Gateway |
| 18090 | Web |
| 18091 | Auth edge |
| 18092 | Trading 服务 |

若某个端口已被占用，就在那条命令上覆盖它——例如 `POSTGRES_PORT=15433 make up`。
该值会写入 `.env.stack`，并被 `make doctor`、`make logs` 与 `make down` 记住。

## 两个 env 文件

仓库里可能存在一个装着真实 venue API 凭证的 `.env`。**栈从不读它。**
`Makefile` 中每一次 compose 调用都显式传 `--env-file .env.stack`：

```make
COMPOSE = docker compose --env-file .env.stack -f compose.yaml
```

没有这个 flag，compose 会自动加载 `./.env`，把真实凭证注入到根本不该持有它们的
容器里。`.env.stack` 由 `make up` 生成，只含端口、uid 和指向密钥文件的路径，并被
Git 忽略。不要把两者合并，也不要在手工执行 compose 时省掉这个 flag。

生成的凭证位于 `.secrets/test/`，目录 `0700`，每个文件 `0600`。它们被 Git 忽略，
缺失时由 `make up` 重新生成。

## 非生产 account

只能使用为文档所述 Demo 或 Testnet 环境签发的凭证。使用专用 account，刻意限制
资金，并确保测试所触及的 product scope 中没有无关订单或持仓。

绝不要提交 `.env`，绝不要打印 credential value，也绝不要把 secret 放进命令参数。
变量名及其所属环境见[配置](../reference/configuration.md)。

## 保持写入有界且串行

外部写入测试是带闸门的。运行文档点名的那个精确 Make target：它的 recipe 只为该
process 启用写闸门，并应用该 target 定义时的 timeout 与 no-skip 策略。不要把写
闸门做成常驻的 shell 变量或文件配置，也不要并行运行两个外部写入 target。

非零退出、超时或跳过的运行会使最终 venue 状态无法确认。再次写入前，先按产品自身
的 identity 与 state check 处理——见[操作与恢复](../guides/operations-recovery.md)。
绝不要为了回到干净状态而取消或平掉无关的 account state。

## 后续

在[运行本地栈](local-stack.md)把栈跑起来，然后在[下第一笔订单](first-order.md)
中下一笔有界的订单。
