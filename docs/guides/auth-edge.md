# The auth edge

> Canonical language: English. [Chinese mirror](../zh-CN/guides/auth-edge.md)

The auth edge is the only service a browser talks to. It terminates the session
and nothing else: it holds no domain state, owns no tables beyond its own
sessions, and makes no trading decision. Everything it forwards, it forwards
unchanged.

Its job is a translation. On the inbound side a request is authenticated by a
cookie the browser sends automatically. On the outbound side it is authenticated
by a service token and an attested principal. That translation is the security
boundary, and it runs in one direction only — a header the browser supplies can
never become the header an upstream trusts.

## What it handles itself

Four routes never leave the process:

| Route | Effect |
| --- | --- |
| `POST /api/v1/auth/login` | Verifies the credential, issues a session, sets the cookie |
| `GET /api/v1/auth/session` | Returns the current principal and session |
| `GET /api/v1/me` | The same answer, under the name the frontend uses |
| `DELETE /api/v1/auth/session` | Revokes the session and expires the cookie |

The session cookie is `bt_session`, with `HttpOnly`, `SameSite=Strict`, and
`Path=/`. It is marked `Secure` when the deployment is served over TLS. The
session token itself is never serialized into a response body — it exists only
in the cookie.

Login returns a separate `csrfToken` in the body. Because the cookie is
`HttpOnly`, script cannot read the session token, so the CSRF token is the value
the frontend echoes back in `X-CSRF-Token`. Every mutating forward requires it;
a missing or wrong one is `403` with code `CSRF_REQUIRED`, before anything is
forwarded.

Failed authentication is always `401` with code `AUTH_REQUIRED` and the same
message, whether the username was unknown, the password was wrong, or the
session had expired. The distinction is not the caller's business.

## What it forwards

Everything else is a table lookup. The table is explicit — one entry per method
and path — rather than a prefix rule, and that is deliberate.

Routes are declared explicitly and sent to the service that owns the data:

| Prefix | Upstream | Notes |
| --- | --- | --- |
| `/api/v1/accounts`, `/api/v1/accounts/{accountId}` and its actions | Gateway | Rewritten onto the internal plane |
| `/api/v1/accounts/{accountId}/runtimes` | Trading | A projection, not the account itself |
| `/api/v1/catalog/*`, `/api/v1/market-data`, `/api/v1/candles`, funding reads | Gateway | Shared application contracts on the internal plane |
| `/api/v1/market/realtime/ws` | Gateway | Public venue market stream |
| `/api/v1/account/realtime/ws` | Gateway | Session-required private account notifications |
| `/api/v1/venues/connectivity` | Gateway | REST snapshot; live updates use the Gateway market socket |
| `/api/v1/commands`, `/api/v1/reservations`, `/api/v1/products/manual/*` | Trading | |
| `/api/v1/positions`, `/api/v1/ledger`, `/api/v1/pnl`, `/api/v1/fills`, `/api/v1/activity` | Trading | |
| `/api/v1/system/status`, `/api/v1/system/readiness`, `/api/v1/audit` | Trading | |

The one line worth staring at is the pair in the middle.
`/api/v1/accounts/{accountId}/runtimes` is a trading-service projection, while
every other `/api/v1/accounts` route is the gateway's account lifecycle —
including the ones that carry venue credentials. A prefix rule that guessed
wrong would send credential material to the wrong service, so the table names
each route instead of deriving it.

The gateway routes are also the only ones whose path changes. `/api/v1/accounts`
becomes `/internal/v1/accounts` on the way out: account lifecycle lives on the
gateway's authenticated internal plane, not on the resource plane the trading
service reads. See [the exchange gateway](exchange-gateway.md).

## What crosses, and what does not

Three header sets define the boundary.

**Stripped from the inbound request, always.** `Authorization`,
`X-Bolter-Principal`, `X-Bolter-Principal-Mode`, `X-Bolter-Mode`, and `Cookie`.
These are exactly the headers the edge is about to assert, so a browser that
sends them must not be able to influence them. Stripping is unconditional — it
does not depend on the value looking wrong.

The two mode headers are on that list even though nothing asserts them any more.
They were how each upstream learned which mode a request belonged to, back when
there were two; a request arriving with one today is someone probing, and it must
not survive to be read as authority if an upstream ever grows the name back.

**Copied through, by allowlist.** `Accept`, `Content-Type`, `Idempotency-Key`,
`If-Match`, `Last-Event-ID`, and `X-Request-Id`. Nothing else. An allowlist
rather than a denylist, because a header nobody thought about should not reach
an upstream by default.

**Injected on the way out.** `Authorization: Bearer <service token>`,
`X-Bolter-Principal` with the authenticated principal's ID, and `X-Request-Id`
when the caller did not supply one.

On the way back, hop-by-hop headers are removed, and so is `Set-Cookie`. No
upstream is allowed to set a cookie in the browser. Only the edge does that, and
only for the session.

## Realtime WebSocket

The edge relays two Gateway WebSocket streams without applying the
ordinary request deadline: public venue data goes directly to Gateway at
`/api/v1/market/realtime/ws`, private account notifications go directly to
Gateway at `/api/v1/account/realtime/ws`. The account socket requires a browser
session; the market socket may be anonymous.

## What it does not do

It does not aggregate. One browser request becomes at most one upstream request,
and the upstream's response body is returned as it came. There is no place in
the edge where two services' answers are merged, because that would put product
logic in the one process that must stay dumb enough to be trusted with the
session.

It does not authorize beyond the session. Whether a principal may touch a
particular account or product is decided by the service that owns it, using the
principal the edge attested.

## Verify it

```sh
curl -i http://127.0.0.1:18091/api/v1/me
```

Without a session this must be `401`. `make doctor` runs exactly this check, and
treats any other answer as a failure — a `200` here would mean the edge was
answering for a principal it never authenticated.

Configuration variables for the service tokens and upstream addresses are in
[Configuration](../reference/configuration.md). How the whole stack fits together
is in [Architecture](../concepts/architecture.md).
