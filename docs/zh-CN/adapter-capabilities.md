# Adapter 能力矩阵

> [English canonical](../adapter-capabilities.md) · 本中文页为维护镜像。

本页负责逐产品限定的静态 runtime adapter 矩阵。它不枚举每个底层 SDK 包、每种
已配置的动态流，也不代表生产就绪。具体方法和配置注意事项请参阅
[交易所指南](venues/README.md)，缺失/deferred 维度请参阅
[Unsupported 和 SDK-only 表面](reference/unsupported.md)，证据措辞请参阅
[测试](reference/testing.md)。

## 如何阅读矩阵

- 无限定词的 `yes` 表示静态行声明了该项准确的标准化能力。带注释的单元格会在
  声明与当前较窄或较宽的具体方法行为不一致时，同时记录两者。
- 无限定词的 `unsupported` 表示静态行没有声明命名的标准化报告表面；注释会指出
  任何令字面声明不完整的可调用方法。两种形式都不表示交易所本身缺少等价 API。
- 流列是粗粒度声明。它们并不意味着每一种 book、quote、trade、reference、
  order、fill、balance 和 position 订阅都存在。
- 所有当前 runtime 产品行都必须具备 `Account-state snapshot`；它提供账户就绪状态、
  余额、保证金摘要、身份和新鲜度。Order/fill/position report 仍是独立的强类型
  domain。
- `Mass status` 只列出 execution adapter 直接负责的 domain。仅账户快照不会因此
  变成 execution mass-status 支持。
- 完整的开放订单覆盖可以证明某个限定范围的订单不再开放；它不能虚构终止原因或
  缺失的成交历史。
- 验收目标命名一条 opt-in 仓库路径。它本身不是带日期的通过记录，也不是生产认证。

## 静态 runtime 产品矩阵

| 交易所 | 产品 | 市场流 | 私有订单流 | 账户流 | 账户状态快照 | Submit | Cancel | Modify | 订单状态报告 | 成交报告 | 持仓报告 | Mass status | 单订单查询 | 仅开放订单注意事项 | 延迟时间戳 | 验收目标 |
| --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- | --- |
| ASTER | Spot cash | yes | yes | yes | yes | yes | yes | no | open orders | my trades | unsupported | open orders, bounded fills | venue order id | yes | no | make test-aster-testnet-runtime-spot |
| ASTER | USDT-linear Perp | yes | yes | yes | yes | yes | yes | no | open orders | my trades | account snapshot | open orders, bounded fills, positions | venue order id | yes | no | make test-aster-testnet-runtime-perp |
| NADO | Spot no-borrow | yes | yes | yes | yes | yes | yes | no | open orders | matches | unsupported | open orders, bounded fills | order digest | yes | no | make test-nado-testnet-runtime-spot |
| NADO | Perp | yes | yes | yes | yes | yes | yes | no | open orders | matches | account snapshot | open orders, bounded fills, positions | order digest | yes | no | make test-nado-testnet-runtime-perp |
| BINANCE | USD-M Perp | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | no | make test-binance-demo-runtime-perp |
| BINANCE | Spot | yes | yes | yes | yes | yes | yes | no | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | no | make test-binance-demo-runtime-spot |
| OKX | USDT-linear SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | unsupported | account snapshot | open-order mass status | unsupported | yes | no | make test-okx-demo-runtime-perp |
| OKX | Spot cash | yes | yes | declared yes; Start wires orders only | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | unsupported | yes | no | make test-okx-demo-runtime-spot |
| BYBIT | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | unsupported | open orders, bounded fills | declared open-order filter; realtime plus filtered history implemented | yes | no | make test-bybit-spot-acceptance |
| BYBIT | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | account snapshot | open orders, bounded fills, positions | declared open-order filter; realtime plus filtered history implemented | yes | no | make test-bybit-usdt-perp-acceptance |
| BYBIT | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded execution history | account snapshot | open orders, bounded fills, positions | declared open-order filter; realtime plus filtered history implemented | yes | no | make test-bybit-usdc-perp-acceptance |
| BITGET | Spot cash | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | unsupported | open orders, bounded fills | declared open-order filter; exact order endpoint implemented | yes | no | make test-bitget-spot-acceptance |
| BITGET | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | account snapshot | open orders, bounded fills, positions | declared open-order filter; exact order endpoint implemented | yes | no | make test-bitget-usdt-perp-acceptance |
| BITGET | USDC-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | yes | open orders | bounded 90-day trade history | account snapshot | open orders, bounded fills, positions | declared open-order filter; exact order endpoint implemented | yes | no | make test-bitget-usdc-perp-acceptance |
| GATE | Spot cash | yes | yes | yes | yes | yes | yes | no | open orders | my trades | unsupported | open orders, bounded fills | venue order id | yes | no | make test-gate-spot-acceptance |
| GATE | USDT-linear Perp/SWAP | yes | yes | yes | yes | yes | yes | no | open orders | my trades | account snapshot | open orders, bounded fills, positions | venue order id | yes | no | make test-gate-usdt-perp-acceptance |
| HYPERLIQUID | Spot cash | no | no | no | yes | yes | yes | yes | open orders | declared unsupported; bounded UserFills filter implemented | unsupported | open-order mass status | declared open-order filter; exact status implemented | yes | no | make test-hyperliquid-testnet-runtime-spot |
| HYPERLIQUID | Perp | yes | yes | yes | yes | yes | yes | yes | open orders | declared unsupported; bounded UserFills filter implemented | account snapshot | open-order mass status | declared venue order ID; mapped client identity also implemented | yes | no | make test-hyperliquid-testnet-runtime-perp |
| HYPERLIQUID | HIP-3 Perp | yes | yes | yes | yes | yes | yes | yes | open orders | declared unsupported; bounded UserFills filter implemented | account snapshot | open-order mass status | declared venue order ID; mapped client identity also implemented | yes | no | make test-hyperliquid-testnet-runtime-hip3 |
| LIGHTER | Spot cash | no | no | no | yes | yes | yes | yes | open orders | unsupported | unsupported | open-order mass status | open order filter | yes | no | make test-lighter-testnet-runtime-spot |
| LIGHTER | Perp | no | no | no | yes | yes | yes | yes | open orders | unsupported | account snapshot | open orders, positions | open order filter | yes | no | make test-lighter-testnet-runtime-perp |

## 声明与方法注意事项

当宽泛声明会掩盖细粒度限制时，矩阵会有意采取保守表述：

- `Latency timestamps = no` 表示没有静态行承诺填充 adapter receive/emit
  timestamp。Runtime 会根据实际可用的 timestamp，另行记录 bus、application、
  callback 和 command timing。
- Hyperliquid Spot 没有实现标准化公共 book、quote 或 trade 订阅。具备认证身份的
  adapter 启动过程可以有条件地接通私有 order/fill 和 spot-state 更新；已分配的
  本地 channel 或已配置的动态标志不能证明连接就绪。
- OKX Spot 的静态 account-stream 声明比具体启动路径更宽：`Adapter.Start` 接通
  私有订单，但不订阅 balance/account。因此 balance 仍来自 REST account-state
  快照。
- Bybit 的具体单订单路径会先查询 realtime record，再查询经过筛选的 history；
  Bitget 则会在提供 instrument 以及 order 或 client 身份时调用精确订单 endpoint。
  它们的静态 single-order label 比这些方法更保守。
- Hyperliquid 为 Spot、Perp 和 HIP-3 实现了有界 `UserFills` 筛选，以及按交易所
  order ID 或映射后的 client identity 进行的精确查询，即使其静态 registry 没有
  声明这些报告细节。Mass-status 覆盖仍有意聚焦开放订单，不承诺权威成交账本。
- Lighter 实现 REST book/bar 查询和 Perp 衍生品参考数据 streaming。其标准化
  book、quote 和 trade 订阅方法仍 unsupported；粗粒度、已配置的 Market 标志不能被
  解释为这些流。
- 当前未平仓量从来不是矩阵中的 stream/cache 列。implemented 时，它是可选的直接
  `OpenInterestClient` 查询，不会存入 runtime cache。
- Strategy `Context` 只暴露 Submit 和按 client-ID 的 Cancel。Adapter/runtime 对
  Modify、CancelAll、report 或 account mutation 的支持，不会把这些命令授予
  strategy。

关于 bars、具体 stream kind、CancelAll、account mutation、订单/TIF 规则、成交
恢复和产品特定 position 范围，请使用对应的交易所页面。关于 funding/reference
data 和只支持查询的 OI，请参阅
[市场与参考数据](guides/market-reference-data.md)。

## 证据边界

默认测试不需要凭证。外部行使用显式 Demo、paper 或 Testnet profile，且必须按
准确的产品范围解释。当前 `Demo/Testnet-certified` 声明需要明确的目标、候选版本、
日期、零跳过，以及产品特定的终态断言。它绝不意味着 mainnet 或未来正确性。

编辑本页后，请运行 schema 和 source-sync contract：

```sh
make test-capabilities
```
