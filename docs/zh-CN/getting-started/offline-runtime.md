# 离线 Runtime

> 规范语言：英文。[英文规范页](../../getting-started/offline-runtime.md)

本页归属用于学习和验证 venue-neutral runtime 行为的免凭证路径。它使用可执行
test 和受控 fake client；不声称模拟交易所。

## 运行离线 gate

先运行聚焦 runtime 的 package，再按需扩大范围：

```sh
make test-core
make test
make test-p6-offline
```

- `make test-core` 运行 core、runtime 和 strategy package。
- `make test` 运行仓库的 short、offline-safe suite。
- `make test-p6-offline` 增加 adapter、SDK、capability 和 reference-data
  contract check，且不启用交易所写入。

这些命令不需要 credential 或 write-enable variable。

## 跟随聚焦的 runtime 示例

以下现有 test 是当前行为的精简可执行示例：

```sh
go test ./runtime -run '^(TestRuntimeSpotFlowMirrorsOrdersFillsAndBalances|TestReconnectForcesReconnectAndReconciles|TestStartupPartialReconciliationNeverActivatesTrading)$' -count=1
go test ./runtime/exec -run '^TestSubmit(WithRiskCallsCheckSubmissionDirectlyOnce|WithoutRiskSkipsRiskAndSubmitsOnce|ValidationRejectsBeforeConfiguredRisk)$' -count=1
```

第一条命令展示 Spot order/fill/balance projection、重连后的 reconciliation，
以及 evidence 不完整时 fail-closed activation。第二条锁定通用 submission
contract：无副作用的 `ValidateSubmit`、可选的 venue-neutral risk/reservation
check，以及一次普通 `Submit` 调用。

阅读对应源码：
[`runtime/spot_runtime_test.go`](../../../runtime/spot_runtime_test.go)、
[`runtime/node_test.go`](../../../runtime/node_test.go)、
[`runtime/reconciliation_safety_test.go`](../../../runtime/reconciliation_safety_test.go)
以及
[`runtime/exec/submission_path_test.go`](../../../runtime/exec/submission_path_test.go)。
它们是 test，不是 standalone strategy runner。

## 构建确定性场景

`runtime/runtimetest` 提供可控的 market、execution 和 account client。一个聚焦
runtime 的 test 可以：

1. 使用 `clock.NewSimulatedClock` 让时间确定；
2. 为 fake execution client 和 account client 分配同一个 canonical account ID；
3. 提供权威 `AccountState` snapshot，并用 venue-neutral client 构造
   `runtime.NewNode`；
4. 运行 node，通过其 execution engine 提交，并注入 normalized order、fill、
   balance、position 或 stream-gap evidence；
5. 在不受 network timing 影响的情况下，断言 cache、portfolio、lifecycle、
   reconciliation、metrics 和 strategy callback。

lifecycle wiring 见 [runtime node 组合](../guides/runtime-node.md)，可移植 callback
surface 见[策略编写](../guides/strategies.md)。

## 了解 fake 无法证明什么

fake client 是 contract double，不是 matching engine。它们无法证明 venue
authentication、当前 symbol 和 filter、server-side validation、liquidity、
partial-fill behavior、margin rule、真实 stream ordering、network failure mode、
account permission 或外部 account 上的 cleanup。test author 提供 normalized
event，因此通过的场景证明 runtime 如何处理这些 evidence，而不是 venue 会产生
这些 evidence。

离线 gate 通过后，选择有界的 [Binance Spot Demo](./cex-demo.md) 或
[Hyperliquid Perp Testnet](./dex-testnet.md) 路径。
[测试参考](../reference/testing.md)定义各层级分别支持哪些 claim。
