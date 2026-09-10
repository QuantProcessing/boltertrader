# 运维与恢复

> 本页为[英文规范](../../guides/operations-recovery.md)的中文说明。

交付形态保持现有单机 Compose：一个 PostgreSQL、Gateway、Trading、Auth edge 和 Web。
同一个 Go 镜像还包含 `gateway-market` 与 `gateway-monitor`，无需新增监控服务。
对外端口只绑定本机回环地址。

## 启动与诊断

`make up` 生成缺失的服务凭据、构建镜像、运行三个迁移器、启动服务并检查就绪状态。
`make down` 保留数据库卷；`make down-hard` 会删除数据，只用于明确可以丢弃的数据。
已有数据库发生角色或迁移差异时先检查并制定升级方案，不要把删除卷当成常规修复。

交易所凭证以密文保存在 PostgreSQL，必须同时备份 `.secrets/stack/secret_master_key`。
替换主密钥不能解密原来的账户。`.secrets/stack` 其余文件保存服务令牌与登录初始化材料；
`.secrets/store` 已不再使用。

`gateway_market_hot_instruments{venue,market_family}` 只统计有活跃消费需求的不同标的。
它不是连接数，也不表示每类衍生品数据都已经订阅。旧 `gateway_market_hot_subscriptions`
会把同一数量重复记到多个数据集，现已删除。

`make doctor` 检查 Docker、服务密钥文件、数据库角色与 HTTP 就绪。公开报价默认标为
**未检查**，指定标的才验证真实 BBO：

```sh
go run ./internal/devsetup/cmd/doctor -instrument 'binance:live:spot:BTC-USDT'
```

标的 ID 从 `gateway-market` 的目录获取。诊断验证报价的源时间并释放临时订阅。
进程正常、目录加载成功、某条实时行情可用是三个不同的结论。

需要代理时显式配置：

```sh
GATEWAY_HTTP_PROXY=http://127.0.0.1:10900 make up
```

Bootstrap 会将回环地址改写为 Docker 可达的 `host.docker.internal`。主机直接启动 Go
Gateway 时需要 `HTTP_PROXY`、`HTTPS_PROXY` 和适当的 `NO_PROXY`；仅有 `ALL_PROXY` 不够。
镜像下载依赖另用可选的 `BOLTER_DOCKER_HTTP_PROXY`。代理端口连通不代表交易所接口可用，
以指定标的的报价诊断为准。

## 未知订单

恢复只查询既有订单身份，不自动重发。“结果未知”的状态说明展示持久化任务原因、
已开始查询次数、最近诊断时间和已确认的 Gateway 状态/版本。后续查询失败不会抹掉
原先成交，也不会把未知改成失败。

保留 command、client-order、Gateway-order ID，核对交易所订单、成交及当前持仓后，
再决定新的交易操作。界面没有“认为失败并重发”。新命令创建后 30 秒内没有开始发送会
停止执行；它与“已经开始发送但结果未知”不同。

删除账户仅移除本地凭证与连接，不撤单、不平仓；历史命令和成交证据保留。

## 备份与恢复

备份时停止 web、authedge、trading、gateway 四个应用容器，PostgreSQL 保持运行。
这样三个数据库对应同一个维护时点。使用仓库外的私有目录：

```sh
umask 077
backup_dir="$HOME/bolter-backups/$(date +%Y%m%d-%H%M%S)"
mkdir -p "$backup_dir"
docker compose --env-file .env.stack stop web authedge trading gateway
for database in bolter_gateway bolter_trading bolter_authedge; do
  docker compose --env-file .env.stack exec -T postgres \
    pg_dump -U bolter_bootstrap -Fc "$database" > "$backup_dir/$database.dump" || exit 1
done
cp -R .secrets "$backup_dir/.secrets"
cp .env.stack "$backup_dir/.env.stack"
cp docker/gateway.docker.yaml "$backup_dir/gateway.yaml"
docker compose --env-file .env.stack start gateway trading authedge web
make doctor
```

同时记录代码 commit、镜像版本，以及 `GATEWAY_CONFIG_FILE` 指定的自定义配置。
备份包含账户密钥与登录会话，应保存到独立、持久的私有存储。

恢复到**全新的空部署**，保留原数据：

1. 使用备份时的代码/镜像，先恢复 `.secrets`，目录权限 0700、文件 0600。调整复制的
   `.env.stack` 中主机路径、UID 和端口，并恢复自定义 Gateway 配置。
2. 只启动 PostgreSQL：`docker compose --env-file .env.stack up -d --wait postgres`，
   初始化脚本建立三个数据库与对应角色。
3. 对每个空数据库运行 `docker compose --env-file .env.stack exec -T postgres pg_restore
   -U bolter_bootstrap --exit-on-error -d bolter_gateway < "$backup_dir/bolter_gateway.dump"`，
   对 Trading 和 Auth edge 重复同样操作。保留属主与 ACL，不对现有部署使用 `--clean`。
4. 运行 `make up` 检查迁移校验和及就绪状态，再读取交易所当前事实。恢复的本地命令
   不应作为新意图重新派发。

`make test-compose-recovery` 在临时项目演练中断与三库恢复。
`python3 scripts/acceptance/stack_recovery.py --public-seconds 180` 额外运行 Binance BTC
现货与 OKX BTC 永续监控，跨服务重启记录资源采样，结束后删除全部临时容器、卷与密钥。
只需公开网络，无交易所凭证。三分钟观测不等于长时间稳定性保证。
