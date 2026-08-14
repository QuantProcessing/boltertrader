# Execution and risk

> Canonical language: English. [Chinese mirror](../zh-CN/concepts/execution-risk.md)

A placement is the only operation in this system where being wrong costs money.
Everything here exists because of one property: the moment a request leaves for a
venue, you may never learn what happened to it.

This page owns what the gateway does about that. The recovery procedures are in
[State and reconciliation](state-reconciliation.md); the operator-facing steps
are in [Operations and recovery](../guides/operations-recovery.md).

## Identity exists before the venue does

The gateway mints and durably records an order's identity *before* it contacts
the venue, not after. A reservation writes three identifiers and the canonical
request in one step:

| Identifier | Shape | Purpose |
| --- | --- | --- |
| Command ID | UUIDv7 | This attempt to place |
| Gateway order ID | UUIDv7 | The order itself, for the rest of its life |
| Client order ID | Positive `uint48` | What the venue is told |

The client order ID is a `uint48` because that is the intersection of what nine
venues accept. It is a narrow integer, not a formatted string, and it is
allocated by the gateway rather than chosen by the caller.

Identity-first is what makes the ambiguous case survivable. An order that was
sent and never answered still has a durable row, a known client order ID, and a
recorded state — which is exactly the input reconciliation needs. Minting
identity on the response instead would leave nothing at all to look up.

## Idempotency is a hash, not a promise

A caller supplies an idempotency key. The gateway pairs it with a SHA-256 hash
of the request, canonicalized by decoding and re-encoding the JSON so that field
order and number formatting cannot change the result.

Two outcomes follow, and the difference matters:

- **Same key, same hash** — a replay. The gateway returns the original result,
  marked as replayed, and does not contact the venue. A retried submission is
  the same submission.
- **Same key, different hash** — a conflict, refused. The caller reused a key for
  a different order, which is a bug that must surface as an error rather than as
  a second order.

The hash covers the account, route, instrument, side, type, quantity, limit price
and policy, reduce-only flag, and the three fence revisions. Everything the
request means is in the hash; nothing the request happens to be transported as
is.

## The write fence refuses rather than reinterprets

A caller states, in the request, which route it believes it is trading through
and which revisions it decided against: account desired-state revision, runtime
generation, and credential version. All three must be positive — a zero counter
means "I did not look", and the gateway will not accept a write that never
observed the state it depends on.

The fence is checked twice on purpose: once when the route is resolved, and again
after the order is marked as dispatching and before the driver is called. The
second check is the one that matters, because a credential rotation or an account
drain can land in between.

A stale fence is refused, never reinterpreted. The alternative — reading the
request as if the current state were the state the caller meant — would silently
route an order through credentials or a route the caller never evaluated. That
is the failure mode the fence exists to prevent, so it is a refusal and not a
retry.

The gateway also holds a per-account write gate. A dispatch failure closes it,
and it stays closed until recovery completes: further placements are refused with
a not-ready error rather than being attempted against an account whose true state
is unknown.

## Nine states, and one of them is a question

An order's state is not a label attached to a venue response. It is a position in
a machine the gateway walks explicitly:

| State | May become |
| --- | --- |
| `prepared` | `dispatching`, `rejected` |
| `dispatching` | `accepted`, `uncertain`, `rejected` |
| `accepted` | `open`, `partially_filled`, `filled`, `canceled`, `rejected`, `uncertain` |
| `open` | `partially_filled`, `filled`, `canceled`, `uncertain` |
| `partially_filled` | `filled`, `canceled`, `uncertain` |
| `uncertain` | `accepted`, `open`, `partially_filled`, `filled`, `canceled`, `rejected` |
| `filled`, `canceled`, `rejected` | *(terminal)* |

`uncertain` is the design. It is a real state with a durable row, not an error
return and not an absence — an order whose fate is unknown is a fact about the
world, and a system that cannot represent it will represent it as something
false instead.

Note what `uncertain` can leave to: everything except itself. That is the whole
promise of reconciliation. Note also that terminal states have no outbound edges,
including back to `uncertain`. Once the gateway has authoritative evidence, later
doubt does not reopen the question.

The trading service keeps a *different* six-state vocabulary for its own
commands. The two are deliberately separate namespaces; see
[Trading service](../guides/trading-service.md).

## The handoff record assumes the worst

Before the driver is called, the gateway opens a handoff record. Resolving that
record is deferred, and **its default resolution is uncertain**.

This inverts the usual failure direction. If the process is killed mid-flight, if
the context is cancelled, if the driver panics — the handoff still resolves, and
it resolves to "we do not know". Only an explicit acceptance or an explicit
rejection overwrites that default. Nothing has to remember to record doubt,
because doubt is what gets recorded when nothing happens.

## Definitive outcomes and ambiguous handoff

| Outcome | Evidence | Gateway treatment |
| --- | --- | --- |
| Refused before the boundary | Validation, fence, gate, or idempotency conflict failed | No venue write. The command never leaves `prepared` |
| Confirmed accepted | A venue response with an order and no error | Walk to `accepted`, then onward as the venue reports; record the venue result |
| Definitive rejection | An explicit venue rejection, logged with the venue's own code and message | Walk to `rejected`. Terminal |
| Ambiguous | Any other post-boundary failure: timeout, cancellation, disconnect, transport error, unclassified error | `uncertain`, with a reconciliation plan attached and an outcome-uncertain error returned to the caller |

A post-boundary error is not a rejection merely because it is an error. The venue
may have accepted the write and failed to tell you. Treating that as a rejection
and retrying is how one intended order becomes two real ones.

So the ambiguous branch does not retry. It hands off to reconciliation with a
plan, returns an error that says exactly that, and closes the account's write
gate. The caller learns "unknown", which is true, instead of "failed", which is a
guess.

## What the gateway checks, and what it does not

Before dispatch, the gateway validates the request against the resolved product
and the instrument's rules — side, type, quantity and price increments, minimum
quantity, minimum notional. This is an I/O-free shape check on data the gateway
already holds.

It does not check venue liquidity, unified-margin capacity, fee tiers, or
position limits. `/v1/risk-limits` reports that venue risk limits are not
normalized and says why, rather than returning a plausible-looking set. A limit
that is stale or invented is worse than one that is absent, because it will be
trusted.

The rate limiter is the one runtime constraint the gateway does enforce
end-to-end, keyed per venue profile, per principal, and per endpoint, so one
account cannot spend another's budget.

## Related

- [Accounts and instruments](accounts-instruments.md) — what the fence's
  revisions belong to
- [State and reconciliation](state-reconciliation.md) — how `uncertain` is
  resolved
- [Operations and recovery](../guides/operations-recovery.md) — the operator
  procedure
- [Unsupported surfaces](../reference/unsupported.md) — which rows can prove
  what
