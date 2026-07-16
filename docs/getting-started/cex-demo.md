# Binance Spot Demo Runtime Acceptance

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/cex-demo.md)

This page owns the canonical CEX onboarding lifecycle. It runs the real Binance
Spot Demo adapter through a runtime node; it is an acceptance harness, not a
reusable trading application or production certification.

## Prepare credentials and the Spot account

Set these Binance Demo credentials in the shell or the repository's ignored
`.env` file:

| Variable | Requirement |
| --- | --- |
| `BINANCE_DEMO_API_KEY` | Required Demo API key |
| `BINANCE_DEMO_API_SECRET` | Required Demo API secret |

Optional controls are:

- `BINANCE_DEMO_SYMBOL`, default `ETH-USDT`;
- `BINANCE_DEMO_MAX_NOTIONAL_USDT`, default `100`;
- `BINANCE_DEMO_ORDER_QTY`, default `0`, which asks the harness to select a safe
  quantity from venue filters and the notional envelope.

Use a dedicated, funded Demo Spot account. The selected symbol must have no open
orders before the test. The account may already hold the base asset because the
harness captures a balance baseline, but it must have enough available quote
currency for the bounded IOC buy; the preflight requires a 5% reserve above the
planned buy notional. Do not use production credentials.

## Run the single target

From the repository root, with no other external write target running:

```sh
make test-binance-demo-runtime-spot
```

The recipe sets `BOLTER_ENABLE_BINANCE_DEMO_WRITES=1` only for this command,
selects `TestBinanceSpotDemoRuntimeAcceptance`, rejects a skipped test, and gives
the Go test process a six-minute timeout. Missing credentials, an unavailable
Demo environment, failed preflight, skip, timeout, or nonzero exit is not a pass.

## Understand the lifecycle

On a successful run, the harness performs this sequence:

1. It resolves the selected symbol and live price/size filters, confirms no open
   order for that symbol, captures base and quote balances, and checks funding.
2. It constructs the real adapter and runtime node, attaches an account-required
   max-notional risk envelope, calls `node.Resync`, applies exactly one cash
   `AccountState`, starts streams, and waits for the node to become active. An
   oversized order is rejected locally without reaching Binance.
3. It submits a post-only `GTX` limit buy below the market, verifies that it did
   not fill, cancels it through runtime, and observes authoritative cancellation
   plus the canceled state in runtime cache.
4. It submits a marketable `IOC` limit buy, verifies a positive bounded fill,
   observes the order and fill through runtime cache, metrics, portfolio, and the
   authoritative base-balance increase, then reconciles again.
5. It floors only that observed balance increase to the venue `SizeStep` and, if
   it remains tradable, sends one `IOC` limit sell. The close quantity can never
   exceed the confirmed lifecycle fill, and an ambiguous close is not retried.
6. It performs a final reconciliation and rechecks authoritative venue and
   account state.

`BINANCE_DEMO_MAX_NOTIONAL_USDT` is a local test/risk envelope. It is not an
exchange-capacity model; Binance remains authoritative for liquidity and final
acceptance.

## Interpret success and ambiguity

A zero exit proves only the selected Spot scope at that run's candidate and
time:

- Binance reports no open order for the selected symbol;
- runtime has applied a fresh cash-account snapshot;
- the absolute available base-asset delta from the captured baseline is less
  than one venue `SizeStep`.

It does not prove whole-account balance equality, a derivative position state,
mainnet behavior, or future availability.

After any nonzero or ambiguous exit, treat terminal state as unconfirmed. Preserve
the exact validation client IDs and any venue order IDs for the resting, fill,
and close attempts. Before another write, inspect those exact orders, all
authoritative open orders for the selected symbol, and the available base balance
against the captured pre-run baseline. Deferred cleanup cancels only identified
test orders and closes only a confirmed, bounded validation-created increase. If
identity or fill evidence is unresolved, do not guess; another sell could create
an unintended short inventory and an account-wide cancel could remove unrelated
orders.

## Related guidance

See the [Binance venue guide](../venues/binance.md),
[operations and recovery](../guides/operations-recovery.md), and
[testing reference](../reference/testing.md).
