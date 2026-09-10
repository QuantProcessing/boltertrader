# Your first order

> Canonical language: English. [Chinese mirror](../zh-CN/getting-started/first-order.md)

This page walks the full loop in the browser: sign in, register a venue account,
place one resting limit order, cancel it, and confirm the terminal state. It
assumes the stack is already up — see
[Running the local stack](local-stack.md).

**This places a real order on a real venue, with real money.** There is no demo
or testnet environment to fall back on any more. Use a dedicated, deliberately
underfunded account whose credential has no withdrawal rights, and hold no
unrelated order or position in the product scope you are about to touch. The
order below is priced to rest rather than fill, and step 8 cancels it. To see
live public markets without enabling account access, stay in the default
read-only mode. Read [Prerequisites](prerequisites.md) first if you have not.

## 1. Open the interface

```sh
open http://127.0.0.1:18090
```

There is one origin. The frontend and `/api/*` are served by the same Caddy
instance, which reverse-proxies the API to the auth edge. That is why the
session cookie stays `HttpOnly` and `SameSite=Strict` and why no CORS surface
exists.

Any route redirects to `/login` until there is a session.

## 2. Sign in

Use the bootstrap operator — `operator` by default — and the password printed
once by the first `make up`. It is also in `.secrets/stack/bootstrap_password`.

After sign-in the header carries the four routes: **Trade**, **Portfolio**,
**Activity**, **Settings**.

## 3. Register a venue account

Go to **Settings** and choose **Add account**. You supply:

- the venue and an optional display name,
- the venue's credential fields, whose labels come from the gateway rather than
  from the frontend, because they differ per venue.

Credential values are write-only. They are sent once, encrypted into the
gateway's credential store, and never returned — they disappear from the form,
from browser storage, and from every later response.

Which venues and product rows exist is in
[Venue support](../venues/README.md). Which credential fields each venue expects
is in [Configuration](../reference/configuration.md).

## 4. Finish the balance check

Adding an account checks its credential format and queries a venue balance.
On success, Gateway saves the encrypted credentials and connects the account;
zero balance is valid. A failed check returns the venue error in the form.

There is no separate activation, permission update, or credential rotation.
The venue decides whether the key can trade and returns its own error if an
operation lacks permission. To replace credentials, delete the account and add
it again. Deletion removes local credentials and connections only; it does not
cancel orders or close positions. Current positions come directly from Gateway's
venue snapshot and require no historical order migration.

## 5. Open the trade terminal

**Trade** shows, for the selected instrument: the ticker strip, a candlestick
chart, the order book, the order ticket, and the blotter. Venue data arrives
through the shared `/api/v1/realtime/ws` connection; private account and order
subscriptions use the same connection after login.

Pick a supported instrument on the account's venue.

## 6. Review, then place

Fill the ticket: **Limit**, a side, a quantity, and a price far enough from the
touch that the order rests instead of filling. If order confirmation is enabled,
review the selected account, side, quantity, price and notional value locally.

**Confirm** sends one command request. Trading checks the current connection,
route and instrument rules before submitting it to Gateway. Idempotency protects
request retries; an uncertain venue result is resolved by lookup rather than
another submission. See [Execution and risk](../concepts/execution-risk.md).

## 7. Watch it rest

The order appears in the blotter's **Orders** tab with its instrument, side,
type, price, quantity, state, and last update. State transitions arrive on the
same stream.

The identifier shown is the trading service's `commandId`. The gateway keeps its
own durable order identity underneath; the two are separate namespaces on
purpose, and [the trading service guide](../guides/trading-service.md) explains
which one to quote when.

## 8. Cancel it, and confirm the terminal state

Cancel from the **Orders** row. The row moves to a terminal state once the venue
acknowledges — not when the request is accepted.

Then confirm two things:

- The **Orders** tab is empty. Nothing of yours is resting.
- **Activity** shows the full chain for that order: the command, its
  acknowledgement, and its terminal state, each with a timestamp.

Leaving an order resting is the one failure mode this walkthrough can create. If
the row does not reach a terminal state — a request that timed out, a page
closed mid-cancel, a stream that dropped — do not place again. Resolve it first
through [Operations and recovery](../guides/operations-recovery.md).

## What this proved, and what it did not

It proved that one product row of one account can complete a full manual
lifecycle end to end, and that the terminal state was observed rather than
assumed.

It did not prove that the venue is production-ready, that neighbouring product
rows work, or that any other account mode behaves the same way. Those claims
have their own evidence, and
[Testing and evidence](../reference/testing.md) states what establishes
each.

## Next

- [Architecture](../concepts/architecture.md) — the layers you just went
  through, and why the boundaries are where they are.
- [The trading service](../guides/trading-service.md) — the command ledger
  behind the ticket.
- [The exchange gateway](../guides/exchange-gateway.md) — the shared plane
  behind the account.
