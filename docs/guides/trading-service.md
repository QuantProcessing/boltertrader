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

The arbitrage product has two strategy families rather than treating every
ticker alike. **Crypto Continuous** offers spot-perpetual basis and cross-venue
perpetual funding. **TradFi Perpetual** offers perpetual funding and cross-venue
same-asset perpetual price spread. Asset class comes from the normalized public
catalog; a ticker name is never guessed into crypto, equity, or commodity.
An exact canonical spot/perpetual pair may enter the basis workspace while
retaining `assetClass=unknown`; the topology identifies the strategy without
inventing an economic classification.

Rates are not comparable as published. Venues settle funding on their own
schedule — four hours here, eight there — so a raw rate says nothing until it is
normalized. Every rate is converted to a daily figure (`rate × 24 ÷
intervalHours`) before any two are compared, the spread is `shortDaily −
longDaily`, and the headline APR is that spread times 365. A venue that will not
say what its quoted period is remains visible on the funding board but is not
emitted as an arbitrage opportunity; the service never assumes eight hours.

`GET /api/v1/products/arbitrage/opportunities` is a public read and ranks purely
from Trading's local catalog and funding-market projection. Anonymous callers
see every public opportunity; only authenticated callers may add `accountId`
parameters to calculate execution reachability. Each item carries blockers.
Funding strategies require a fresh rate and published interval; basis and price
spread require fresh prices; OI is display and ranking context and never blocks
execution. Listing is determined by the public instrument directory, while
account reachability is a separate execution fact; one can never be inferred
from the other.

Placement reuses the manual flow's shape exactly.
`POST /api/v1/products/arbitrage/execution-previews` returns a sealed preview —
the same seal, the same expiry — except that it reads a fresh BBO for both legs
directly through Gateway and reserves against both accounts. The opportunity
projection is deliberately not trusted for executable prices. Either BBO being
unavailable or older than two seconds rejects the preview. It also carries
`warnings`, which are not blockers and do not stop a confirm: `fees_excluded` says the quoted spread is gross,
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
- `/api/v1/realtime/ws` — the single multiplexed WebSocket the interface holds
  open.

The funding board and opportunities are resident projections. Trading consumes
Gateway's single internal market WebSocket: the first message for every topic
is a snapshot, later deltas are applied by generation and sequence, and a gap
requires a new snapshot. Browser subscribers receive merged board and
opportunity updates at most once per second and never trigger a venue request.

`/api/v1/funding-history` is intentionally different: it is an on-demand read
from Gateway's credential-free public plane for one instrument and a bounded
window, `7d` or `30d`. Historical candles and funding are not preheated or
persisted. A venue with no history surface is reported unavailable rather than
empty. See [Market and reference data](market-reference-data.md).

Collection responses are enveloped as `{"data":{"items":[…]}}`, with a `meta`
object alongside `data`. Single-resource responses put the resource directly in
`data`. A client that reads `data.items` on a single resource, or `data` on a
collection, is reading the wrong shape. The board and the history are single
resources by that rule, not collections: `data` is the snapshot or the series
itself, with its columns under `venues` and its readings under `points`. A board
read has no venue-rate budget because it is served entirely from memory.

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
