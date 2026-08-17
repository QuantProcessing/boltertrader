# 运行本地栈

> 英文为规范版本。[English](../../getting-started/local-stack.md)

本页归属本地栈的生命周期：`make up` 启动了什么、如何判断它是否健康、以及如何
把它停掉。先安装工具——[前置条件](prerequisites.md)。然后在
[下第一笔订单](first-order.md)中下单。

## 启动

```sh
make up
```

依次发生三件事：

1. `go run ./internal/devsetup/cmd/bootstrap` 把栈的凭证生成到 `.secrets/test/`，
   并写出 `.env.stack`。重复执行是幂等的。
2. `docker compose --env-file .env.stack -f compose.yaml up -d --build --wait`
   构建并启动容器，最多等待 180 秒的健康检查。
3. `make doctor` 运行，逐项报告栈的每个部分。

首跑——且只有生成它的那一次——bootstrap 会打印 bootstrap operator 的用户名与
密码。那是它唯一一次以可读形式出现：栈只保存 bcrypt 哈希。同一个值也会以 `0600`
写入 `.secrets/test/bootstrap_password`。要重置它，删除
`.secrets/test/bootstrap_password_hash` 后再次 `make up`。

默认用户名是 `operator`，可用 `BT_BOOTSTRAP_USERNAME` 覆盖。

## 运行了什么

八个容器，其中三个跑完即退出：

| 容器 | 角色 |
| --- | --- |
| `postgres` | 单实例，三个数据库，各有一个属主角色 |
| `gateway-migrate` | 应用 gateway 的 migration 后退出 |
| `trading-migrate` | 应用 trading 服务的 migration 后退出 |
| `authedge-migrate` | 应用 auth edge 的 migration 后退出 |
| `gateway` | 共享能力面 |
| `trading` | 手动交易与资金费套利 |
| `authedge` | 浏览器会话与转发 |
| `web` | Caddy，托管前端并反向代理 `/api/*` |

发布出来的端口，全部只绑定 `127.0.0.1`：

| 地址 | 服务 |
| --- | --- |
| `http://127.0.0.1:18090` | Web——你要打开的就是这个 |
| `http://127.0.0.1:18091` | Auth edge |
| `http://127.0.0.1:18092` | Trading 服务 |
| `http://127.0.0.1:18080` | Gateway |
| `127.0.0.1:15432` | PostgreSQL |

只有 web 端口是给浏览器用的。其余四个发布出来是为了 `make doctor` 和排障。

要改端口，就设在启动栈的那条命令上：

```sh
POSTGRES_PORT=15433 GATEWAY_PORT=18081 make up
```

这些值会落进 `.env.stack`，之后每次 `make doctor`、`make logs` 和 `make down`
都从那里读回。

## 检查

```sh
make doctor
```

它每项检查输出一行，且绝不提前退出——一次发现五个问题的运行会一次性全说完。
检查按依赖顺序进行：`docker`、`.env.stack`、`credentials`、`credential store`、
`postgres`、`postgres roles`、`gateway`、`trading`、`auth edge`、`web`。每条失败
行后面都跟着修复它的命令。

`skip` 不等于通过。它表示这项检查因为所依赖的东西已经失败而无法运行——
「postgres 挂了所以下游全都没测」与「下游检查都通过了」是两个不同的答案。

gateway 与 trading 探针是带认证的：它们从 `.secrets/test/` 读取生成的服务令牌，
再调用各自的 readiness 端点。auth edge 探针期望无会话时 `/api/v1/me` 返回
`401`，因为那才是正确答案。

## 观察

```sh
make logs
```

跟随所有容器，附带最后 200 行回滚。

## 停止

```sh
make down
```

停止并删除容器。卷会保留，因此下一次 `make up` 回来时 account、订单和历史都还在。

```sh
make down-hard
```

额外加上 `--volumes`。PostgreSQL 中的一切都会被销毁，包括你的 account 和通过界面
注册的 venue 凭证。`.secrets/` 中生成的文件不受影响，因此 bootstrap 密码仍然有效。

## 状态存在哪里

- **PostgreSQL** 保存 gateway 的 account、catalog 与持久订单；trading 服务的命令
  账本与 projection；以及 auth edge 的会话。三个数据库，各有一个属主角色，任何服务都不读另一个的表。
- **`.secrets/test/`** 保存服务令牌、凭证库主密钥、gateway 的 DSN，以及 bootstrap
  密码与其哈希。目录 `0700`，每个文件 `0600`。
- **`.secrets/store/`** 是 gateway 的加密 venue 凭证库。它是一个 bind mount，也是
  整套栈里唯一一个被服务写入的路径。
- **`.env.stack`** 只保存端口、uid 和那些密钥文件的路径，不含其他内容。它不是
  `.env`，栈也从不读 `.env`——见[前置条件](prerequisites.md)。

四者都被 Git 忽略。

## 排障

| 现象 | 原因 | 处理 |
| --- | --- | --- |
| `docker: not on PATH` | 缺少 compose 插件 | 安装 Docker Desktop 或 OrbStack |
| `.env.stack: missing or empty` | bootstrap 从未运行 | `make up` |
| 干净 `make up` 之后端口检查失败 | 端口被别的东西占用 | 带覆盖重跑，例如 `WEB_PORT=18095 make up` |
| `credentials` 报告权限被放宽 | 编辑器或备份工具改动过 | `make up` 会重新收紧 |
| `gateway` 或 `trading` 返回非 200 | 服务启动失败 | `make logs` 里有原因 |

更深层的故障——migration 中途停住、订单终态无法确认——归
[操作与恢复](../guides/operations-recovery.md)。

## 后续

[下第一笔订单](first-order.md)会带你走完完整的浏览器闭环，用的是真实 venue account
和真钱。如果想在完全不接 venue 的情况下走同一个闭环，改跑
`npm --prefix web run dev:mock`。
