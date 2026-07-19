# Nado

[Chinese mirror](../zh-CN/venues/nado.md) · This English page is canonical.

## Ownership and scope

This page owns Nado Testnet product behavior, digest-based order identity,
account semantics, acceptance commands, and the server-authoritative capacity
boundary.

## Environment, products, and identity

Runtime adapters cover funded-only Spot and Perp on Nado Testnet through one
unified-margin account, represented by `NADO-001`. Private operations use a
signing key and selected subaccount. Exact environment variables and safety
gates are centralized in [Configuration](../reference/configuration.md).

Spot orders always set venue `spot_leverage=false`: the supported Spot scope uses
funded inventory and does not borrow.

## Implemented surface

| Product | Market requests | Normalized streams | Commands |
| --- | --- | --- | --- |
| Spot no-borrow | `OrderBook`; `Bars` unsupported | Book, quote, and trade streams when the WS backend is configured; configured private execution/account events | `Submit`, `Cancel`, `CancelAll`; `Modify` unsupported |
| Perp | `OrderBook`; `Bars` unsupported; derivative reference and current OI | Book, quote, trade, funding-reference, execution, and account streams when configured | `Submit`, `Cancel`, `CancelAll`; `Modify` unsupported |

### Orders and capacity

Nado requires a `ClientID`. It accepts limit and market orders in net position
mode; market orders require IOC semantics. Supported limit TIF values are
GTC/default, IOC, FOK, and GTX/post-only. Spot rejects `ReduceOnly`; Perp supports
it. Trigger, activation, trailing fields, and margin-mode mutation are unsupported.
`SetLeverage` stays in the common Perp REST surface, but Nado treats it as a
validated no-op: the call succeeds and returns `Leverage.Effective=0` because the
backend risk engine determines the actual leverage.

Order submission does not accept or require a `max_order_size` parameter. The
Nado SDK keeps `GetMaxOrderSize` as a separate raw query for API fidelity, but
the adapter submit path never calls it. There is no local venue-capacity lease or
admission protocol. The path is side-effect-free `ValidateSubmit`, optional
generic runtime risk, ordinary adapter `Submit`, then authoritative server
acceptance or rejection. Test notional limits are safety envelopes, not a model
of spare venue capacity.

The adapter prepares one signed order transiently, executes it once, redacts the
prepared material, and requires the response digest to match the signed digest.
That digest is the exact `VenueOrderID` used for status and cancellation.

### Reports and account semantics

Both products expose open orders and exact single-order recovery by digest and
stored client correlation. Fill reports use bounded archive `matches` history
(default 100, maximum 500). Spot has no position reports; Perp positions come
from the account snapshot. Mass status combines open orders, bounded fills, and
Perp positions, recording incomplete coverage instead of presenting a partial
page as complete.

Nado account state has semantics that differ from conventional free/locked
balances:

- The account snapshot must contain initial, maintenance, and unweighted health.
  `AvailableCollateral` is `max(initial health, 0)` and `Equity` is unweighted
  health. Neither value is a currency free balance.
- The venue account response has no event timestamp. The SDK wraps it in
  `AccountSnapshot` with a local `ReceivedAt`; freshness uses that receipt time
  and must not present it as an exchange event timestamp.
- Each Spot `balance.amount` is signed inventory. A negative amount remains a
  negative `Total` and produces `Borrowed=abs(amount)`; the adapter does not
  invent `Free`, `Locked`, or `Available` values.
- Perp `v_quote_balance` is venue accounting state, not free collateral.
  Position quantity comes from the signed Perp balance amount.

### Isolated-only Perp products

Discovery preserves the venue's `isolated_only` flag. For such a Perp product,
an opening order sets the isolated appendix and supplies 1x initial margin as
`price × quantity × contract multiplier`, rounded upward to six decimals. A
reduce-only close preserves the isolated appendix but adds zero opening margin.
This behavior is adapter encoding for venue-required isolated products; it does
not create a generic runtime margin policy.

## Derivative reference data

Perp exposes current funding, mark, index, and oracle price through REST plus
partial funding WS updates. Current OI is read directly from `all_products` and
is not cached by the runtime. Funding history is not advertised. Spot has no
derivative-reference surface.

`WatchMarkPrice` stays in the common Perp WebSocket surface for API symmetry,
but Nado does not provide a mark-price subscription and returns
`ErrUnsupported` immediately. Use the REST mark/index/oracle price surface when
you need Nado reference prices.

## Verification commands

```sh
make test-nado-testnet-read
make test-nado-testnet-runtime-spot
make test-nado-testnet-runtime-perp
make test-nado-testnet-acceptance
make test-nado-testnet-reference-data-read
```

The aggregate target runs read, adapter-write, and runtime-write coverage through
the zero-skip runner. Write targets use the Nado Testnet gate documented in
[Configuration](../reference/configuration.md).

## Acceptance, cleanup, and ambiguity

Spot and Perp acceptance place and cancel a resting order, execute a bounded IOC
fill, close the resulting exposure, and reconcile final state. Spot uses a
selected balance baseline; Perp requires the selected position to return flat.
Cleanup tracks exact digests and test-created exposure rather than using broad
`CancelAll` as its primary mechanism.

A Testnet failure or ambiguous outcome must be resolved with exact digest status,
open orders, bounded matching fills, and product-scoped account evidence. Never
infer spare capacity locally, blindly retry a submit/close, or assume that an
empty open-order result means the order never filled.

See [Execution and Risk](../concepts/execution-risk.md),
[Operations and Recovery](../guides/operations-recovery.md), and the
[capability matrix](../adapter-capabilities.md).
