# Accounts and instruments

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/accounts-instruments.md)

Two things must be named before anything can be traded: whose credentials are
acting, and which tradable thing they are acting on. The gateway owns both
answers, and both are narrower than they look.

The [Glossary](../reference/glossary.md) owns the vocabulary. This page owns the
model behind it.

## An account is a saved venue connection

Gateway stores one account record containing its ID, venue, display name,
encrypted credential bundle, creation time and validation time. Credential
material is never included in account responses. Login protects access to the
personal instance; there are no per-account roles or permission grants.

Adding an account validates the credential format and queries a venue balance.
A successful read, including zero balance, saves and connects the account. It
does not prove that the key can place orders: the venue enforces that permission
and its error is returned when an operation is rejected.

## Add, view and delete

These are the only account operations. There is no desired-state revision,
credential version, rotation, activation or disable workflow. Connection health
is an observation of the current local connection, not a persisted lifecycle.

Deletion stops new calls, waits for calls already in flight, closes local
clients and deletes the credential record. It does not cancel orders, close
positions or require an empty venue account. To change credentials, delete and
add again. The new account gets a new ID and reads the venue's current state;
there is no historical order migration.

Orders retain their own immutable IDs, dispatch records and reconciliation
state. Removing credentials does not delete that evidence or turn an unknown
order outcome into a rejection. See [Execution and risk](execution-risk.md).

## Routes describe adapter support

A route identifies a product, settlement asset and route profile. Gateway derives
these routes from the exchange registry; users do not configure them as key
permissions. A venue can have several routes when products or settlement assets
use different clients or endpoints.

Only production endpoints are supported. The simplified registration flow does
not import old account records or reinterpret their environment settings.

## Instruments are a projection, not a pass-through

The gateway's instrument type is a deliberate narrowing of the exchange layer's.
Native symbols, native IDs, transport encodings, and credential-bearing metadata
do not cross that boundary. What crosses is:

| Field | Meaning |
| --- | --- |
| Instrument ID | The stable four- or five-segment name |
| Venue, environment, product, settlement | The scope it was resolved under |
| Symbol, base asset, quote asset | Canonical, venue-neutral |
| Rules | Price increment, quantity increment, minimum quantity, minimum notional |

The instrument ID's grammar is enforced, not conventional. The last segment must
be a canonical symbol, and the validator insists on the hyphen — a native symbol
that leaked upward has no hyphen, so the cheapest possible check catches the
failure that matters most. Where a settlement segment appears, it must be a
normalized asset with no hyphen of its own.

The four rules are the entire pre-trade shape contract. There is no venue-tier
fee schedule, no per-account limit, no liquidity hint. Those are venue answers
that change under you, and a rule that is stale is worse than a rule that is
absent.

## Freshness is part of the answer

A catalog snapshot is not just a list of instruments. It carries a revision, a
generation, and the moment it was loaded, and the status derived from it reports
staleness, read readiness, and write readiness as separate facts with a reason
attached.

They are separate because they fail separately. A snapshot can be fresh enough
to quote and too stale to trade against. A refresh error does not empty the
catalog — it marks it, so a read can still be served from known-old data while a
write is refused. Collapsing these into one boolean would force the wrong answer
in both directions.

Write readiness has a second input beyond freshness: whether the product row is
promoted for writes at all, and whether this specific account is currently ready.
Nineteen of the twenty rows accept writes by default; see
[Unsupported surfaces](../reference/unsupported.md) for the one that does not,
and [Operations and recovery](../guides/operations-recovery.md) for what makes an
individual account stop being ready.

## What this buys the layers above

A product service asks for an instrument by ID and gets back something it can
render, validate against, and store — with no venue-specific branch anywhere in
it. It asks for an account and gets back a record it can display without ever
holding a secret. Both properties hold for all twenty product rows, which is the
only reason a single order ticket can serve nine venues.

## Related

- [Architecture](architecture.md) — where this boundary sits
- [Execution and risk](execution-risk.md) — connection checks and order identity
- [State and reconciliation](state-reconciliation.md) — how an account's view of
  the venue is rebuilt
- [Market and reference data](../guides/market-reference-data.md) — the read
  surface over this model
- [Exchange gateway](../guides/exchange-gateway.md) — the service that serves it
