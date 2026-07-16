# Hyperliquid

[Chinese mirror](../zh-CN/venues/hyperliquid.md) · This English page is canonical.

## Ownership and scope

This page owns Hyperliquid Testnet product scopes, identity rules, dynamic stream
caveats, order and report constraints, and bounded acceptance behavior. The
[capability matrix](../adapter-capabilities.md) remains the row-by-row inventory.

## Environment, products, and identity

Runtime adapters cover Spot cash, standard Perp, and HIP-3 Perp. Public
verification uses fixed Hyperliquid Testnet REST/WS endpoints. The canonical DEX
walkthrough is [Hyperliquid Perp Testnet](../getting-started/dex-testnet.md).

Private operations use an API-wallet or owner private key. The adapter resolves
the signer through Hyperliquid `userRole`: a `user` signer owns itself, while an
`agent` signer is mapped to the returned owner address. An explicitly configured
account address must match that resolved owner. `vault` and `subAccount` roles are
rejected by the canonical account model.

HIP-3 is not inferred from a standard Perp symbol. The requested instrument must
carry an explicit HIP-3 DEX-qualified venue symbol such as `dex:coin`; discovery,
orders, reports, and cleanup remain scoped to that DEX.

Exact environment variables and safety gates are centralized in
[Configuration](../reference/configuration.md).

## Implemented surface

| Product | Market requests | Normalized market streams | Execution and account streams |
| --- | --- | --- | --- |
| Spot | `OrderBook`, `Bars` | `SubscribeBook`, `SubscribeQuotes`, and `SubscribeTrades` are unsupported | The static row does not advertise broad streaming. With authenticated WS identity, `Adapter.Start` conditionally subscribes confirmed `orderUpdates`, `userFills`, and `spotState`, emitting execution/account events and gap evidence. |
| Standard Perp | `OrderBook`, `Bars` | Book, quote, and trade subscriptions | Private order, fill, and clearinghouse/account state subscriptions when configured |
| HIP-3 Perp | Same Perp requests, scoped to the explicit DEX | Same Perp subscriptions, scoped to the explicit DEX | Same Perp private streams, with account state resolved for the selected DEX/account mode |

All three products implement `Submit`, `Cancel`, synthesized `CancelAll`, and
`Modify`. `CancelAll` enumerates and cancels current open orders; it is not an
exchange-independent cleanup substitute for tracking the exact test-created
orders.

### Orders and account mutation

- Spot accepts limit orders only. TIF is GTC/default, IOC, or GTX (mapped to
  Hyperliquid ALO); FOK is unsupported. Spot rejects `ReduceOnly` and non-net
  position sides. Leverage and margin-mode mutation are unsupported for Spot.
- Perp and HIP-3 accept limit orders, market orders with an explicit aggressive
  `Price` safety bound, and stop-loss/take-profit market or limit triggers with
  both `TriggerPrice` and wire `Price`. Limit TIF is GTC/default, IOC, or GTX;
  FOK is unsupported. Only net position mode is supported, and `ReduceOnly` is
  preserved.
- Perp `Modify` reconstructs the existing order and preserves type, TIF,
  trigger, client identity, and reduce-only semantics. It fails closed when the
  venue response does not contain enough semantics for a safe modification.
- Perp account mutation supports leverage and cross/isolated margin mode. This
  does not imply portfolio-margin or unsupported owner-role coverage.

### Reports, positions, and account state

Spot, Perp, and HIP-3 expose open-order snapshots and exact single-order status
by venue order ID or mapped client identity. `GenerateFillReports` fetches the
venue's `UserFills` snapshot and applies bounded local identity, time, and limit
filters, but declared mass-status recovery remains deliberately open-order
focused and does not advertise authoritative fill-history coverage. Do not treat
a broad mass-status call as a complete fill ledger.

Spot inventory is represented by account balances and has no position-report
surface. Perp and HIP-3 positions come from the account client. Execution mass
status does not substitute for those account-backed position snapshots.

## Derivative reference data

Standard Perp and HIP-3 expose current funding, mark price, oracle price, and
reference polling/snapshot events. Current open interest is a direct asset-context
query and is not cached by the runtime. Spot has no derivative-reference surface.

## Verification commands

```sh
make test-hyperliquid-testnet-runtime-spot
make test-hyperliquid-testnet-runtime-perp
make test-hyperliquid-testnet-runtime-hip3
make test-hyperliquid-testnet-acceptance
make test-hyperliquid-testnet-reference-data-read
```

The aggregate target includes read, adapter-write, and runtime-write coverage for
Spot, standard Perp, and HIP-3. Each write target is bounded by the Testnet write
gate documented in [Configuration](../reference/configuration.md).

## Acceptance, cleanup, and ambiguity

The write lifecycle places and cancels a resting order, submits a bounded IOC
fill, and closes the resulting exposure. Perp close is reduce-only; Spot cleanup
uses the selected base-currency balance baseline and allowed dust. Final
reconciliation proves no test-created open order and no remaining exposure only
for the selected product/instrument/account scope.

On an ambiguous submit, cancel, fill, or close, resolve the exact order identity,
open-order snapshot, matching fills, and selected balance/position before another
side effect. Never blindly retry a close or use account-wide `CancelAll` as
evidence that an ambiguous order did not execute.

See [Operations and Recovery](../guides/operations-recovery.md),
[Configuration](../reference/configuration.md), and the
[capability matrix](../adapter-capabilities.md).
