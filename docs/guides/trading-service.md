# The trading service

> Canonical language: English. [Chinese mirror](../zh-CN/guides/trading-service.md)

The trading service hosts **manual trading**, command recovery,
portfolio accounting and browser projections. Every execution shares the
same durable command ledger.

The service holds no venue credential and opens no venue connection: every write
goes through [the exchange gateway](exchange-gateway.md).

It never serves a browser directly. Requests arrive from
[the auth edge](auth-edge.md) with a service token and an attested principal.

## The command ledger

Every intent that can reach a venue is written down before it is attempted. A
command is created in `accepted`, and moves through a fixed set of states:

| State | Meaning |
| --- | --- |
| `accepted` | Recorded with a recovery task, not yet handed to the gateway |
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

Manual placement is one request. The browser may show a local confirmation from
the account, position, catalog and public prices already on screen, then sends
the order intent directly to `POST /api/v1/products/manual/commands`.

The HTTP handler authenticates, checks CSRF and decodes the request. The manual
application service owns validation, account and route resolution, command
creation and execution. A new submission resolves the current account runtime,
validates catalog rules that need no executable quote, and saves the command
with its initial recovery task in one transaction. Only the creation winner
attempts execution, after claiming the command through `BeginExecution`.
Gateway and the venue retain final authority over readiness and order admission.
Market-order notional is not estimated from a second REST quote.

Retries first look up the stored command by principal, method, path and
idempotency key. Matching user order parameters return that command even if the
account or catalog is unavailable, the runtime has changed, or writes are now
disabled. Resolved route/runtime values remain those of the original submission.
A replay never executes, including an `accepted` command whose original sender
has not started. Changed user parameters conflict; they do not place a new order.

The generic command collection is read-only; `POST /api/v1/commands` and all
`/api/v1/reservations` operations are removed. Trading does not reserve balance
or margin. Historical reservation rows and command links remain readable for
traceability, but no active service creates, consumes or checks them.

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

The funding board and market streams are owned by Gateway. Trading does not
maintain a second market cache or relay browser market subscriptions. See
[Market and reference data](market-reference-data.md).

Collection responses are enveloped as `{"data":{"items":[…]}}`, with a `meta`
object alongside `data`. Single-resource responses put the resource directly in
`data`. A client that reads `data.items` on a single resource, or `data` on a
collection, is reading the wrong shape. The board and the history are single
resources by that rule, not collections: `data` is the snapshot or the series
itself, with its columns under `venues` and its readings under `points`.

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

The offline suite covers the ledger's state machine, manual execution, and the
projections. No test places an order: the external write ladder was deleted
along with the Demo and Testnet environments it ran against. What the offline
suite does and does not prove is in
[Testing and evidence](../reference/testing.md).
