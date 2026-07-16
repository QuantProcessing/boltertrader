# Offline Runtime

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/offline-runtime.md)

This page owns the credential-free path for learning and verifying venue-neutral
runtime behavior. It uses executable tests and controlled fake clients; it does
not claim to emulate an exchange.

## Run the offline gates

Start with the runtime-focused packages, then expand the scope when needed:

```sh
make test-core
make test
make test-p6-offline
```

- `make test-core` runs core, runtime, and strategy packages.
- `make test` runs the repository's short, offline-safe suite.
- `make test-p6-offline` adds adapter, SDK, capability, and reference-data
  contract checks without enabling exchange writes.

No credential or write-enable variable is required for these commands.

## Follow focused runtime examples

These existing tests are small executable examples of current behavior:

```sh
go test ./runtime -run '^(TestRuntimeSpotFlowMirrorsOrdersFillsAndBalances|TestReconnectForcesReconnectAndReconciles|TestStartupPartialReconciliationNeverActivatesTrading)$' -count=1
go test ./runtime/exec -run '^TestSubmit(WithRiskCallsCheckSubmissionDirectlyOnce|WithoutRiskSkipsRiskAndSubmitsOnce|ValidationRejectsBeforeConfiguredRisk)$' -count=1
```

The first command shows Spot order/fill/balance projection, reconnect followed by
reconciliation, and fail-closed activation on incomplete evidence. The second
locks the generic submission contract: side-effect-free `ValidateSubmit`, an
optional venue-neutral risk/reservation check, and one ordinary `Submit` call.

Read the corresponding sources in
[`runtime/spot_runtime_test.go`](../../runtime/spot_runtime_test.go),
[`runtime/node_test.go`](../../runtime/node_test.go),
[`runtime/reconciliation_safety_test.go`](../../runtime/reconciliation_safety_test.go),
and
[`runtime/exec/submission_path_test.go`](../../runtime/exec/submission_path_test.go).
They are tests, not a standalone strategy runner.

## Build deterministic scenarios

`runtime/runtimetest` provides controllable market, execution, and account
clients. A focused runtime test can:

1. use `clock.NewSimulatedClock` to make time deterministic;
2. assign one canonical account ID to fake execution and account clients;
3. provide an authoritative `AccountState` snapshot and construct
   `runtime.NewNode` from venue-neutral clients;
4. run the node, submit through its execution engine, and inject normalized
   order, fill, balance, position, or stream-gap evidence;
5. assert cache, portfolio, lifecycle, reconciliation, metrics, and strategy
   callbacks without network timing.

Use [runtime node composition](../guides/runtime-node.md) for lifecycle wiring and
[strategy authoring](../guides/strategies.md) for the portable callback surface.

## Know what fakes cannot prove

The fake clients are contract doubles, not a matching engine. They do not prove
venue authentication, current symbols and filters, server-side validation,
liquidity, partial-fill behavior, margin rules, real stream ordering, network
failure modes, account permissions, or cleanup on an external account. The test
author supplies the normalized events, so a passing scenario proves how runtime
handles that evidence—not that a venue will produce it.

After offline gates pass, choose the bounded [Binance Spot Demo](./cex-demo.md)
or [Hyperliquid Perp Testnet](./dex-testnet.md) path. The
[testing reference](../reference/testing.md) defines which claims belong to each
tier.
