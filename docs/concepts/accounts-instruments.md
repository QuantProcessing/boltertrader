# Accounts and instruments

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/accounts-instruments.md)

Two things must be named before anything can be traded: whose credentials are
acting, and which tradable thing they are acting on. The gateway owns both
answers, and both are narrower than they look.

The [Glossary](../reference/glossary.md) owns the vocabulary. This page owns the
model behind it.

## An account is four things at once

A gateway account is not a credential pair with a label. It is a record that
carries, together:

| Part | What it holds |
| --- | --- |
| Identity | Venue, venue environment, native environment, display name |
| Credentials | A reference into the secret store — never the material |
| Routes | One or more product routes, each bound to a route profile |
| Revisions | Desired-state revision, runtime generation, credential version |
| State | One of eight lifecycle states |

The separation of the credential *reference* from the credential *material* is
the load-bearing one. Credentials enter through the gateway's internal plane and
land in the secret store; every read path afterwards returns a redacted
projection that has no secret field to omit in the first place. Nothing above the
gateway can ask for the material, because no type above the gateway has a place
to put it.

The three revision counters exist so that a decision made at one moment can be
refused later instead of silently reinterpreted. They are covered in
[Execution and risk](execution-risk.md), where they do their work.

## The lifecycle is a state machine, not a status field

An account's state is what the control plane may request next, and the allowed
transitions are the whole authority:

| From | May become |
| --- | --- |
| *(new)* | `creating` |
| `creating` | `active`, `disabled` |
| `active` | `rotating`, `draining`, `disabled` |
| `rotating` | `active`, `disabled` |
| `draining` | `drained`, `disabled` |
| `drained` | `removing`, `disabled` |
| `disabled` | `removing`, `removed` |
| `removing` | `removed` |
| `removed` | *(nothing)* |

The shape is linear and irreversible apart from two excursions. **Rotation**
leaves `active` and returns to it, carrying a fence that names the credential
version and runtime generation it moves from and to. **Draining** leaves `active`
and does not return: it exists so that an account can be wound down while its
open orders and positions are still being resolved, rather than being cut off
mid-flight.

`disabled` is reachable from almost everywhere on purpose. It is the stop, and a
stop should never be blocked by which step you happened to be on.

Removal has a precondition the state table does not show: an account with open
orders, live reservations, uncertain commands, or owned positions reports those
as blockers, and the blockers are enumerated rather than summarized. "Cannot
remove" is not an answer an operator can act on; "three uncertain commands" is.

## Routes carry the product, the account does not

An account does not have a product. Its **routes** do.

Each route names a product, a settlement asset, a route profile, and the state
the operator wants it in. The profile is resolved through the route-profile
registry by venue and profile ID, and validation is a two-way agreement: the
profile's product and settlement asset must each match the route's own. A route
that resolves but disagrees is rejected, because a route profile is exactly the
thing that decides where a signed request goes.

This is why an account may hold several routes at once — a venue that settles
perps in more than one asset needs one route per settlement — and why an account
in `creating`, `active`, or `rotating` must carry at least one active route. An
account with no way to reach a venue is not in any of those states; it is
`disabled` with extra steps.

There used to be a second boundary, absolute in the same way: a mode. `test`
covered demo and testnet, `live` covered live, and a process running in one
could not resolve a profile belonging to the other. Both non-production tiers
are gone, so the boundary has one side and no longer separates anything. What
remains of it is a validation rather than a partition — an account read back
from storage whose venue environment or native environment is not `live` is
rejected, because those columns are just strings and a row written before the
endpoints were removed must not be silently routed at the live venue.

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
- [Execution and risk](execution-risk.md) — what the revision counters guard
- [State and reconciliation](state-reconciliation.md) — how an account's view of
  the venue is rebuilt
- [Market and reference data](../guides/market-reference-data.md) — the read
  surface over this model
- [Exchange gateway](../guides/exchange-gateway.md) — the service that serves it
