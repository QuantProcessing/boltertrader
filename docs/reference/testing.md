# Testing

> Canonical language: English. [Chinese mirror](../zh-CN/reference/testing.md)

This page is the inventory of what you can run and what a green run proves. The
default path is offline, credential-free, and deterministic; everything that
touches a venue is a separate, explicitly named target.

## The default path

```sh
make test
```

`go test -short ./...` across the repository. It needs no credentials, no
Docker, and no network. This is the gate to run before every commit.

| Target | Scope |
| --- | --- |
| `make test` | The whole repository, short mode |
| `make test-sdk` | `./sdk/...` only |
| `make test-exchange-offline` | `./exchange/...` with `-count=1` |
| `make test-exchange-race` | `./exchange/...` under `-race` |
| `make test-exchange-redaction` | The six tests that prove credentials never reach an error string |
| `make test-exchange-quality` | offline, then redaction, then race |
| `make test-gateway-offline` | `./gateway/...` and `./cmd/exchange-gateway` |
| `make test-gateway-contract` | `contracts`, `domain`, `capability`, `store` |
| `make test-gateway-security` | Import boundary, OpenAPI, config redaction, crosswalk |
| `make test-gateway-race` | The gateway packages under `-race` |
| `make test-gateway` | offline, then race, then security |
| `make test-browser` | The web app: install, build, Vitest, Playwright |

`go test ./...` without `-short` is not the safe default. Tests that can reach a
venue inspect the environment when short mode is off.

Two gateway targets need a real PostgreSQL and read `GATEWAY_TEST_POSTGRES_DSN`
from the environment; they fail fast when it is unset rather than skipping.
`make test-gateway-postgres` covers migrations, the command store, the audit
store, and account ownership. `make test-gateway-restart` covers the coordinator's
process-crash windows. `make test-compose-postgres` provisions a throwaway
cluster and runs both for you, which is the easier path.

## Container gates

```sh
make test-compose
```

Six sub-targets, in order:

| Target | What it proves |
| --- | --- |
| `test-compose-config` | The Compose file renders and the binaries load their config |
| `test-compose-build` | Both images build |
| `test-compose-security` | Non-root user, pinned entrypoint, no source or secrets in the image, all seven binaries present |
| `test-compose-smoke` | Migrators complete, services start, the gateway answers its own live and ready probes, and each service exits `0` within 15 seconds of `SIGTERM` |
| `test-compose-persistence` | Data survives a container recreate |
| `test-compose-postgres` | The real-PostgreSQL suites, against a throwaway cluster |

Each of these builds its own Compose project with generated credentials and
kernel-assigned ports. None of them requires `make up` to have run, and none of
them disturbs a stack that is already up.

## External acceptance

Everything below places real orders on a real non-production venue. Every target
sets its write gate command-locally — that is the only way a write gate should
ever be set. See [Configuration](configuration.md).

### Exchange layer

Nine venues, aggregated as `make test-exchange-acceptance`.

| Environment | Aggregate | Product targets |
| --- | --- | --- |
| Binance Demo | `test-exchange-binance-demo-acceptance` | `-spot`, `-perp` |
| OKX Demo | `test-exchange-okx-demo-acceptance` | `-spot`, `-perp` |
| Bybit Demo | `test-exchange-bybit-demo-acceptance` | `-spot`, `-usdt-perp`, `-usdc-perp` |
| Bitget Demo | `test-exchange-bitget-demo-acceptance` | `-spot`, `-usdt-perp`, `-usdc-perp` |
| Gate Testnet | `test-exchange-gate-testnet-acceptance` | `-spot`, `-usdt-perp` |
| Aster Testnet | `test-exchange-aster-testnet-acceptance` | `-spot`, `-usdt-perp` |
| Nado Testnet | `test-exchange-nado-testnet-acceptance` | `-spot`, `-usdt0-perp` |
| Hyperliquid Testnet | `test-exchange-hyperliquid-testnet-acceptance` | `-spot`, `-perp` |
| Lighter Testnet | `test-exchange-lighter-testnet-acceptance` | `-spot`, `-perp` |

Product target names are the aggregate's prefix plus the suffix, so Bybit's USDC
perpetual is `make test-exchange-bybit-demo-usdc-perp`.

### Gateway product rows

Twenty targets, one per gateway product row, aggregated as
`make test-gateway-demo-testnet`:

| Venue | Targets |
| --- | --- |
| Binance | `test-gateway-binance-spot-demo`, `test-gateway-binance-usd-perp-demo` |
| OKX | `test-gateway-okx-spot-demo`, `test-gateway-okx-usdt-perp-demo` |
| Lighter | `test-gateway-lighter-spot-testnet`, `test-gateway-lighter-perp-testnet` |
| Hyperliquid | `test-gateway-hyperliquid-spot-testnet`, `test-gateway-hyperliquid-perp-testnet` |
| Bybit | `test-gateway-bybit-spot-demo`, `test-gateway-bybit-usdt-perp-demo`, `test-gateway-bybit-usdc-perp-demo` |
| Bitget | `test-gateway-bitget-spot-demo`, `test-gateway-bitget-usdt-perp-demo`, `test-gateway-bitget-usdc-perp-demo` |
| Gate | `test-gateway-gate-spot-testnet`, `test-gateway-gate-usdt-perp-testnet` |
| Aster | `test-gateway-aster-spot-testnet`, `test-gateway-aster-usdt-perp-testnet` |
| Nado | `test-gateway-nado-spot-testnet`, `test-gateway-nado-usdt0-perp-testnet` |

These are named for the product rows the gateway exposes, and each one runs the
exchange-layer acceptance test for that row. The naming exists so that a claim
about a gateway product row maps to exactly one command — not so that the same
test can be counted twice.

## Gates, skips, and serial execution

**Write gates are command-local.** Each external target prefixes its own
`BOLTER_ENABLE_<VENUE>_<ENV>_WRITES=1`. Exporting one into your shell profile or
a `.env` file turns every subsequent `go test` into a potential venue write.
Don't.

**A skip is a failure.** External targets run through
`internal/testenv/cmd/noskipgotest`, which fails when zero tests executed and
fails when any test skipped. A missing credential, an unavailable product, or a
selector that matched nothing therefore cannot be mistaken for a pass.

**Runs are serial.** The Makefile declares `.NOTPARALLEL`, which serializes
prerequisites within one invocation. It does not serialize separate `make`
processes. Run one live-write target at a time and wait for terminal
verification before starting the next.

## What a green run proves

A zero exit proves the product, symbol, and account scope that harness encodes,
and nothing wider. It does not prove account-wide emptiness, mainnet behavior,
or continued production readiness.

A nonzero result, a timeout, a skip, or an ambiguous submission does not prove
cleanup ran. Inspect the harness-owned order IDs and the exact product scope
before remediating, and never blind-cancel unrelated orders or flatten an
unconfirmed position. See
[Operations and recovery](../guides/operations-recovery.md).

## Publishing evidence

A Demo/Testnet-certified claim — the term is defined in the
[Glossary](glossary.md) — must name the environment and exact product scope, the
exact Make target, the zero-skip result and its date, and any limitation that
narrows it.

Publish that summary and nothing else. No credentials, no raw logs, no account
residuals, no local paths. Implementation, capability, and external evidence stay
three separate claims.
