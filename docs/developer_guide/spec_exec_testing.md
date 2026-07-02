# Execution Acceptance Testing Spec

BolterTrader follows the NautilusTrader-style testing split:

1. Unit and package tests prove local deterministic transitions.
2. Adapter contract tests prove venue-neutral behavior without live credentials.
3. Demo/Testnet spec acceptance proves a real venue contract.

Default tests must be deterministic and credential-free. Demo/Testnet acceptance
is explicitly invoked, bounded by a notional envelope, and responsible for
cleaning up any exchange state it creates.

## Baseline Runtime Smoke

The baseline live execution acceptance run must:

1. Build the real adapter against the Demo endpoint.
2. Build `runtime.TradingNode` with market, execution, and account clients.
3. Call `node.Resync` before trading.
4. Run a test strategy through `runtime/strategy.Context`.
5. Submit one resting post-only order and cancel it.
6. Submit one market order and observe a fill through the runtime.
7. Observe account position through the runtime.
8. Close the position reduce-only.
9. Reconcile and assert the runtime cache is flat with no open orders.

Pass criteria:

- The command exits 0.
- No open orders remain.
- No position remains.
- Runtime metrics show at least one order and one fill.
- Cleanup errors include venue order IDs, client order IDs, symbol, side, size,
  and remaining exposure.

## Environment Rules

Use Demo/Testnet credentials created for the selected exchange environment.
Production credentials must not be accepted as fallback credentials for Demo
acceptance tests.

For Binance Demo:

- `BINANCE_DEMO_API_KEY`
- `BINANCE_DEMO_API_SECRET`
- optional `BINANCE_DEMO_SYMBOL`, default `ETH-USDT`
- optional `BINANCE_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `BINANCE_DEMO_ORDER_QTY`, default automatic safe quantity

For OKX Demo:

- `OKX_DEMO_API_KEY`
- `OKX_DEMO_API_SECRET`
- `OKX_DEMO_API_PASSPHRASE`
- optional `OKX_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `OKX_DEMO_SPOT_SYMBOL`, default `ETH-USDT`
- optional `OKX_DEMO_PERP_SYMBOL`, default `ETH-USDT-SWAP`
- optional `OKX_DEMO_HOST_PROFILE`, default `global`; use `eea` for OKX's EEA
  Demo hosts, or `custom` with explicit REST/WS overrides

Proxy configuration is command-local. The SDK reads `PROXY`; inherited shell
variables such as `ALL_PROXY` are not part of the test contract.

## Command Ladder

```sh
make test-core
make test-adapter
make test-binance-demo-perp
make test-binance-demo-runtime-perp
make test-binance-demo-spot-data
make test-binance-demo-spot
make test-binance-demo-acceptance
make test-okx-demo-spot
make test-okx-demo-runtime-spot
make test-okx-demo-perp
make test-okx-demo-runtime-perp
make test-okx-demo-acceptance
```

`make test-binance-demo-spot-data` is read-only and enables
`BOLTER_ENABLE_LIVE_READ_TESTS=1` for the Spot Demo data smoke. Spot and perp
write tests use `BINANCE_DEMO_API_KEY` and `BINANCE_DEMO_API_SECRET`; they are
not called by `make test`.

`make test-okx-demo-spot` and `make test-okx-demo-perp` are adapter-level OKX
Demo write gates. `make test-okx-demo-runtime-spot` and
`make test-okx-demo-runtime-perp` run the same product flows through
`runtime.TradingNode`. They are not called by `make test`.

## Risk And Reconciliation

Runtime Demo acceptance should keep venue notional small and start from a flat
derivatives account. Spot Demo acceptance keeps notional small, preflights quote
cash, and cleans up by selling the test base-asset delta below one size step. It
may bypass strategy alpha/risk logic when the acceptance goal is venue/runtime
plumbing, but runtime-node acceptance must keep reconciliation enabled through
`node.Resync` before and after the live write flow.

Risk engine behavior remains covered by deterministic runtime tests. Add a
separate Demo risk-gate acceptance only when the desired assertion requires live
instrument metadata or live venue rejects.
