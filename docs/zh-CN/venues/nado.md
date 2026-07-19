# Nado

[English canonical](../../venues/nado.md) · 本中文页为维护镜像。

## 归属与范围

本页负责说明 Nado Testnet 产品行为、基于 digest 的订单身份、账户语义、验收
命令，以及以服务端为权威的容量边界。

## 环境、产品与身份

Runtime adapter 通过一个统一保证金账户覆盖 Nado Testnet 上仅使用已有资金的
Spot 和 Perp，该账户表示为 `NADO-001`。私有操作使用签名密钥和所选 subaccount。
准确的环境变量和安全门禁统一记录在[配置](../reference/configuration.md)中。

Spot 订单始终设置交易所 `spot_leverage=false`：受支持的 Spot 范围只使用已有
库存，不进行借贷。

## 已实现范围（Implemented surface）

| 产品 | 市场请求 | 标准化流 | 命令 |
| --- | --- | --- | --- |
| Spot no-borrow | `OrderBook`；`Bars` unsupported | 配置 WS backend 后提供 book、quote 和 trade 流；以及已配置的私有 execution/account 事件 | `Submit`、`Cancel`、`CancelAll`；`Modify` unsupported |
| Perp | `OrderBook`；`Bars` unsupported；衍生品参考数据和当前 OI | 配置后提供 book、quote、trade、funding-reference、execution 和 account 流 | `Submit`、`Cancel`、`CancelAll`；`Modify` unsupported |

### 订单与容量

Nado 要求 `ClientID`。它在 net position mode 下接受 limit 和 market 订单；
market 订单要求 IOC 语义。支持的 limit TIF 是 GTC/default、IOC、FOK 和
GTX/post-only。Spot 拒绝 `ReduceOnly`；Perp 支持该字段。trigger、activation、
trailing 字段，以及 margin-mode mutation 均 unsupported。`SetLeverage` 仍保留在
公共 Perp REST surface 中，但 Nado 把它视为经过校验的 no-op：调用成功并返回
`Leverage.Effective=0`，因为实际杠杆由后端 risk engine 决定。

订单提交不接受也不要求 `max_order_size` 参数。为保持 API 保真度，Nado SDK
保留 `GetMaxOrderSize` 作为单独的原始查询，但 adapter submit 路径绝不会调用
它。本地不存在交易所容量 lease 或 admission protocol。路径依次是无副作用的
`ValidateSubmit`、可选的通用 runtime risk、普通 adapter `Submit`，然后由服务端
作出权威接受或拒绝。测试 notional 限额是安全边界，不是对交易所剩余容量的
建模。

Adapter 会临时准备一个签名订单，只执行一次，随后抹除准备材料，并要求响应
digest 与签名 digest 匹配。该 digest 就是用于状态查询和取消的精确
`VenueOrderID`。

### 报告与账户语义

两个产品都提供开放订单，以及通过 digest 和已存 client correlation 进行的精确
单订单恢复。成交报告使用有界 archive `matches` 历史（默认 100，最大 500）。
Spot 不提供持仓报告；Perp 持仓来自账户快照。Mass status 会组合开放订单、有界
成交和 Perp 持仓，并记录不完整覆盖，而不会把部分页面冒充为完整结果。

Nado 账户状态的语义不同于传统 free/locked balance：

- 账户快照必须包含 initial、maintenance 和 unweighted health。
  `AvailableCollateral` 是 `max(initial health, 0)`，`Equity` 是 unweighted
  health。两者都不是某种货币的 free balance。
- Venue account response 没有 event timestamp。SDK 将其包装为
  `AccountSnapshot` 并记录本地 `ReceivedAt`；freshness 使用该接收时间，但不得
  将它呈现为 exchange event timestamp。
- 每个 Spot `balance.amount` 都是带符号库存。负数 amount 仍然是负的 `Total`，
  并产生 `Borrowed=abs(amount)`；adapter 不会虚构 `Free`、`Locked` 或
  `Available` 值。
- Perp `v_quote_balance` 是交易所会计状态，而不是 free collateral。Position
  quantity 来自带符号的 Perp balance amount。

### 仅隔离保证金的 Perp 产品

发现流程会保留交易所的 `isolated_only` 标志。对于这类 Perp 产品，开仓订单会
设置 isolated appendix，并按 `price × quantity × contract multiplier` 提供 1x
initial margin，向上舍入到六位小数。reduce-only 平仓会保留 isolated appendix，
但新增 opening margin 为零。该行为是 adapter 对交易所强制隔离产品的编码，
不会形成通用 runtime margin policy。

## 衍生品参考数据

Perp 通过 REST 提供当前 funding、mark、index 和 oracle price，并通过 WS 提供
部分 funding 更新。当前 OI 直接从 `all_products` 读取，runtime 不缓存它。不声明
支持 funding history。Spot 不提供衍生品参考数据表面。

`WatchMarkPrice` 仍保留在公共 Perp WebSocket interface 中以保持 API 对称性，
但 Nado 不提供 mark-price subscription，并会立刻返回 `ErrUnsupported`。需要
Nado reference price 时，请使用 REST 的 mark/index/oracle price surface。

## 验证命令

```sh
make test-nado-testnet-read
make test-nado-testnet-runtime-spot
make test-nado-testnet-runtime-perp
make test-nado-testnet-acceptance
make test-nado-testnet-reference-data-read
```

聚合目标通过零跳过运行器执行 read、adapter-write 和 runtime-write 覆盖。写入
目标使用[配置](../reference/configuration.md)中记录的 Nado Testnet 门禁。

## 验收、清理与歧义

Spot 和 Perp 验收会挂出并取消一个挂单、执行一次有界 IOC 成交、平掉由此
产生的敞口，并对账最终状态。Spot 使用所选余额基线；Perp 要求所选持仓回到
flat。清理跟踪精确 digest 和测试创建的敞口，而不是把宽泛的 `CancelAll` 作为
主要机制。

Testnet 失败或歧义结果必须通过精确 digest 状态、开放订单、有界匹配成交和产品
范围内的账户证据来解析。绝不能在本地推断剩余容量、盲目重试 submit/close，
也不能假设空的开放订单结果意味着订单从未成交。

参见[执行与风险](../concepts/execution-risk.md)、
[运维与恢复](../guides/operations-recovery.md)和
[能力矩阵](../adapter-capabilities.md)。
