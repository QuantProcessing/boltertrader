# 运行 Runtime 节点

[English](../../guides/runtime-node.md) · 本页是英文规范页的中文镜像。

## 归属与范围

本页负责说明 `runtime.TradingNode` 的运行形态：client 装配、初始 reconciliation、
event processing 和 shutdown。adapter 构造与交易所有关，详见[交易所](../venues/README.md)。

## 使用中立契约构建节点

使用 `runtime.Clients{Market, Execution, Account}`、clock 和 client ID prefix 创建
节点。`NewNode` 会创建规范化 cache、account-aware portfolio、event bus、lifecycle
state 和 reconciliation machinery；若提供 execution client，它还会创建 execution
engine。策略通过 runtime option surface 添加，而不是把 adapter 或 SDK reference
传入策略代码。

下面的 constructor 摘自当前的
[`cmd/livedemo/main.go`](../../../cmd/livedemo/main.go)，并非独立程序。省略的代码会
构造 adapter、clock、strategy 和 journal，并处理它们的 setup error。

```go
node := runtime.NewNode(
	runtime.Clients{
		Market:    adapter.Market,
		Execution: adapter.Execution,
		Account:   adapter.Account,
	},
	clk, "livedemo",
	nodeOpts...,
)
```

常用 option 包括 `runtime.WithStrategy`、`runtime.WithBars`、
`runtime.WithJournal`、`runtime.WithAccountID` 和
`runtime.WithAccountStaleAfter`。如果 execution client 和 account client 暴露
`contract.AccountIDProvider`，二者解析出的 ID 必须一致。`WithAccountID` 用于断言
预期 identity，而不是创建第二个 account scope。

节点可以在 client set 不完整时运行，但交易 readiness 取决于实际提供的 execution
和 account capability。由 account 支持的交易部署，应在 execution、account、
portfolio 和 report 中使用唯一的规范 account ID。

## 启动顺序

对于需要断言 preflight baseline 的 account-backed write path，当前 runtime
acceptance case（例如 Binance Spot）采用以下顺序：

1. 构造 adapter 和 node；
2. 调用 `node.Resync(ctx)` 作为 preflight，并检查其权威的 account/open-order
   report；
3. 启动 adapter stream 和所需的 public subscription；
4. 在所属 goroutine 或专用 goroutine 中运行阻塞式 `node.Run(runCtx)`；
5. 在策略回调之外提交订单前，等待 `node.State()` 变为 `running`/`active`；
6. 取消 `runCtx`，等待 `Run` 返回，然后关闭 adapter 持有的 resource。

显式 preflight 很有价值，因为 caller 可以在 stream 或 write 开始前断言 account
mode 和 baseline state。它不能替代启动 reconciliation：`Run` 会重放 open intent，
再次执行 reconciliation，然后才把交易切换为 active 并调用 `OnStart`。若
reconciliation 失败，node 会进入 `failed`/`halted`，而不会静默启用交易。

下面的第二段代码同样不是独立程序，展示了当前
[`cmd/livedemo/main.go`](../../../cmd/livedemo/main.go) 中的执行顺序：

```go
// Reconcile cache from REST before trading.
log.Println("reconciling account state...")
if rep, err := node.Resync(ctx); err != nil {
	log.Printf("resync warning: %v", err)
} else {
	log.Printf("resync: balances=%d positions=%d", rep.BalancesUpdated, rep.PositionsUpdated)
}

// Start the private user-data stream and subscribe to public trades.
if err := adapter.Start(ctx); err != nil {
	log.Fatalf("start user-data stream: %v", err)
}
if err := adapter.Market.SubscribeTrades(ctx, inst); err != nil {
	log.Fatalf("subscribe trades: %v", err)
}

log.Printf("running. symbol=%s qty=%s. Ctrl-C to stop.", symbol, qty)
node.Run(ctx)
```

该 command 会有意决定如何处理自己的 preflight error。交易应用应 fail closed，
除非其自身的书面 policy 能证明 node 可以保持 read-only。`Run` 不返回 error，
因此请检查 `node.State()` 和 `node.Health()` 以识别 failed startup（启动失败）。

## 重连与新鲜度

`node.Reconnect(ctx)` 会要求实现 `contract.Reconnectable` 的 client 重新连接，
随后执行 reconciliation。内部自行重连的 client 会被跳过。stream-gap state 会把
交易切换为 reconciling，从而参与 lifecycle gate。当显式配置 account-backed
generic risk 时，account freshness 也会独立参与该 risk gate。不能仅因 socket
已连接就把 node 视为可写。

检查 `node.State()`、`node.Health()` 和 `node.Metrics()`，以了解 readiness、
account age、open-order count、stream gap、fill 和 reconciliation result。
状态语义详见[状态与对账](../concepts/state-reconciliation.md)。

## 关闭责任

取消传给 `Run` 的 context，并等待其返回。`Run` 会经历 stopping/stopped 状态并
调用策略的 `OnStop`；caller 不应让它与手动 `node.Stop` 产生竞态。runtime loop
has stopped（已停止）后，再关闭 adapter client 和 durable journal store。如果 shutdown 超时，
应将最终交易所状态视为未经确认，并遵循[运维与恢复](operations-recovery.md)。

## 用于生产前的验证

在考虑 mainnet 装配之前，请使用离线示例和 Demo/Testnet acceptance target。
非生产环境的通过结果只对指定 environment、product、candidate 和 date 构成证据；
它并不是生产认证。

- [离线 Runtime 演练](../getting-started/offline-runtime.md)
- [测试参考](../reference/testing.md)
- [配置参考](../reference/configuration.md)
- [运维与恢复](operations-recovery.md)
