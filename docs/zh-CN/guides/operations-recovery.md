# 运维与恢复

[English](../../guides/operations-recovery.md) · 本页是英文规范页的中文镜像。

## 归属与范围

本页负责说明启动、重连、订单结果不明确以及有界非生产 cleanup 时的 operator
action。它不能替代交易所 runbook，也不会根据 Demo/Testnet 证据声称已具备生产
readiness。

## 从权威状态开始

启用 write 前使用 `node.Resync(ctx)`。确认 reconciliation report、account
freshness、product/account mode，并确保不存在不安全的预存状态。stream gap 或
reconnect 后，使用 `node.Reconnect(ctx)` 或显式 resync；不要仅靠 callback 重建
order 或 position。

`Reconnect` 会调用实现 `contract.Reconnectable` 的 client，随后执行
reconciliation。`Resync` 会与 event application 串行化。这两种方法都不会把
缺失的 fill history 变成“订单从未成交”的证据；当 recovery 不充分时，lifecycle
可以保持 reconciling 或 halted。

## 将歧义视为状态，而不是重试信号

提交后的 timeout 或 transport error 可能意味着交易所已经接受订单。保留原始
client ID 以及所有 venue order ID，再使用该产品支持的精确 status、open-order、
fill 和 position 证据。第一笔订单的结果得到解析之前，不要使用新的 identity
提交替代订单。

execution journal 和 reconciliation path 会区分 intent、confirmed result 和
unresolved outcome。failed command（失败的命令）并不能证明没有 write 到达交易所。

| 观测结果 | Operator 操作 |
| --- | --- |
| 交接给交易所前，本地 validation、lifecycle 或 risk 拒绝 | 修复 request 或 readiness condition；不要为该次被拒绝的尝试执行交易所 cleanup |
| 已确认订单 | 保留 client ID 和 venue ID；遵循常规 status/fill handling |
| 可能已交接给交易所，但没有权威结果 | 冻结替代性 write，使用准确的已知 identity 查询，执行 reconciliation；若证据仍不完整，则将结果保留为 ambiguous |
| 权威的 terminal result | 只执行该 terminal order/fill/account 证据能够证明合理的 cleanup |

不在完整 open-order list 中，只能证明订单当前不再 open。它不能虚构 terminal
cause、证明 fill 为零或授权替代订单。若产品实现了 exact status report 和 exact
fill report，则使用它们；否则保持结果 unresolved。

## Cleanup 必须有界

acceptance harness 只会取消它能够识别且由 validation 创建的订单，也只会
flatten 它能够归因并约束的 exposure。zero exit 时，应用测试针对准确产品定义的
success。任何 nonzero 或 ambiguous exit 都意味着最终状态未经确认，即使 deferred
cleanup 已执行。

- Binance Spot：检查准确的 validation ID、所选 symbol 的 open order，以及权威
  base-asset balance 相对于已捕获 baseline 的变化。success 允许小于一个 size step
  的 dust；它不能证明整个 account 完全相等。
- Hyperliquid Perp：检查准确的 validation ID、所选 scope 的 open order，以及所选
  Perp 的 account/runtime position。success 不会认证 Spot、HIP-3 或每一种 account
  product。

绝不要盲目取消无关订单，也不要 flatten 未经证明的 position。如果 close outcome
存在歧义，额外 sell 可能创建 short position。

下面的代码摘自当前的 bounded cleanup implementation
[`adapter/internal/runtimeaccept/order_lifecycle.go`](../../../adapter/internal/runtimeaccept/order_lifecycle.go)，
并非独立程序。它展示了 ambiguous close 之后 fail-closed 的 Perp 规则：稳定的
nonzero position 证据会阻止再次发送 flattening order。

```go
if !allowFlatten {
	reports, err := waitForStableCleanupPosition(cleanupCtx, exec, spec)
	if err != nil {
		return errors.Join(cancelErr, err)
	}
	if len(reports) != 0 {
		return errors.Join(cancelErr, fmt.Errorf("position cleanup blocked: close outcome ambiguous; refusing an additional sell with %d non-zero position report(s)", len(reports)))
	}
	return cancelErr
}
```

对于 Spot，cleanup 受以下条件约束：已捕获的权威 base-balance baseline、观测到的
lifecycle-created fill quantity、fee reserve、size step 和 minimum notional。
它绝不会出售根据 account-wide balance 推定归属的资产。对于 Perp，cleanup 仅限
所选 instrument/account scope、观测到的 lifecycle exposure、显式 quantity cap 和
reduce-only semantics。若 close outcome 存在歧义，就会禁止再次自动尝试 flatten。

## 证据与升级

记录 candidate、command、environment/product、client ID 和 venue ID、terminal
report 以及 reconciliation result，但不要记录 secret。raw execution evidence
应保持私密；公开 certification summary 只包含 scoped、dated、zero-skip result。

- [CEX Demo 演练](../getting-started/cex-demo.md)
- [DEX Testnet 演练](../getting-started/dex-testnet.md)
- [测试参考](../reference/testing.md)
- [状态与对账](../concepts/state-reconciliation.md)
