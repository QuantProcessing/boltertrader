# Aster

[Chinese mirror](../zh-CN/venues/aster.md) · This English page is canonical.

## Ownership and scope

This page owns Aster Testnet product configuration, acceptance commands, and
runtime caveats, including the provenance boundary for fixtures and open
interest.

## Environment, products, and identity

Runtime adapters cover Spot cash and USDT-linear Perp on the official Aster
V3 Testnet profiles. Private operations use the API wallet address and its
EIP-712 signer key; an optional expected signer is an identity check, not another
account. Exact credential and gate names belong to
[Configuration](../reference/configuration.md).

Symbols whose normalized venue symbol begins with `TEST` are fixture-only and
are excluded from discovery and write selection.

## Implemented surface

| Product | Market requests | Normalized streams | Commands |
| --- | --- | --- | --- |
| Spot | `OrderBook`; `Bars` unsupported | Book, quote, and trade market streams plus configured execution/account streams | `Submit`, `Cancel`, `CancelAll`; `Modify` unsupported |
| USDT-linear Perp | `OrderBook`; `Bars` unsupported; derivative reference, funding history, and current OI | Book, quote, trade, reference, execution, and account streams when configured | `Submit`, `Cancel`, `CancelAll`; `Modify` unsupported |

Account state is an authoritative REST snapshot. Stream events update normalized
order/fill/balance/position state but do not broaden the supported request
methods.

### Orders and account mutation

- Spot and Perp accept limit and market orders. Limit TIF is GTC/default, IOC,
  FOK, or GTX. Market orders reject an explicit TIF and an explicit limit price.
- Spot is cash-only and rejects `ReduceOnly` and non-net position sides.
- Perp is one-way/net only and supports `ReduceOnly`; hedge position mode is
  unsupported.
- Leverage and margin-mode mutation are not implemented for either adapter.

### Reports, positions, and account state

Both products expose open orders, exact single-order status, and bounded `my
trades` fill reports. Execution mass status caps each fill query at 1,000 records
and marks incomplete coverage instead of implying an unbounded ledger. Open-order
absence alone is not terminal-order proof.

Spot has balance/account snapshots and no position reports. Perp positions come
from the account snapshot. Configured private streams provide incremental
execution and account events for both products.

## Derivative reference data and provenance

Perp exposes current funding, mark, and index price through REST and mark-price
WS updates, plus bounded funding history. Current OI is a direct, uncached request
to the probe-backed V3 route `/fapi/v3/openInterest`.

The checked-in Aster fixtures are sanitized synthetic derivatives of the sources
declared in the fixture manifest; they are not captured account data. Most
declared sources are official examples. The OI fixture derives from separately
recorded Testnet probe evidence because the route was absent from the inspected
V3 Markdown. If that route becomes unavailable or incompatible, the SDK returns
a typed unavailable error. It must not fall back to a V1 route or synthesize OI
from another field.

## Verification commands

```sh
make test-aster-testnet-read
make test-aster-testnet-runtime-spot
make test-aster-testnet-runtime-perp
make test-aster-testnet-acceptance
make test-aster-testnet-reference-data-read
```

The aggregate target runs the read, adapter-write, and runtime-write targets
through the zero-skip runner. Write targets are guarded by the Aster Testnet gate
documented in [Configuration](../reference/configuration.md).

## Acceptance, cleanup, and ambiguity

Spot and Perp acceptance place and cancel a resting order, execute a bounded IOC
fill, close the resulting exposure, and reconcile final state. Spot cleanup uses
the selected base-currency balance baseline and dust tolerance. Perp cleanup
requires the selected position to return flat. Pre-existing open orders or Perp
positions make the zero-skip target fail rather than silently certifying a dirty
account.

If a command outcome is ambiguous, resolve exact order status, open orders,
bounded matching fills, and the selected balance/position before another write.
Cleanup is limited to test-created identities and exposure; it must not infer
success from an empty open-order snapshot or use account-wide cancellation as a
replacement for exact evidence.

See [Configuration](../reference/configuration.md),
[Operations and Recovery](../guides/operations-recovery.md),
[Testing](../reference/testing.md), and the
[capability matrix](../adapter-capabilities.md).
