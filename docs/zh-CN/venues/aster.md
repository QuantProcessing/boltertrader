# Aster

[English canonical](../../venues/aster.md) · 本中文页为维护镜像。

## 归属与范围

本页负责说明 Aster Testnet 产品配置、验收命令和 runtime 注意事项，包括
fixture 与未平仓量的数据来源边界。

## 环境、产品与身份

Runtime adapter 覆盖 Aster 官方 V3 Testnet profile 上的 Spot cash 和
USDT-linear Perp。私有操作使用 API 钱包地址及其 EIP-712 签名密钥；可选的
预期签名者仅用于身份校验，并不代表另一个账户。准确的凭证名和门禁名由
[配置](../reference/configuration.md)统一定义。

标准化交易所 symbol 以 `TEST` 开头的标的仅用于 fixture，不会进入发现和写入
选择。

## 已实现范围（Implemented surface）

| 产品 | 市场请求 | 标准化流 | 命令 |
| --- | --- | --- | --- |
| Spot | `OrderBook`；`Bars` unsupported | Book、quote 和 trade 市场流，以及已配置的 execution/account 流 | `Submit`、`Cancel`、`CancelAll`；`Modify` unsupported |
| USDT-linear Perp | `OrderBook`；`Bars` unsupported；衍生品参考数据、资金费率历史和当前 OI | 配置后提供 book、quote、trade、reference、execution 和 account 流 | `Submit`、`Cancel`、`CancelAll`；`Modify` unsupported |

账户状态是权威 REST 快照。流事件会更新标准化的订单、成交、余额和持仓状态，
但不会扩大受支持的请求方法范围。

### 订单与账户变更

- Spot 和 Perp 接受 limit 与 market 订单。Limit TIF 可以是 GTC/default、IOC、
  FOK 或 GTX。Market 订单拒绝显式 TIF 和显式 limit price。
- Spot 仅支持 cash，并拒绝 `ReduceOnly` 和非 net position side。
- Perp 仅支持 one-way/net，并支持 `ReduceOnly`；hedge position mode unsupported。
- 两个 adapter 都未实现（not implemented）leverage 和 margin-mode mutation。

### 报告、持仓与账户状态

两个产品都提供开放订单、精确单订单状态和有界 `my trades` 成交报告。Execution
mass status 将每次成交查询限制在 1,000 条，并会标记覆盖不完整，而不是暗示其
提供无界账本。仅凭开放订单中不存在某订单，不能证明订单终态。

Spot 提供余额/账户快照，不提供持仓报告。Perp 持仓来自账户快照。配置后的私有
流会为两个产品提供增量 execution 和 account 事件。

## 衍生品参考数据与来源

Perp 通过 REST 提供当前 funding、mark 和 index price，通过 mark-price WS
提供更新，并提供有界 funding history。当前 OI 是对探测确认的 V3 路由
`/fapi/v3/openInterest` 发起的直接、无缓存请求。

仓库中的 Aster fixture 是 fixture manifest 所声明来源经净化得到的合成衍生
数据，并非捕获的账户数据。大多数 declared source 是官方示例。由于检查过的
V3 Markdown 中没有 OI 路由，OI fixture 来自另行记录的 Testnet probe
evidence。如果该路由不可用或不兼容，SDK 会返回带类型的 unavailable error。
它不得回退到 V1 路由，也不得从其他字段合成 OI。

## 验证命令

```sh
make test-aster-testnet-read
make test-aster-testnet-runtime-spot
make test-aster-testnet-runtime-perp
make test-aster-testnet-acceptance
make test-aster-testnet-reference-data-read
```

聚合目标通过零跳过运行器执行 read、adapter-write 和 runtime-write 目标。写入
目标受[配置](../reference/configuration.md)中记录的 Aster Testnet 门禁保护。

## 验收、清理与歧义

Spot 和 Perp 验收会挂出并取消一个挂单、执行一次有界 IOC 成交、平掉由此
产生的敞口，并对账最终状态。Spot 清理使用所选基础币余额基线和 dust tolerance；
Perp 清理要求所选持仓回到 flat。若预先存在开放订单或 Perp 持仓，零跳过目标
会失败，而不是悄悄为脏账户给出认证。

如果命令结果存在歧义，必须先解析精确订单状态、开放订单、有界匹配成交及所选
余额/持仓，再进行另一次写入。清理仅限测试创建的身份和敞口；不得从空的开放
订单快照推断成功，也不得用全账户取消替代精确证据。

参见[配置](../reference/configuration.md)、
[运维与恢复](../guides/operations-recovery.md)、
[测试](../reference/testing.md)和[能力矩阵](../adapter-capabilities.md)。
