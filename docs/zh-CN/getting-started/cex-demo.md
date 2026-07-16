# Binance Spot Demo Runtime 验收

> 规范语言：英文。[英文规范页](../../getting-started/cex-demo.md)

本页归属规范的 CEX 入门 lifecycle。它通过一个 runtime node 运行真实的 Binance
Spot Demo adapter；这是 acceptance harness，不是可复用的交易应用或 production
认证。

## 准备凭证和 Spot account

在 shell 或仓库忽略的 `.env` 文件中设置以下 Binance Demo 凭证：

| Variable | Requirement |
| --- | --- |
| `BINANCE_DEMO_API_KEY` | 必需的 Demo API key |
| `BINANCE_DEMO_API_SECRET` | 必需的 Demo API secret |

可选控制项如下：

- `BINANCE_DEMO_SYMBOL`，默认值为 `ETH-USDT`；
- `BINANCE_DEMO_MAX_NOTIONAL_USDT`，默认值为 `100`；
- `BINANCE_DEMO_ORDER_QTY`，默认值为 `0`，表示让 harness 根据 venue filter
  和 notional envelope 选择安全数量。

使用专用且有资金的 Demo Spot account。测试前，所选 symbol 不得存在 open
order。account 可以已经持有 base asset，因为 harness 会记录 balance baseline；
但必须有足够的 available quote currency 完成有界 IOC buy，preflight 要求计划
buy notional 之上保留 5% reserve。不要使用 production credential。

## 运行唯一 target

在仓库根目录运行，且不得同时运行其他外部写入 target：

```sh
make test-binance-demo-runtime-spot
```

recipe 仅为此命令设置 `BOLTER_ENABLE_BINANCE_DEMO_WRITES=1`，选择
`TestBinanceSpotDemoRuntimeAcceptance`，拒绝 skipped test，并给 Go test
process 六分钟 timeout。缺少凭证、Demo 环境不可用、preflight 失败、skip、
timeout 或非零退出都不算通过。

## 理解 lifecycle

成功运行时，harness 会依次执行：

1. 解析所选 symbol 和实时 price/size filter，确认该 symbol 没有 open order，
   记录 base 与 quote balance，并检查资金。
2. 构造真实 adapter 和 runtime node，附加 account-required max-notional risk
   envelope，调用 `node.Resync`，精确应用一个 cash `AccountState`，启动 stream，
   并等待 node 进入 active 状态。oversized order 会在本地被拒绝，不会到达 Binance。
3. 在 market 下方提交 post-only `GTX` limit buy，确认其没有成交，通过 runtime
   取消，并观察权威 cancellation 以及 runtime cache 中的 canceled 状态。
4. 提交 marketable `IOC` limit buy，确认产生正数且有界的 fill，通过 runtime
   cache、metrics、portfolio 观察 order 和 fill，并确认权威 base-balance 增加，
   然后再次 reconcile。
5. 只把观察到的 balance increase 向下取整到 venue `SizeStep`；如果仍可交易，
   则发送一个 `IOC` limit sell。close quantity 绝不会超过已确认的 lifecycle
   fill，且结果不明确的 close 不会被重试。
6. 执行最终 reconciliation，并重新检查权威 venue 和 account state。

`BINANCE_DEMO_MAX_NOTIONAL_USDT` 是本地 test/risk envelope，不是 exchange-
capacity model；Binance 对 liquidity 和最终 acceptance 保持权威。

## 解释成功与不明确结果

退出码为零只证明该次运行 candidate 和时间点下选定的 Spot scope：

- Binance 报告所选 symbol 没有 open order；
- runtime 已应用一个 fresh cash-account snapshot；
- 相对于记录的 baseline，available base-asset delta 的绝对值小于一个 venue
  `SizeStep`。

它不证明 whole-account balance equality、derivative position state、mainnet
behavior 或 future availability。

任何非零或结果不明确的退出后，都应把 terminal state 视为未确认。保留 resting、
fill 和 close attempt 的精确 validation client ID，以及所有 venue order ID。
再次写入前，检查这些精确 order、所选 symbol 的全部权威 open order，以及相对
pre-run baseline 的 available base balance。Deferred cleanup 只能取消已识别的
test order，也只能关闭已确认且有界的 validation-created increase。如果 identity
或 fill evidence 尚未解决，不要猜测；再次 sell 可能制造意外 short inventory，
而 account-wide cancel 可能删除无关订单。

## 相关指南

参见 [Binance venue 指南](../venues/binance.md)、
[操作与恢复](../guides/operations-recovery.md)和
[测试参考](../reference/testing.md)。
