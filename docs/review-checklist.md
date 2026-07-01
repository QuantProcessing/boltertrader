# Review Checklist

Use this checklist before merging runtime, adapter, SDK, or product-model
changes.

## Architecture

- The SDK layer stays faithful to the exchange API and does not normalize away
  venue-specific fields.
- The client contract layer exposes venue-neutral behavior without leaking
  runtime state ownership.
- Runtime state transitions are deterministic, replayable, and testable.
- Strategy-facing APIs preserve backtest/live parity.
- Product assumptions are explicit: spot, perp, future, and option semantics are
  not collapsed into one product model.

## Safety

- Order, fill, position, balance, PnL, reconciliation, and risk changes include
  bounded failure behavior.
- Write operations are idempotent or have documented retry and recovery rules.
- Reconnect loops have cancellation, backoff, and resubscription guarantees.
- Production live writes use venue-specific `testenv.RequireLiveWrite` gates.
- Binance Demo writes use `testenv.RequireBinanceDemoWrite`, bounded notional,
  and cleanup metadata.
- Live reads use `testenv.RequireLiveRead` and never run in default tests.

## Tests

- Default tests are offline deterministic.
- New behavior is covered by unit, fixture, scenario, or conformance tests.
- Contract tests cover every supported adapter behavior changed by the patch.
- Runtime tests cover event ordering and recovery when state is mutated.
- Race-sensitive code is covered by `make test-race` or an equivalent targeted
  race run.

## Documentation

- Public runtime APIs include enough docs for strategy authors to use them
  without reading exchange SDK internals.
- Examples compile or are covered by tests when practical.
- Live-test instructions mention exact opt-in flags.
- Any unsupported product behavior is called out directly rather than implied.
