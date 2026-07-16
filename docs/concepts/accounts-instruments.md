# Accounts and Instruments

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/accounts-instruments.md)

This page owns the venue-neutral instrument, account, balance, margin, position,
and account-readiness models. Report coverage and convergence belong in
[State and Reconciliation](state-reconciliation.md).

## Instruments separate neutral and venue identity

`InstrumentID` is the runtime key: `Venue`, canonical `Symbol`, and product
`Kind`. It never contains a venue's integer asset index or alternate wire symbol.
Those forms live on the resolved `Instrument`, which adapters expose through the
neutral `InstrumentProvider` contract.

An `Instrument` carries:

- neutral base, quote, and settlement currencies;
- adapter-owned `VenueSymbol` and optional `AssetIndex` routing identity;
- exact-decimal price tick, size step, minimum quantity, minimum notional, and
  contract multiplier;
- derived price precision and a neutral net-only or hedge-capable position-mode
  capability.

Runtime consumes these fields for exact calculations and generic policy; it does
not reinterpret venue symbol rules. Prices, quantities, multipliers, balances,
and PnL use `shopspring/decimal`, with floating point confined to wire conversion
where unavoidable.

## The exact `AccountState` snapshot

Every account adapter must implement `AccountClient.AccountState`. The snapshot
contains only these account domains:

| Field group | Fields |
| --- | --- |
| Identity | `AccountID`, `Venue`, `Type`, `BaseCurrency` |
| Balances | `Balances[]` |
| Margin requirements | `Margins[]` |
| Optional aggregate | `Summary` |
| Reporting and event identity | `Reported`, `EventID` |
| Times | `TsEvent`, `TsInit` |

`Type` is the coarse neutral account type `Cash` or `Margin`; `Unknown` is not a
valid authoritative state. Each `AccountBalance` contains `AccountID`, currency,
total, free, locked, borrowed, interest, and update time. Each `MarginBalance`
contains currency, optional instrument scope, initial requirement, maintenance
requirement, and update time. The optional summary contains settlement currency,
equity, available collateral, and update time.

Orders and positions are deliberately not embedded in `AccountState`. Order,
fill, and position reports are typed execution-report domains. An
`AccountClient.Positions` snapshot may provide scoped position evidence where
the adapter advertises it, but reconciliation converts that evidence into the
same position-report comparison path rather than treating it as part of the
account snapshot.

## Positions and balances

A neutral `Position` has account and instrument identity, a position side,
signed quantity, entry and mark prices, unrealized PnL, leverage, and update
time. Positive quantity is long and negative quantity is short; `PositionSide`
keeps hedge-mode legs distinct when both can coexist.

For cash balances, the model can validate `total = free + locked` when there is
no borrowing or interest. Margin-account `Free` can represent free margin, so
that cash invariant is not imposed universally.

## Account identity and readiness

Execution and account clients for real venues expose their logical account
through `contract.AccountIDProvider`. If both are configured, their IDs must
agree; an explicitly expected runtime account ID must also match. Empty or
conflicting adapter identities fail before startup or reconciliation.

`AccountState.ValidateTradingReady` requires a valid snapshot, `Reported=true`,
a non-empty event ID, event and initialization timestamps, a positive stale
threshold, and a fresh account-state or reconciliation time. Generic risk can
require this authoritative readiness for risk-increasing orders; detailed risk
policy is owned by [Execution and Risk](execution-risk.md).

## Account mode belongs to the adapter

Venue account modes, account roles, margin modes, and position modes do not
become runtime branches. The adapter discovers and validates forms such as
unified/classic accounts, owner/agent roles, cross/isolated margin, and
one-way/hedge positions, then fails closed when the configured shape is not
supported. `AccountState.Type` remains the coarse neutral cash-or-margin result,
not a copy of every venue mode.

The portable `AccountClient` contract still exposes `SetLeverage` and
`SetMarginMode`. These are neutral commands, not discovery mechanisms. Each
adapter implements the venue mapping or returns `contract.ErrNotSupported`, and
support must be checked per venue and product.

See the [capability matrix](../adapter-capabilities.md),
[Configuration](../reference/configuration.md), and
[Unsupported and Deferred Features](../reference/unsupported.md) before relying
on an account or product mode.
