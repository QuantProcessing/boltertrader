# Testing and Evidence

[简体中文](../zh-CN/reference/testing.md)

> English is canonical. **Owner:** this page owns the repository test ladder,
> the complete nine-venue command inventory, and the rules for interpreting
> non-production evidence. Venue pages own prerequisites and product caveats.

## Offline-first test ladder

The default path is credential-free and never opts into external reads or
writes.

| Purpose | Command |
| --- | --- |
| Default short suite | `make test` |
| Core, runtime, and strategy | `make test-core` |
| Adapter packages | `make test-adapter` |
| SDK packages | `make test-sdk` |
| Capability contract | `make test-capabilities` |
| Full offline gate | `make test-p6-offline` |
| Runtime race checks | `make test-race` |
| Offline reference-data checks | `make test-reference-data-offline` |

Run the smallest relevant package or target while developing, then the broader
offline gates appropriate to the change. `go test ./...` is not the default safe
gate; live-backed tests may inspect the local environment when short mode is not
enabled.

## Nine-venue external command inventory

Every command in this table targets a named Demo/Testnet environment. The
reference-data column is read-only. Venue aggregates and runtime targets can
place, cancel, or otherwise mutate real non-production exchange state.

| Environment | Venue aggregate | Reference-data read | Runtime write target(s) |
| --- | --- | --- | --- |
| Aster V3 Testnet | `make test-aster-testnet-acceptance` | `make test-aster-testnet-reference-data-read` | `make test-aster-testnet-runtime-spot`; `make test-aster-testnet-runtime-perp` |
| Nado Testnet | `make test-nado-testnet-acceptance` | `make test-nado-testnet-reference-data-read` | `make test-nado-testnet-runtime-spot`; `make test-nado-testnet-runtime-perp` |
| Binance Demo | `make test-binance-demo-acceptance` | `make test-binance-demo-reference-data-read` | `make test-binance-demo-runtime-spot`; `make test-binance-demo-runtime-perp` |
| OKX Demo | `make test-okx-demo-acceptance` | `make test-okx-demo-reference-data-read` | `make test-okx-demo-runtime-spot`; `make test-okx-demo-runtime-perp` |
| Bybit Demo Trading | `make test-bybit-acceptance` | `make test-bybit-demo-reference-data-read` | `make test-bybit-demo-runtime-spot`; `make test-bybit-demo-runtime-usdt-perp`; `make test-bybit-demo-runtime-usdc-perp` |
| Bitget Demo/PAP | `make test-bitget-acceptance` | `make test-bitget-demo-reference-data-read` | `make test-bitget-demo-runtime-spot`; `make test-bitget-demo-runtime-usdt-perp`; `make test-bitget-demo-runtime-usdc-perp` |
| Gate Testnet | `make test-gate-testnet-acceptance` | `make test-gate-testnet-reference-data-read` | `make test-gate-testnet-runtime-spot`; `make test-gate-testnet-runtime-usdt-perp` |
| Hyperliquid Testnet | `make test-hyperliquid-testnet-acceptance` | `make test-hyperliquid-testnet-reference-data-read` | `make test-hyperliquid-testnet-runtime-spot`; `make test-hyperliquid-testnet-runtime-perp`; `make test-hyperliquid-testnet-runtime-hip3` |
| Lighter Testnet | `make test-lighter-testnet-acceptance` | `make test-lighter-testnet-reference-data-read` | `make test-lighter-testnet-runtime-spot`; `make test-lighter-testnet-runtime-perp` |

`make test-reference-data-read` is the only all-nine aggregate: it runs the nine
reference-data targets above. There is no all-nine write-acceptance aggregate.

## Aggregate asymmetries

Do not infer membership from an aggregate's name:

- `make test-demo-acceptance` covers Binance, OKX, Bybit, and Bitget only.
- `make test-aster-nado-testnet-acceptance` pairs Aster and Nado;
  `make test-bybit-bitget-acceptance` pairs Bybit and Bitget.
- Only the Aster and Nado venue aggregates currently include their standalone
  reference-data target. Other venue aggregates may include different read
  checks, but not the exact reference-data target in the table.
- The `test-bitget-testnet-*` target names are compatibility aliases for the canonical
  Bitget Demo/PAP targets. They do not identify a second environment.
- `make test-live-read` is a broad opt-in SDK/adapter smoke path. Because it does
  not provide the per-target zero-skip contract, it is not certification
  evidence by itself.

Run the exact target whose product and evidence scope you intend to claim.

## Gates, skips, and serial execution

- Live reads require `BOLTER_ENABLE_LIVE_READ_TESTS=1`. Canonical Make targets set
  it command-locally.
- Live writes require a venue-specific command-local gate. Run the Make target;
  do not make a write gate persistent configuration. Exact names are in
  [Configuration](configuration.md).
- Canonical external targets use `internal/testenv/cmd/noskipgotest`: a skipped
  test is a non-pass, not successful evidence. Missing credentials, an unavailable
  product, or a selector mismatch therefore blocks a certification claim.
- The top-level Makefile declares `.NOTPARALLEL`, which serializes prerequisites
  in one invocation. Separate Make processes can still overlap. Run live-write
  commands serially and wait for terminal verification before starting another.

## Scoped state and terminal proof

The public onboarding journeys are repository acceptance harnesses, not reusable
strategy applications:

- [Binance Spot Demo](../getting-started/cex-demo.md) runs
  `make test-binance-demo-runtime-spot`. Success covers validation-owned orders
  for the selected symbol and an authoritative base-balance delta below one size
  step.
- [Hyperliquid standard Perp Testnet](../getting-started/dex-testnet.md) runs
  `make test-hyperliquid-testnet-runtime-perp`. Success covers loaded open orders
  and both venue-account and runtime positions in the selected standard-Perp
  scope.

The same principle applies to every live target: a zero exit proves only the
symbol/product/account scope and terminal assertions encoded by that harness. It
does not prove account-wide emptiness, mainnet behavior, or evergreen production
readiness.

A nonzero result, timeout, skip, or ambiguous submission does not prove cleanup.
Inspect the validation-owned IDs and exact product scope before remediation.
Never blindly resubmit an ambiguous order, cancel unrelated orders, or flatten an
unconfirmed account. See
[Operations and recovery](../guides/operations-recovery.md).

## Certification record

Use the [status ontology](glossary.md). A public
**Demo/Testnet-certified** summary must name:

- the candidate identifier and validation date;
- the environment and exact product scope;
- the exact Make target and zero-skip result;
- the harness's terminal state assertions; and
- any known limitation that narrows the evidence.

Publish that concise, redacted summary—not credentials, raw logs, account
residuals, or local paths. Implementation and capability declarations remain
separate claims from external evidence.
