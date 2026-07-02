# NT-Style Testing Alignment Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Bring BolterTrader's test and acceptance workflow into the same shape as NautilusTrader: layered local tests, adapter contract tests, and explicit Demo/Testnet spec-acceptance runs through the runtime node.

**Architecture:** Keep fast deterministic tests in `runtime/`, `core/`, `sdk/`, and `adapter/` packages. Put live Demo acceptance tests beside the adapter they exercise, but force them through `runtime.NewNode` and a small ExecTester-style strategy so real venue events traverse bus, cache, portfolio, reconciliation, and cleanup logic.

**Tech Stack:** Go `testing`, Makefile targets, Binance Demo REST/WebSocket, existing `runtime.TradingNode`, existing `adapter/binance/perp` contract clients, `shopspring/decimal`.

---

## File Structure

- Modify `Makefile`: expose separate fast, adapter-live, runtime-live, and full Demo acceptance targets.
- Modify `docs/testing-strategy.md`: document the NT-style testing ladder and the exact commands/gates.
- Create `docs/developer_guide/spec_exec_testing.md`: repo-local execution acceptance spec modeled on NT's ExecTester spec, with safety envelope and pass/fail criteria.
- Create `adapter/binance/perp/demo_runtime_acceptance_test.go`: Binance USD-M Demo runtime acceptance test that uses `runtime.NewNode`.
- Create `adapter/binance/perp/demo_runtime_tester_test.go`: test-only ExecTester-style strategy and observer helpers.
- Modify `adapter/binance/perp/demo_acceptance_helpers_test.go`: add unit coverage for strategy ID sizing and runtime acceptance helpers.

## Task 1: Document the NT-Style Testing Contract

**Files:**
- Create: `docs/developer_guide/spec_exec_testing.md`
- Modify: `docs/testing-strategy.md`
- Modify: `Makefile`

- [x] **Step 1: Add the execution spec document**

Create `docs/developer_guide/spec_exec_testing.md` with:

```markdown
# Execution Acceptance Testing Spec

BolterTrader follows the NautilusTrader testing split:

1. Unit and package tests prove local deterministic transitions.
2. Adapter contract tests prove venue-neutral behavior without live credentials.
3. Demo/Testnet spec acceptance proves a real venue contract.

For live execution acceptance, use Demo/Testnet credentials, bounded notional,
flat-account preflight, reconciliation enabled, and automatic cleanup.

## Baseline Runtime Smoke

The baseline acceptance run must:

1. Build the real adapter against the Demo endpoint.
2. Build `runtime.TradingNode` with market, execution, and account clients.
3. Call `node.Resync` before trading.
4. Run a test strategy through `runtime.strategy.Context`.
5. Submit one resting post-only order and cancel it.
6. Submit one market order and observe a fill through the runtime.
7. Observe account position through the runtime.
8. Close the position reduce-only.
9. Reconcile and assert the runtime cache is flat with no open orders.

Pass criteria: the command exits 0, no open orders remain, no position remains,
and runtime metrics show at least one order and one fill.
```

- [x] **Step 2: Update the testing strategy doc**

Add a section mapping BolterTrader commands to the ladder:

```markdown
## NT-Style Acceptance Ladder

- `make test-core`: local runtime/core behavior.
- `make test-adapter`: venue-neutral adapter contracts.
- `make test-binance-demo-perp`: adapter-level Binance Demo execution.
- `make test-binance-demo-runtime-perp`: runtime-level Binance Demo execution.
- `make test-binance-demo-acceptance`: the complete Binance Demo acceptance gate.
```

- [x] **Step 3: Add Make targets**

Add:

```make
.PHONY: test-binance-demo-runtime-perp test-binance-demo-acceptance

test-binance-demo-runtime-perp:
	go test -run TestBinanceDemoRuntimeAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m

test-binance-demo-acceptance: test-binance-demo-perp test-binance-demo-runtime-perp
```

## Task 2: Add Runtime Demo acceptance RED Test

**Files:**
- Create: `adapter/binance/perp/demo_runtime_acceptance_test.go`
- Create: `adapter/binance/perp/demo_runtime_tester_test.go`

- [x] **Step 1: Write the failing runtime acceptance test**

Add `TestBinanceDemoRuntimeAcceptance` that imports `github.com/QuantProcessing/boltertrader/runtime`, constructs a Binance Demo adapter, creates a `runtime.TradingNode`, calls `node.Resync(ctx)`, starts the adapter, runs `node.Run(ctx)` in a goroutine, and uses a test strategy that submits through `strategy.Context`.

Expected RED: compile fails because `newDemoRuntimeExecTester` and related helpers are not yet defined.

- [x] **Step 2: Run RED**

Run:

```bash
go test ./adapter/binance/perp -run TestBinanceDemoRuntimeAcceptance -count=1
```

Expected: FAIL with undefined helper names.

## Task 3: Implement Runtime ExecTester Helper

**Files:**
- Create: `adapter/binance/perp/demo_runtime_tester_test.go`
- Modify: `adapter/binance/perp/demo_runtime_acceptance_test.go`

- [x] **Step 1: Implement test strategy**

Implement a test-only strategy which:

- Embeds `strategy.Base`.
- OnStart submits a market BUY using `c.Orders.Submit`.
- Records the client ID and venue order ID from the submit response.
- OnFill records fills and signals a channel.
- OnStop cancels any remaining runtime-cache open orders for the instrument.

- [x] **Step 2: Implement runtime wait helpers**

Add polling helpers that assert:

- Runtime cache observes an order.
- Runtime metrics show at least one fill.
- Runtime cache observes a position after the fill.
- `node.Resync` clears flat positions after cleanup.

- [x] **Step 3: Run GREEN**

Run:

```bash
PROXY=http://127.0.0.1:10900 go test -run TestBinanceDemoRuntimeAcceptance ./adapter/binance/perp/ -count=1 -timeout=3m -v
```

Expected: PASS and account ends flat.

## Task 4: Verify Full Ladder

**Files:**
- None beyond prior tasks.

- [x] **Step 1: Run fast runtime checks**

```bash
go test ./runtime/... -count=1
go test -race ./runtime/... -count=1
```

- [x] **Step 2: Run Binance Demo acceptance**

```bash
PROXY=http://127.0.0.1:10900 make test-binance-demo-acceptance
```

- [x] **Step 3: Confirm venue cleanup**

Use signed Demo REST preflight to assert `ETHUSDT` open orders are `[]` and position amount is `0.000`.

## Self-Review

- Spec coverage: includes NT-style unit/integration/spec acceptance split, runtime node Demo acceptance, reconciliation, cache, bus, and cleanup.
- Placeholder scan: no TBD/TODO placeholders.
- Type consistency: plan uses existing `runtime.TradingNode`, `strategy.Context`, and adapter Demo helpers.
