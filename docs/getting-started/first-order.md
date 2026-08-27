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

- the venue and the route profiles this account may use,
- the product policy — which product rows this account may trade,
- the venue's credential fields, whose labels come from the gateway rather than
  from the frontend, because they differ per venue.

Credential values are write-only. They are sent once, encrypted into the
gateway's credential store, and never returned — they disappear from the form,
from browser storage, and from every later response.

Which venues and product rows exist is in
[Venue support](../venues/README.md). Which credential fields each venue expects
is in [Configuration](../reference/configuration.md).

## 4. Wait for the account to be ready

The account panel shows the desired-state revision, the runtime generation, and
the credential version. Below them the **Runtime** panel reports two separate
readiness answers from the trading service: **Read** and **Write**.

Use **Validate** to ask the gateway to prove the credential against the venue.
Do not continue until write readiness is true — a blocked account has a reason
attached, and placing into it produces a rejection rather than an order.

Registration is asynchronous by design: the gateway records the desired state,
then a reconciler brings the runtime to it. See
[State and reconciliation](../concepts/state-reconciliation.md).

## 5. Open the trade terminal

**Trade** shows, for the selected instrument: the ticker strip, a candlestick
chart, the order book, the order ticket, and the blotter. Venue data arrives
directly from Gateway at `/api/v1/market/realtime/ws`; account notifications use
the separate authenticated Gateway socket.

Pick an instrument your account's product policy allows.

## 6. Review, then place

Fill the ticket: **Limit**, a side, a quantity, and a price far enough from the
touch that the order rests instead of filling. Then **Review order**.

Review is not cosmetic. The trading service returns a *sealed* preview: the
priced, validated, and reserved intent, signed and given a short expiry. The
confirmation panel counts that expiry down. **Confirm** submits the sealed
preview; it cannot submit anything the preview did not price.

If the seal expires, the button says so and you review again. This is the
mechanism that makes "what you confirmed" and "what was sent" the same object —
see [Execution and risk](../concepts/execution-risk.md).

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
