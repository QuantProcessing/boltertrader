# Running a Runtime Node

[Chinese mirror](../zh-CN/guides/runtime-node.md) · This English page is canonical.

## Ownership and scope

This page owns the operational shape of `runtime.TradingNode`: client wiring,
initial reconciliation, event processing, and shutdown. Adapter construction is
venue-specific and is documented under [Venues](../venues/README.md).

## Build the node from neutral contracts

Create a node with `runtime.Clients{Market, Execution, Account}`, a clock, and a
client-ID prefix. `NewNode` creates the normalized cache, account-aware portfolio,
event bus, lifecycle state, and reconciliation machinery; it creates an execution
engine when an execution client is supplied. A strategy is added through the
runtime option surface, not by passing adapter or SDK references into strategy
code.

The following constructor excerpt, not a standalone program, is from the current
[`cmd/livedemo/main.go`](../../cmd/livedemo/main.go). The omitted code constructs
the adapter, clock, strategy, and journal and handles their setup errors.

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

Common options include `runtime.WithStrategy`, `runtime.WithBars`,
`runtime.WithJournal`, `runtime.WithAccountID`, and
`runtime.WithAccountStaleAfter`. If execution and account clients expose
`contract.AccountIDProvider`, their resolved IDs must agree. `WithAccountID`
asserts the expected identity rather than creating a second account scope.

The node can operate with partial client sets, but trading readiness depends on
the execution and account capabilities actually supplied. An account-backed
trading deployment should use one canonical account ID across execution, account,
portfolio, and reports.

## Startup sequence

For an account-backed write path that asserts a preflight baseline, current
runtime acceptance cases such as Binance Spot use this order:

1. construct the adapter and node;
2. call `node.Resync(ctx)` as a preflight and inspect its authoritative
   account/open-order report;
3. start the adapter streams and requested public subscriptions;
4. run the blocking `node.Run(runCtx)` in the owning goroutine or a dedicated
   goroutine;
5. wait until `node.State()` is `running`/`active` before submitting outside a
   strategy callback;
6. cancel `runCtx`, wait for `Run` to return, and then close adapter-owned
   resources.

The explicit preflight is useful because callers can assert account mode and
baseline state before streams or writes. It is not a substitute for startup
reconciliation: `Run` replays open intents and performs reconciliation again
before it transitions trading to active and invokes `OnStart`. A reconciliation
error moves the node to `failed`/`halted` rather than silently enabling trading.

This second excerpt, also not standalone, is the current ordering in
[`cmd/livedemo/main.go`](../../cmd/livedemo/main.go):

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

That command deliberately chooses how to treat its preflight error. A trading
application should fail closed unless its own documented policy proves the node
can remain read-only. `Run` returns no error, so inspect `node.State()` and
`node.Health()` for a failed startup.

## Reconnect and freshness

`node.Reconnect(ctx)` asks clients implementing `contract.Reconnectable` to
reconnect and then reconciles. Clients that reconnect internally are skipped.
Stream-gap state participates in the lifecycle gate by moving trading to
reconciling. When account-backed generic risk is explicitly configured, account
freshness independently participates in that risk gate. A node must not be
treated as writable merely because a socket is connected.

Inspect `node.State()`, `node.Health()`, and `node.Metrics()` for readiness,
account age, open-order counts, stream gaps, fills, and reconciliation results.
For detailed state semantics, see
[State and Reconciliation](../concepts/state-reconciliation.md).

## Shutdown ownership

Cancel the context passed to `Run` and wait for it to return. `Run` transitions
through stopping/stopped and calls the strategy's `OnStop`; callers should not
race it with a manual `node.Stop`. Close adapter clients and durable journal
stores after the runtime loop has stopped. If shutdown times out, treat final
venue state as unconfirmed and follow [Operations and Recovery](operations-recovery.md).

## Verification before production use

Use the offline examples and Demo/Testnet acceptance targets before considering
mainnet wiring. A non-production pass is evidence for the named environment,
product, candidate, and date only; it is not production certification.

- [Offline runtime walkthrough](../getting-started/offline-runtime.md)
- [Testing reference](../reference/testing.md)
- [Configuration reference](../reference/configuration.md)
- [Operations and recovery](operations-recovery.md)
