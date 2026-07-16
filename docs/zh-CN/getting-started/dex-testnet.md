# Hyperliquid Perp Testnet Runtime 验收

> 规范语言：英文。[英文规范页](../../getting-started/dex-testnet.md)

本页归属规范的 DEX 入门 lifecycle。它通过一个 runtime node 运行真实的
Hyperliquid standard-Perp Testnet adapter；这是有界的 acceptance harness，
不是 production trading guide。

## 解析 signer 和 owner identity

将 `HYPERLIQUID_TESTNET_PK` 设置为一个有资金的 Testnet user 或 API-wallet
private key。adapter 会派生 signer address，并在交易前查询 Hyperliquid
`userRole`：

- 直接的 `user` role 使用 signer address；
- `agent` role 解析为 Hyperliquid 返回的 0x owner；
- 如果 signer 是 agent，将 `HYPERLIQUID_ACCOUNT_ADDRESS` 设置为该 owner，
  以显式指定预期 identity；不匹配会在写入前被拒绝；
- 本 account model 拒绝 `vault` 和 `subAccount` user role，本演练不支持它们。

可选控制项为 `HYPERLIQUID_TESTNET_PERP_SYMBOL` 和
`HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`，后者默认值为 `100`。如果未设置
symbol，harness 会选择首个已加载的 standard-Perp instrument。

使用专用 Testnet account。开始时，所选 Perp 必须没有 open order 且 position
为零；order book 必须同时有 bid 和 ask；权威 account snapshot 必须包含足够的
free settlement collateral，以支持有界 opening notional。不要使用 mainnet key
或 production endpoint。

## 运行唯一 target

在仓库根目录运行，且不得同时运行其他外部写入 target：

```sh
make test-hyperliquid-testnet-runtime-perp
```

recipe 仅为此命令设置 `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1`，选择
`TestHyperliquidPerpTestnetRuntimeAcceptance`，拒绝 skipped test，并给 Go
test process 六分钟 timeout。缺少 identity、owner 解析无效、状态不干净、
collateral 不足、order book 为空、skip、timeout 或非零退出都不算通过。

## 理解 lifecycle

成功运行时，harness 会依次执行：

1. 确认 execution client 与 account client 共享同一个 canonical account ID，
   检查所选 instrument 的 open order，加载 two-sided book，在 notional envelope
   内选择 quantity，并检查 free settlement collateral。
2. 用 account-required risk 构造 runtime node，调用 `node.Resync`，精确应用一个
   权威 `AccountState`，验证 freshness、balance 和 portfolio readiness，并确认
   所选 account/runtime position 初始为零。随后启动 stream，并等待 active trading
   state。
3. 提交 post-only `GTX` limit buy，确认其保持 unfilled，通过 runtime 取消，
   并同时等待 runtime-cache cancellation 与权威 venue terminal evidence。
4. 提交 marketable `IOC` limit buy，等待精确的 filled quantity，在权威 account
   position 和 runtime portfolio 中观察 long，并要求匹配的 private order/fill
   stream evidence。
5. 针对观察到的精确 opening quantity 发送一个 `IOC` limit sell，且
   `ReduceOnly=true`。等待权威的所选 Perp position 与 runtime portfolio 变为
   flat；结果不明确的 close 不会被盲目重复。
6. 再次 reconcile，重新应用 fresh account snapshot，并检查最终 venue、cache、
   account-position 和 portfolio state。

max-notional value 是本地 safety envelope，不是 venue-capacity model。
Hyperliquid 对 margin、liquidity 和最终 acceptance 保持权威。

## 解释成功与不明确结果

退出码为零只证明该次运行 candidate 和时间点下已加载的 standard-Perp scope：

- node 已加载的 standard-Perp scope 中没有剩余 open order；
- 所选 Perp 的权威 account position 精确为零；
- runtime cache 中没有非零的所选 Perp position，runtime portfolio 对应的 net
  quantity 精确为零；
- open 和 close 都观察到了 private order 与 fill evidence。

它不证明 Spot、HIP-3、每一种 account product、mainnet behavior 或 future
availability。

任何非零或结果不明确的退出后，即使 deferred cleanup 已运行，terminal state
仍未确认。保留 resting、open 和 close attempt 的精确 validation client ID 与
venue order ID。再次写入前，检查 selected-scope 的权威 open order、所选 Perp
account position，以及 runtime cache/portfolio state。cleanup 只能取消已跟踪的
lifecycle order，也只能减少由已确认 lifecycle fill 所界定的 exposure；close
结果不明确时，不得再次发送可能产生 short position 的 close。绝不要取消或平掉
无关的 account state。

## 相关指南

参见 [Hyperliquid venue 指南](../venues/hyperliquid.md)、
[操作与恢复](../guides/operations-recovery.md)和
[测试参考](../reference/testing.md)。
