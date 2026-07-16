# Lighter

[Chinese mirror](../zh-CN/venues/lighter.md) · This English page is canonical.

## Ownership and scope

This page owns Lighter Testnet runtime products and the distinction between
reference streaming and ordinary market-data streaming. It also defines the
limited scope of current write acceptance.

## Environment, products, and identity

Runtime adapters cover Spot cash and Perp on Lighter Testnet through one unified
Lighter account index, represented by the default logical account `LIGHTER-001`.
Authenticated writes require the account index, API-key index, and matching
private key. Read-only construction still resolves the configured account and
market indices. Use [Configuration](../reference/configuration.md) for the exact
credential and gate names.

## Implemented surface

| Product | Market requests | Normalized market streams | Private streams |
| --- | --- | --- | --- |
| Spot | `OrderBook`, `Bars` | `SubscribeBook`, `SubscribeQuotes`, and `SubscribeTrades` are unsupported | Execution and account streams are unsupported; `Adapter.Start` is intentionally a no-op and runtime reconciliation uses REST. |
| Perp | `OrderBook`, `Bars`; current derivative reference and OI | Only `SubscribeReference` is wired, using WS `market_stats` for funding, mark, and index fields. Book, quote, and trade subscriptions remain unsupported. | Execution and account streams are unsupported; runtime reconciliation uses REST. |

The coarse dynamic market-stream bit can be true because a WS client exists. It
means only that the Perp reference subscription can connect; it is not evidence
for book, quote, or trade streams.

Both products implement `Submit`, `Cancel`, `CancelAll`, and `Modify`.

### Orders and account mutation

- Only limit orders are accepted. Supported TIF values are GTC/default, IOC, and
  GTX/post-only; FOK and other TIF values are unsupported. GTC/default is encoded
  as a 28-day venue `GoodTillTime`, not as an unbounded lifetime.
- Only net position mode is supported. Spot rejects `ReduceOnly`; Perp preserves
  it.
- The unified account client exposes leverage and cross/isolated margin-mode
  code paths, but their adapter/SDK leverage-parameter conversion is not a stable
  public contract, Spot calls are not rejected locally, and current Testnet
  acceptance does not exercise either method. Do not use these mutations; they
  are not part of the documented usable surface.

### Reports, positions, and account state

Open-order reports are available, but exact single-order status is implemented
through the open-order path. A missing order is therefore ambiguous rather than
terminal evidence. Fill-history reports are unsupported. Mass status can report
open orders and Perp account positions, with coverage warnings when pagination or
client-ID reconstruction prevents completeness; fills remain unavailable.

REST account state supplies balances, collateral/margin values, and Perp
positions for the unified account. There is no Spot position model and no account
stream.

## Derivative reference data

Perp exposes a REST funding snapshot, a WS `market_stats` reference stream with
funding/mark/index fields, and current open interest from order-book detail.
Current OI is queried directly and is not cached by the runtime. Spot has no
derivative-reference surface.

## Verification commands

```sh
make test-lighter-testnet-runtime-spot
make test-lighter-testnet-runtime-perp
make test-lighter-testnet-acceptance
make test-lighter-testnet-reference-data-read
```

The aggregate target combines read and write targets under the repository's
zero-skip runner. Only a recorded successful zero-skip run proves the named
Testnet scope.

## Acceptance, cleanup, and ambiguity

Current Spot and Perp write acceptance is deliberately narrower than the full
command surface: it places one resting GTX order, proves it did not fill, cancels
that exact order, and performs final REST reconciliation. It does not prove a
successful fill, a normal close, fill history, or private-stream delivery.

The test account must begin without conflicting open orders or positions.
Cleanup tracks the exact client/venue identity. If submit visibility is
ambiguous, it polls bounded exact/open evidence and only performs bounded
exposure cleanup when account evidence shows that exposure changed; it does not
blindly resubmit or use broad `CancelAll` as its primary cleanup path.

See [Market and Reference Data](../guides/market-reference-data.md),
[Operations and Recovery](../guides/operations-recovery.md),
[Testing](../reference/testing.md), and the
[capability matrix](../adapter-capabilities.md).
