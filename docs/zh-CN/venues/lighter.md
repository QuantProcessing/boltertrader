# Lighter

[English canonical](../../venues/lighter.md) · 本中文页为维护镜像。

## 归属与范围

本页负责说明 Lighter Testnet runtime 产品，以及 reference streaming 与普通市场
数据 streaming 的区别。它也定义当前写入验收的有限范围。

## 环境、产品与身份

Runtime adapter 通过一个统一 Lighter account index 覆盖 Lighter Testnet 上的
Spot cash 和 Perp，默认逻辑账户表示为 `LIGHTER-001`。认证写入需要 account
index、API-key index 和匹配的 private key。只读构造仍会解析已配置的 account 和
market index。准确的凭证名和门禁名请参见
[配置](../reference/configuration.md)。

## 已实现范围（Implemented surface）

| 产品 | 市场请求 | 标准化市场流 | 私有流 |
| --- | --- | --- | --- |
| Spot | `OrderBook`、`Bars` | `SubscribeBook`、`SubscribeQuotes` 和 `SubscribeTrades` unsupported | Execution 和 account 流 unsupported；`Adapter.Start` 被有意设为 no-op，runtime 对账使用 REST。 |
| Perp | `OrderBook`、`Bars`；当前衍生品参考数据和 OI | 只有 `SubscribeReference` 已接通，使用 WS `market_stats` 提供 funding、mark 和 index 字段。Book、quote 和 trade 订阅仍 unsupported。 | Execution 和 account 流 unsupported；runtime 对账使用 REST。 |

粗粒度 dynamic market-stream bit 可能为 true，因为存在 WS client。它只表示 Perp
reference subscription 可以连接；不能作为 book、quote 或 trade stream 的证据。

两个产品都实现 `Submit`、`Cancel`、`CancelAll` 和 `Modify`。

### 订单与账户变更

- 只接受 limit 订单。支持的 TIF 是 GTC/default、IOC 和 GTX/post-only；FOK 和
  其他 TIF unsupported。GTC/default 会编码为 28-day 交易所 `GoodTillTime`，并非
  无界生命周期。
- 只支持 net position mode。Spot 拒绝 `ReduceOnly`；Perp 保留该字段。
- Unified account client 暴露 leverage 和 cross/isolated margin-mode 代码路径，
  但其 adapter/SDK leverage-parameter conversion 不是稳定的公共 contract，Spot
  调用不会在本地被拒绝，当前 Testnet 验收也未执行任一方法。不要使用这些
  mutation；它们不属于文档化的可用表面。

### 报告、持仓与账户状态

提供开放订单报告，但精确单订单状态通过开放订单路径 implemented。因此，缺少订单代表
歧义，而不是终态证据。成交历史报告 unsupported。Mass status 可以报告开放订单和 Perp
账户持仓；当 pagination 或 client-ID reconstruction 阻止完整覆盖时会给出警告；
成交仍不可用。

REST account state 为统一账户提供 balance、collateral/margin value 和 Perp
position。没有 Spot position model，也没有 account stream。

## 衍生品参考数据

Perp 提供 REST funding 快照、带 funding/mark/index 字段的 WS `market_stats`
reference stream，以及来自 order-book detail 的当前未平仓量。当前 OI 直接查询，
不会由 runtime 缓存。Spot 没有衍生品参考数据表面。

## 验证命令

```sh
make test-lighter-testnet-runtime-spot
make test-lighter-testnet-runtime-perp
make test-lighter-testnet-acceptance
make test-lighter-testnet-reference-data-read
```

聚合目标在仓库的零跳过运行器下组合 read 和 write 目标。只有一次有记录、成功且
零跳过的运行，才能证明被明确命名的 Testnet 范围。

## 验收、清理与歧义

当前 Spot 和 Perp 写入验收有意窄于完整命令表面：它会挂出一个 GTX 挂单，
证明该订单未成交，取消该精确订单，并执行最终 REST 对账。它不证明成功成交、
正常平仓、成交历史或私有流交付。

测试账户开始时不得有冲突的开放订单或持仓。清理会跟踪精确 client/venue 身份。
如果 submit visibility 存在歧义，它会有界轮询精确/开放证据，并且仅在账户证据
显示敞口变化时执行有界敞口清理；不会盲目重新提交，也不会使用宽泛的
`CancelAll` 作为主要清理路径。

参见[市场与参考数据](../guides/market-reference-data.md)、
[运维与恢复](../guides/operations-recovery.md)、
[测试](../reference/testing.md)和[能力矩阵](../adapter-capabilities.md)。
