# Execution Acceptance Testing Spec

BolterTrader follows the NautilusTrader-style testing split:

1. Unit and package tests prove local deterministic transitions.
2. Adapter contract tests prove venue-neutral behavior without live credentials.
3. Demo/Testnet spec acceptance proves a real venue contract.

Default tests must be deterministic and credential-free. Demo/Testnet acceptance
is explicitly invoked, bounded by a notional envelope, and responsible for
cleaning up any exchange state it creates. CEX first-stage acceptance uses Demo
or paper-trading environments; DEX first-stage acceptance uses Testnet.

## Baseline Runtime Smoke

The baseline live execution acceptance run must:

1. Build the real adapter against the selected Demo/Testnet endpoint.
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
- `node.Health()` is `running` before writes and exposes clients, streams,
  in-flight count, latency drops, observer drops, and last reconciliation error.
- Runtime metrics include command and reconciliation latency samples.
- Cleanup errors include venue order IDs, client order IDs, symbol, side, size,
  and remaining exposure.

## Environment Rules

Use Demo/Testnet credentials created for the selected exchange environment.
Production credentials must not be accepted as fallback credentials for
Demo/Testnet acceptance tests.

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

For Bybit Demo:

- `BYBIT_DEMO_API_KEY`
- `BYBIT_DEMO_API_SECRET`
- optional `BYBIT_DEMO_SYMBOL`, default `BTCUSDT`
- optional `BYBIT_DEMO_USDT_PERP_SYMBOL`, default `BTCUSDT`
- optional `BYBIT_DEMO_USDC_PERP_SYMBOL`, default `BTCPERP`
- optional `BYBIT_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `BYBIT_DEMO_MAX_NOTIONAL_USDC`, default `100`

The Bybit Demo key must be created from Bybit Demo Trading. Bybit Testnet keys
are a separate credential scope and are rejected by the Demo Trading REST host.

For Bitget Demo:

- `BITGET_DEMO_API_KEY`
- `BITGET_DEMO_SECRET_KEY`
- `BITGET_DEMO_PASSPHRASE`
- optional `BITGET_DEMO_REST_BASE_URL`, default `https://api.bitget.com`
- optional `BITGET_DEMO_PUBLIC_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/public`
- optional `BITGET_DEMO_PRIVATE_WS_URL`, default
  `wss://wspap.bitget.com/v3/ws/private`
- optional `BITGET_DEMO_SYMBOL`, default `BTCUSDT`
- optional `BITGET_DEMO_USDT_PERP_SYMBOL`, default `BTCUSDT`
- optional `BITGET_DEMO_USDC_PERP_SYMBOL`, default `BTCPERP`
- optional `BITGET_DEMO_MAX_NOTIONAL_USDT`, default `100`
- optional `BITGET_DEMO_MAX_NOTIONAL_USDC`, default `100`

`BITGET_TESTNET_*` variables are accepted only as legacy local aliases; new
configuration should use `BITGET_DEMO_*`.

For Hyperliquid Testnet:

- `HYPERLIQUID_TESTNET_PK`
- `BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1` for direct write/runtime `go test`
  runs; Makefile write/runtime targets set it command-locally
- optional `HYPERLIQUID_ACCOUNT_ADDRESS`, when trading from an address different
  from the private-key address
- optional `HYPERLIQUID_TESTNET_VAULT`
- optional `HYPERLIQUID_TESTNET_MAX_NOTIONAL_USDC`, default `100`
- optional `HYPERLIQUID_TESTNET_SPOT_SYMBOL`
- optional `HYPERLIQUID_TESTNET_PERP_SYMBOL`
- optional `HYPERLIQUID_TESTNET_HIP3_SYMBOL` in explicit dex-qualified
  `dex:coin` or `dex:coin-USDC` form

Proxy configuration is command-local. The SDK reads `PROXY`; inherited shell
variables such as `ALL_PROXY` are not part of the test contract.

## Command Ladder

```sh
make test-core
make test-adapter
make test-capabilities
make test-p6-offline
make test-binance-demo-perp
make test-binance-demo-runtime-perp
make test-binance-demo-spot-data
make test-binance-demo-spot
make test-binance-demo-runtime-spot
make test-binance-demo-acceptance
make test-okx-demo-spot
make test-okx-demo-runtime-spot
make test-okx-demo-perp
make test-okx-demo-runtime-perp
make test-okx-demo-acceptance
make test-bybit-demo-spot
make test-bybit-demo-runtime-spot
make test-bybit-demo-usdt-perp
make test-bybit-demo-runtime-usdt-perp
make test-bybit-demo-usdc-perp
make test-bybit-demo-runtime-usdc-perp
make test-bybit-acceptance
make test-bitget-demo-spot
make test-bitget-demo-runtime-spot
make test-bitget-demo-usdt-perp
make test-bitget-demo-runtime-usdt-perp
make test-bitget-demo-usdc-perp
make test-bitget-demo-runtime-usdc-perp
make test-bitget-acceptance
make test-bybit-bitget-acceptance
make test-hyperliquid-testnet-spot-read
make test-hyperliquid-testnet-spot
make test-hyperliquid-testnet-runtime-spot
make test-hyperliquid-testnet-perp-read
make test-hyperliquid-testnet-perp
make test-hyperliquid-testnet-runtime-perp
make test-hyperliquid-testnet-hip3
make test-hyperliquid-testnet-runtime-hip3
make test-hyperliquid-testnet-acceptance
make test-lighter-testnet-read
make test-lighter-testnet-spot
make test-lighter-testnet-runtime-spot
make test-lighter-testnet-perp
make test-lighter-testnet-runtime-perp
make test-lighter-testnet-acceptance
```

`make test-binance-demo-spot-data` is read-only and enables
`BOLTER_ENABLE_LIVE_READ_TESTS=1` for the Spot Demo data smoke. Spot and perp
write tests use `BINANCE_DEMO_API_KEY` and `BINANCE_DEMO_API_SECRET`; they are
not called by `make test`. `make test-binance-demo-runtime-spot` runs the Spot
cash write/cleanup path through `runtime.TradingNode`.

`make test-okx-demo-spot` and `make test-okx-demo-perp` are adapter-level OKX
Demo write gates. `make test-okx-demo-runtime-spot` and
`make test-okx-demo-runtime-perp` run the same product flows through
`runtime.TradingNode`. They are not called by `make test`.

`make test-bybit-acceptance` and `make test-bitget-acceptance` are product-level
non-production gates for Spot cash, USDT-linear Perp, and USDC-linear Perp:
Bybit uses Demo Trading, while Bitget uses Demo/paper trading.
Each venue also exposes product aggregates such as
`make test-bybit-spot-acceptance` and `make test-bitget-usdt-perp-acceptance`.
These targets use `noskipgotest`, so missing credentials, unsupported account
mode, dirty account state, insufficient test balance, or invalid endpoint
profiles are reported as incomplete acceptance instead of a green skip. The current
entrypoints verify live market data, authoritative account-state snapshots,
runtime reconciliation into cache/portfolio, risk fail-closed behavior, and
private stream startup, then execute a bounded resting-cancel plus IOC
fill/close cleanup ladder. Final execution acceptance still requires these
noskip targets to pass against the configured real Demo/Testnet accounts.

`make test-hyperliquid-testnet-spot-read` and
`make test-hyperliquid-testnet-perp-read` are read-only and enable
`BOLTER_ENABLE_LIVE_READ_TESTS=1`. Hyperliquid write/runtime targets require
`HYPERLIQUID_TESTNET_PK`; their Makefile targets set
`BOLTER_ENABLE_HYPERLIQUID_TESTNET_WRITES=1` command-locally. HIP-3 targets
additionally require `HYPERLIQUID_TESTNET_HIP3_SYMBOL` for the configured
testnet `dex:coin` or `dex:coin-USDC`. UI display symbols such as `TSLA-USDC`
can map to multiple HIP-3 dexes and are intentionally not accepted without a
dex qualifier. They are not called by `make test`. The Hyperliquid Make targets
fail when any selected acceptance test skips, so operator-cleanup or funding
preflight failures cannot masquerade as a completed acceptance run.

`make test-lighter-testnet-acceptance` is the Lighter unified-account Testnet
gate for Spot and Perp. It requires Testnet account/api-key indexes and uses
noskip gating like Hyperliquid, Bybit, and Bitget.

## Risk And Reconciliation

Runtime non-production acceptance should keep venue notional small and start from
a flat derivatives account. Spot acceptance keeps notional small, preflights
quote cash, and cleans up by selling the test base-asset delta below one size
step. It may bypass strategy alpha/risk logic when the acceptance goal is
venue/runtime plumbing, but runtime-node acceptance must keep reconciliation
enabled through `node.Resync` before and after the live write flow.

Hyperliquid runtime Testnet acceptance follows the same runtime-node shape but
uses conservative resting orders plus explicit cancel rather than requiring a
fill. It attaches the risk engine, proves an oversized order is rejected before
the venue boundary, observes cancel state through the runtime cache, checks REST
open orders after cancel, and requires the final cache/portfolio to be flat.
Perp and HIP-3 runs must start from a flat derivatives account; otherwise the
test skips and asks the operator to clean the testnet account first. The Make
acceptance gate treats that skip as failed evidence until the account is clean
and the place/cancel path actually runs.

Bybit and Bitget runtime acceptance use the same NT-style safety envelope for
their first unified-account slice. The node must reconcile one authoritative
account state before private stream startup, portfolio and risk must read by
canonical account id, unsupported account modes must fail closed, and the order
lifecycle must pass through runtime execution with a resting cancel, IOC fill,
IOC close, final open-order check, and final reconciliation. These targets are
not accepted when they skip or cannot reach the live venue.

Risk engine behavior remains covered by deterministic runtime tests. Add a
separate non-production risk-gate acceptance only when the desired assertion
requires live instrument metadata or live venue rejects.
