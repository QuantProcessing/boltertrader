# The trading service

> Canonical language: English. [Chinese mirror](../zh-CN/guides/trading-service.md)

The trading service hosts the products. There are two: **manual trading**, which
places one order at a time, and **funding arbitrage**, which places two at once
on different venues. Both stand on the same command ledger, the same
reservations, the same reconciler, and the same projections — a product is a set
of endpoints and an intent shape, not a second copy of the machinery.

The service holds no venue credential and opens no venue connection: every write
goes through [the exchange gateway](exchange-gateway.md).

It never serves a browser directly. Requests arrive from
[the auth edge](auth-edge.md) with a service token and an attested principal.

## The command ledger

Every intent that can reach a venue is written down before it is attempted. A
command is created in `accepted`, and moves through a fixed set of states:

| State | Meaning |
| --- | --- |
| `accepted` | Recorded and reserved, not yet handed to the gateway |
| `submitted` | Handed off; the gateway owns it now |
| `uncertain` | The handoff's outcome is unknown |
| `filled` / `canceled` / `failed` | Terminal |

The transitions are enumerated, not implied: `accepted` may go anywhere,
`submitted` may not go back to `accepted`, and `uncertain` may recover to
`submitted` or settle. A terminal state is final.

`uncertain` is the state that earns the ledger its keep. When a handoff times out
after the request left, the service does not know whether the venue accepted the
order, and guessing in either direction is a real loss: assume rejection and you
place twice, assume acceptance and you report an order that does not exist. So
it records the ambiguity and lets reconciliation resolve it against the venue's
own answer.

## Two identifiers, two namespaces

A manual order has two identities, and they are not interchangeable.

The **`commandId`** is the trading service's. It is what the interface shows,
what the activity feed keys on, and what `GET /api/v1/commands/{commandId}`
takes.

The gateway mints its **own** command identifier when it accepts the placement,
and its own durable order identity beneath that. Those live in the command's
execution evidence, and they are what a cancel names.

These are separate namespaces on purpose. Requiring them to be equal made every
cancel impossible, because they never are. What a cancel actually needs is the
gateway order to name and the gateway command that produced it — so that is what
the service asserts before it will attempt one. When reading gateway logs or
audit rows, quote the gateway's identifiers; when reading the interface or the
activity feed, quote the `commandId`.

## The order ticket

Manual placement is a two-step flow, and the split is the point.

`POST /api/v1/products/manual/execution-previews` returns a **sealed** preview.
The service prices the intent, validates it against the instrument's rules,
takes a reservation against the account, then signs the resulting object and
gives it a short expiry.

`POST /api/v1/products/manual/commands` accepts that sealed preview. It cannot
accept anything else — not a modified copy, not an expired one, and not an
intent the preview never priced. The consequence is that "what the operator
confirmed" and "what was sent to the venue" are the same object rather than two
renderings of the same form.

Reservations are their own resource (`/api/v1/reservations`), created,
consumed, or released. A reservation that is never consumed is released rather
than left holding capacity.

## The arbitrage product

The second product trades the funding spread rather than the price. It takes one
instrument on two venues, goes long where funding is cheapest and short where it
is dearest, and holds a delta-flat pair that earns the difference. It is a
cross-venue perpetual hedge and nothing else: no single-venue basis, no spot leg,
no dated contract.

Rates are not comparable as published. Venues settle funding on their own
schedule — four hours here, eight there — so a raw rate says nothing until it is
normalized. Every rate is converted to a daily figure (`rate × 24 ÷
intervalHours`) before any two are compared, the spread is `shortDaily −
longDaily`, and the headline APR is that spread times 365. A venue that will not
say what its interval is cannot be ranked, and the service says so instead of
assuming eight hours.

`GET /api/v1/products/arbitrage/opportunities` ranks the pairs across the
accounts named in the request. Each carries its own `blockers`, and a pair with
any of them is not executable: `funding_interval_unknown`, `account_unreachable`,
`instrument_unlisted`, `board_stale`. An empty list is the only thing that makes
the ticket live.

Placement reuses the manual flow's shape exactly.
`POST /api/v1/products/arbitrage/execution-previews` returns a sealed preview —
the same seal, the same expiry — except that it prices both legs and reserves
against both accounts. It also carries `warnings`, which are not blockers and do
not stop a confirm: `fees_excluded` says the quoted spread is gross,
`negative_spread` says the ranking inverted between the read and the preview,
`funding_rate_unavailable` and `board_stale` say a leg's number is older than it
looks.

`POST /api/v1/products/arbitrage/commands` takes that preview and carries all
three verbs, distinguished by `action`:

| Action | What it does |
| --- | --- |
| `open` | Places both legs from the sealed preview, in one request |
| `close` | Flattens both legs reduce-only, in one request |
| `cancel` | Withdraws one named resting leg |

Every one of them requires a `lineageId` and an `Idempotency-Key` that matches
the body. The **lineage** is the pair's identity: two orders on two venues in two
command rows, tied together by one identifier. It is what makes "close the pair"
a single operation rather than two cancels the operator has to sequence
correctly, and it is what `GET /api/v1/products/arbitrage/positions` groups by.
A pair whose leg payload cannot be read back reports `leg_payload_unreadable`
and refuses to close, because a close it cannot describe is a close it cannot
prove.

Both legs still go through the ledger one command at a time. There is no atomic
two-venue write and the service does not pretend otherwise: a pair can end up
with one leg filled and one leg `uncertain`, which is exactly the state the
ledger exists to hold. Reconciliation resolves each leg against its own venue,
and the pair is whatever its legs turn out to be.

All three actions sit behind the same live-write switch as every other private
write: with it closed, `open`, `close`, and `cancel` return
`403 LIVE_PRIVATE_WRITE_DISABLED` before anything reaches a venue.

They used to be refused unconditionally, on the grounds that a pair could be
exercised in the test tier until the product had evidence it did not yet have.
That tier is gone, so the refusal has nowhere left to send anyone, and the
switch is the only thing standing between an `open` and two real orders. The
evidence gap it was standing in for has not closed — arbitrage has less behind
it than the manual product does, and the switch does not know that.

## Reconciliation

A ticker-driven reconciler claims due tasks under a lease and asks the gateway
for the authoritative answer. It runs once a second, claims up to twenty tasks
at a time, and holds each for fifteen seconds.

The reconciler — not the HTTP handler — is what learns a venue's final answer.
The handler returns as soon as the handoff is made; the fill or the cancel
arrives later. So terminal states reach the activity feed through exactly one
path: a settlement observer registered on the reconciler, which publishes the
command when it lands in `filled`, `canceled`, or `failed`.

Without that observer the audit trail would stop at "submitted" and never record
what actually happened. If a terminal state is missing from the activity feed
while the command shows one in the ledger, that observer is where to look.

The gateway runs its own reconciliation, over its own durable orders, at its own
layer. The two are independent, and neither substitutes for the other. See
[State and reconciliation](../concepts/state-reconciliation.md).

## Projections

Reads are served from projections the service maintains, not from live venue
calls:

- `/api/v1/positions`, `/api/v1/pnl` — portfolio state.
- `/api/v1/fills`, `/api/v1/ledger` — what happened, and its accounting.
- `/api/v1/activity` — the command chain, in order, with timestamps.
- `/api/v1/accounts/{accountId}/runtimes` — the read and write readiness the
  interface shows on each account.
- `/api/v1/catalog/venues`, `/api/v1/catalog/instruments` — what can be traded.
- `/api/v1/market-data`, `/api/v1/candles` — the ticker strip and the chart.
- `/api/v1/products/arbitrage/opportunities`,
  `/api/v1/products/arbitrage/positions` — the ranked spreads and the open pairs.
- `/api/v1/realtime/stream` — the single server-sent stream the interface holds
  open.

Two reads are not projections and do not pretend to be.
`/api/v1/funding-board` and `/api/v1/funding-history` are served from the
gateway's credential-free public plane, which needs no account to answer. The
board is the whole matrix, one column per venue-product-settlement; the history
is one instrument on one venue over a bounded window, `7d` or `30d` and nothing
else — an open range would mean paginating a venue call, which is the thing this
read is built to avoid. A venue with no history surface is reported unavailable
rather than empty. See
[Market and reference data](market-reference-data.md).

Collection responses are enveloped as `{"data":{"items":[…]}}`, with a `meta`
object alongside `data`. Single-resource responses put the resource directly in
`data`. A client that reads `data.items` on a single resource, or `data` on a
collection, is reading the wrong shape. The board and the history are single
resources by that rule, not collections: `data` is the snapshot or the series
itself, with its columns under `venues` and its readings under `points`. Both
also carry a third top-level key, `rateLimit`, which is what is left of the
venue budget the read spent.

## Storage

The service owns its own database, `bolter_trading`, connecting as the role of
the same name. `cmd/trading` pins both rather than accepting whatever it is
given: nothing else checks them, so a typo would reconcile this service's orders
against whatever schema happened to answer.

Its tables are unqualified: the schema comes from the connection's
`search_path`, so the same binary runs against `trading`, a throwaway test
schema, or `public` without a code change. Nothing in Go names a schema.

No credential material is stored here. The trading service never sees a secret —
account credentials live behind the gateway, and it never asks for them.

## Verify it

```sh
make test
make test-browser
```

The offline suite covers the ledger's state machine, the preview seal, and the
projections. No test places an order: the external write ladder was deleted
along with the Demo and Testnet environments it ran against. What the offline
suite does and does not prove is in
[Testing and evidence](../reference/testing.md).
