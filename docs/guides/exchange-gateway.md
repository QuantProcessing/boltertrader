# Exchange Gateway

Exchange Gateway is a self-hosted, long-running Go service over the `exchange`
drivers. It owns venue clients, credentials, the instrument Catalog, durable
order identity, restart reconciliation, private event delivery, and operational
state. Callers use stable gateway resources and do not construct venue SDK
requests.

V1 is a trading gateway, not a clearing system. It provides no shared margin,
custody, smart order routing, fund transfer, deposit, or withdrawal surface.
There is no Redis dependency and no gRPC listener. PostgreSQL is the only
required durable service.

## Choosing this product

BolterTrader exposes two peer paths over the venues:

- use direct `exchange` clients for a convenient normalized API inside a Go
  process when the caller owns lifecycle, caching, and durability;
- use Exchange Gateway when a resident process must centralize accounts,
  credentials, Catalog, order state, reconciliation, and private events.

Gateway is built above `exchange` and does not replace it. Product services sit
above Gateway rather than beside it: the trading service holds its own command
ledger and calls these resources. Personal scripts, monitoring and alerting apps
can use the [independent Gateway Go client](gateway-client.md). Migration of the
remaining internal market paths is ongoing. See [the trading service](trading-service.md).

## Service shape

```text
REST + public/private WebSocket + /metrics
                      |
               Exchange Gateway
             /        |          \
        Catalog   Order engine   Event hub
             \        |          /
             Account/client manager
                      |
                 exchange clients
```

The process exposes:

- authenticated versioned REST under `/v1`;
- authenticated private events at `/v1/ws`;
- authenticated Prometheus metrics at `/metrics`;
- health at `/v1/health/live` and readiness at `/v1/health/ready`.

TLS should terminate at the deployment ingress. Service tokens are resolved from explicit secret references. Exchange
credentials are entered after login and encrypted in PostgreSQL; neither belongs
in the YAML file.

## Configuration

The YAML file configures the process only: listeners, the bearer token
reference, and the PostgreSQL DSN reference. Accounts are durable state rather
than file state. Add them after login through Settings. The authenticated internal listener
accepts `POST /internal/v1/accounts` with venue, displayName and credentialBundle.
One successful balance query validates the connection; it then saves encrypted
credentials and returns 201. GET lists or views saved accounts; DELETE removes
only local credentials and connections, leaving venue orders and positions intact.
There are no disable, rotation, permission-update or desired-state APIs.

The exchange decides API permissions and its safe error message is returned
when an operation is rejected. Restart restores saved connections; current
positions come from the exchange, without importing historical orders.
Existing installations must add their credentials again: legacy account records
are not imported. Migration 012 creates the new registration table.

Set `BT_SECRET_MASTER_KEY_FILE` to an absolute path containing the existing
32-byte master key encoded as 64 hexadecimal characters (mode 0600). The file must remain private and persistent. This key encrypts
account records; it is not a second user authorization step. See
[simple accounts](../design/simple-accounts-2026-09-10.md) for the exact boundary.

```yaml
server:
  listen: 127.0.0.1:8080
  auth_token_ref: env://GATEWAY_AUTH_TOKEN
  internal_listen: 127.0.0.1:8081
  internal_auth_token_ref: env://GATEWAY_INTERNAL_AUTH_TOKEN
postgres:
  dsn_ref: env://GATEWAY_POSTGRES_DSN
```

The file is strictly decoded. An unknown field, a non-loopback listener without
`allow_non_loopback_listen`, a non-loopback internal listener, or a literal
secret value prevents startup before the listener opens.

V1 models twenty venue/product rows across Binance, OKX, Lighter, Hyperliquid,
Bybit, Bitget, Gate, Aster, and Nado. Nineteen rows are write-ready by default;
Lighter Spot remains read-only until it proves two-sided liquidity and exact
lookup against the live book. The exact credential fields differ by venue and
are strictly decoded on the account declaration.

Start the service:

```sh
go run ./cmd/exchange-gateway -config ./gateway.yaml
```

## Resource flow

Resolve instruments from the service before placing an order:

```sh
curl -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  "http://127.0.0.1:8080/v1/instruments?account_id=binance-spot"
```

Place an order with an account, route, gateway instrument ID, idempotency key
and the current account `runtime_id`. Replace the runtime placeholder below
with the value returned by `/v1/accounts/{account_id}`:

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: manual-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-spot",
    "route_id":"spot",
    "runtime_id":"REPLACE_WITH_CURRENT_ACCOUNT_RUNTIME_ID",
    "instrument_id":"binance:live:spot:BTC-USDT",
    "idempotency_key":"manual-10001",
    "side":"buy",
    "type":"limit",
    "quantity":"0.001",
    "limit_price":"50000",
    "limit_policy":"resting"
  }'
```

The gateway commits command intent before the first venue write. Reusing the
same idempotency key with the same canonical payload replays the same command;
reusing it with a different payload returns a conflict. A post-handoff timeout
becomes `uncertain`. The gateway never blindly resends it.

`runtime_id` is the placement ownership fence; the old three revision counters
are no longer accepted. Cancellation uses `DELETE /v1/orders/{gateway_order_id}`
without a body, routing from the stored order. Amendment still returns `501`.
Once acceptance has been confirmed, repeated lookup misses leave the order
awaiting reconciliation; absence alone cannot turn that order into a rejection.

The venue-native `fill.fill_id` is scoped by `account_id` and `instrument_id`.
Consumers must include all three fields when deduplicating fills.

Queries include `/v1/orders`, `/v1/open-orders`, `/v1/fills`, `/v1/balances`,
`/v1/positions`, `/v1/funding-rates`, `/v1/funding-rates/history`,
`/v1/order-book`, `/v1/bbo`, `/v1/mark-price`, `/v1/accounts`,
`/v1/capabilities`, `/v1/rules`, and `/v1/risk-limits`. The complete contract is
[OpenAPI 3.1](../../api/exchange-gateway/v1/openapi.yaml).

`/v1/fills` accepts an optional `gateway_order_id` for exact order fills. The
gateway first verifies the caller owns that Gateway order, then maps it to the
internal venue order id and filters venue fills without exposing the venue id.
If `account_id` or `instrument_id` is also supplied, it must match the stored
order identity. `next_cursor` is emitted only when the venue page proves a known
continuation.

Gateway V1 does not execute leverage mutations. The stable leverage route
returns `501` and records a safe unsupported audit event until leverage changes
have the same durable idempotency and restart-reconciliation model as orders.
The underlying `exchange.PerpClient` capability remains available to direct
driver users.

## Public market data

Account-independent application APIs are available under `/v1/market`:

- `GET /v1/market/instruments` returns the public catalog and explicit coverage.
- `GET /v1/market/candles` reads bounded history by `instrument_id` and `interval`.
- `GET /v1/market/snapshot` reads one complete shared topic snapshot.
- `GET /v1/market/ws` subscribes to order book, BBO, candles and mark price.

They require the existing Gateway Bearer token, but no exchange account or venue
credentials. The token remains a full Gateway credential; this batch does not
introduce a read-only token. Unknown or unavailable market scopes remain explicit.
`complete=false` is a catalog coverage/freshness signal, not a statement about
whether live quotes are arriving. Known instruments remain readable from a stale
directory. See the [application market API guide](../design/market-api-2026-09-10.md)
for requests, stream recovery, supported scope and examples.

The older `/v1/order-book`, `/v1/bbo`, `/v1/mark-price`, `/v1/candles` and funding-rate
routes retain their account-scoped contracts for existing callers. New market-only
applications should use `/v1/market`.

`/internal/v1/market/ws` is Gateway's multiplexed service-to-service market
protocol. Its public topics include order book, BBO, candles, mark, funding
board, and venue connectivity. The production capability matrix selects each
dataset's transport, native-family boundary, cadence, and rate-limit operation.
Gateway owns exactly one physical `VenueFeedOwner` per venue, environment, and
native market family; route profiles are output projections, not additional
connections. Market Hub caches latest-value facts such as BBO and mark, but it
does not materialize a full order book. The Exchange adapter bootstraps depth
from WebSocket only, validates native continuity, and keeps only the bounded
visible snapshot needed to seed a late logical consumer of the shared topic.
Gateway then relays `snapshot`, `delta`, or self-contained `full` events. No
REST depth is joined to a WS sequence; a gap emits `resync_required`, reopens
the native stream, and starts a new generation. A venue whose protocol cannot
produce an initial WS book (currently Nado) is explicitly unsupported for
depth rather than silently falling back to REST.

Open interest remains venue-attributable. Hyperliquid and Lighter provide it
through their all-market streams, while Bybit, Bitget, Gate, OKX, and Nado use
their native batch surfaces. Binance and Aster expose only per-symbol current
OI, so the broad feed leaves OI absent instead of scanning the whole catalog
and risking IP rate limits. OI from another venue is never relabeled as
Binance/Aster OI.

Hot order-book, BBO, candle, and mark topics share one pooled native socket per
feed owner. A reference-counted hotset ranks position, open-order, execution,
browser and funding-history reasons in that order. Position,
open-order, and execution topics cannot be evicted by venue topic limits; the
last reason retains its topic for a 30-second grace period. Broad polling and
catalog refresh share one rate-limit coordinator and singleflight key per
venue operation. FreshBBO joins the same retained BBO WebSocket in Market Hub;
it never depends on a browser subscription or a REST quote fallback.

The internal listener also retains public market operations for existing service consumers: `/internal/v1/market/ws`, `/internal/v1/market-facts`,
`/internal/v1/funding-board`, `/internal/v1/funding-history`, and
`/internal/v1/venues/connectivity`. They take no `account_id`, because Gateway
answers them with credential-free venue clients. They remain authenticated by
the internal bearer token and never expose credential material or raw venue
errors.

Auth Edge exposes `/internal/v1/market/ws` to browsers as the anonymous-capable
`/api/v1/market/realtime/ws`. Private exchange account notifications are
exposed separately as `/api/v1/account/realtime/ws`; they are live change
notifications, while REST snapshots remain authoritative after startup or a
reconnect. Trading may consume both streams but is no longer a browser relay.
Browser sockets send an application ping every 20 seconds and require a pong
within 10 seconds, so a half-open browser-to-Gateway connection cannot remain
visibly online. Native venue clients independently enforce read-idle deadlines;
their heartbeat or read failure closes the native socket and enters the owner's
bounded exponential reconnect loop.

The funding board is a pure projection of resident `DerivativeFact` data. At
startup Gateway bootstraps every supported native perpetual family once and
then maintains funding, mark, index, and open-interest facts using the matrix's
all-market WebSocket or bounded batch-REST strategy. Wide and hot patches merge
by independent field clocks, so a late funding update cannot overwrite a newer
mark. Binance and Aster open interest is enriched asynchronously on a rolling
one-minute cycle; Nado's non-streamed price and OI fields are enriched by one
bounded all-product read. Hot Binance/Aster instruments raise OI cadence to 15
seconds. Bybit and Bitget preserve mark, index, funding, and OI from their one
native ticker; OKX combines the four native reference topics behind one logical
hot reference stream. Gate updates its live `futures.tickers` payload when the
catalog membership changes without replacing the owner or generation. An HTTP board read only copies memory and therefore
cannot increase venue request volume. No order book is stored in this
projection.

The internal board WebSocket sends a complete `snapshot` first and coalesced
`upsert` events afterwards. Each event has a monotonic sequence. A slow consumer
or projection gap receives `resync_required` and is closed; reconnecting always
starts with a new complete snapshot. Every column reports `live`, `degraded`, or
`unavailable`, its last success time, and reconnect count. Funding older than 60
seconds and prices older than 15 seconds are marked delayed; a field older than
two minutes is omitted rather than rendered as current. OI may be absent without
making the funding board unavailable.

The history read remains request-driven. It takes a venue, an environment, a symbol, and an explicit
`since`/`until` pair, and returns one series of funding points with the
settlement interval measured between them. The canonical symbol is resolved to
the venue's native identifier out of the resident board column, so the
native form never crosses the boundary here either. A venue with no history
surface answers with a code that says so rather than an empty series, and a
symbol that is not on that column answers with a different one: "this venue
keeps no history" and "this venue does not list this instrument" are different
facts, and a caller that cannot tell them apart will retry the wrong one. The
bound is the caller's to choose and the caller's to keep sane — the trading
service admits two fixed windows precisely so that one read is one venue call
with no pagination. Historical candles and funding are neither preheated nor
persisted by the board collector.

## Private events

Connect to:

```text
GET /v1/ws?account_id=binance-spot
Authorization: Bearer <token>
```

Each account route owns one product/account-level native stream regardless of
catalog size. Orders, fills, balances, and positions are normalized with a
stable entity ID, resource version, and source event time. Consumers merge by
entity identity and source time; private events have no sequence, generation,
gap phase, or snapshot barrier.

The internal `/v1/ws` endpoint is retained for server projections. The browser
uses Gateway's live-only `/internal/v1/account/ws` through Auth Edge, so a
reconnect never replays an unbounded browser backlog. A route publishes only
`connected` or `disconnected` connection facts and reconnects with exponential
backoff and jitter. Connection state is presentation-only: it does not suspend
order submission, change position freshness, or trigger REST. The periodic
reconciliation loop repairs missed state every 60 seconds.

## Recovery and reconciliation

PostgreSQL stores idempotency reservations, command handoff state, normalized
orders, durable events, exact venue correlations, reconciliation tasks, and
audit records. On startup the service:

1. validates the config and secret references;
2. connects to PostgreSQL and runs migration verification under an advisory
   lock;
3. acquires per-account ownership;
4. constructs long-lived exchange clients and loads Catalog snapshots;
5. performs exact read-only reconciliation for prepared, dispatching, or
   uncertain commands, then repeats it every 60 seconds;
6. establishes private streams;
7. derives write readiness from account and route capability, independently of
   private-stream connectivity.

If a venue accepted an order before the process crashed, the original portable
client order ID and venue correlation are reused for exact lookup. A generic
write retry is forbidden.

## Metrics, logs, and audit

`/metrics` uses a private Prometheus registry. In addition to bounded account
read/write readiness, commands, reconciliation, Catalog, PostgreSQL, and
shutdown metrics, it reports venue-probe state/latency/failures, market-feed
state/reconnects/gaps/resnapshots, field age/coverage, hot-subscription counts,
and REST outcomes by venue and operation. Connectivity probes are independent
of market-feed health and field freshness.

Labels contain only fixed venue/product/environment, operation, outcome, and
safe error-class vocabularies. Account IDs, instrument IDs, idempotency keys,
order IDs, native messages, and credential-derived values are never labels.

Process logs are structured JSON with bounded event/code values and an optional
validated correlation ID. PostgreSQL audit rows contain principal, operation,
gateway resource, safe outcome, correlation, and timestamp without request
payloads, signatures, or credentials.

## Migration and graceful shutdown

Migration is forward-only and embedded in the binary. An unsupported newer
schema or checksum mismatch blocks startup. Test every release against a
disposable PostgreSQL instance before deployment:

```sh
GATEWAY_TEST_POSTGRES_DSN='postgres://...' make test-gateway-postgres
GATEWAY_TEST_POSTGRES_DSN='postgres://...' make test-gateway-restart
```

On SIGINT or SIGTERM, graceful shutdown stops new writes, drains/persists
in-flight command identity, closes private streams, closes exchange clients,
releases account ownership, then closes PostgreSQL. A timeout is a failed drain
and the next process must complete reconciliation before reopening writes.

## Docker single-host deployment

The local stack requires Docker Engine with Compose v2. It is a single-host
operational example: PostgreSQL, three one-shot migrators, and the four
services, with one named PostgreSQL volume. It does not provide Kubernetes,
multi-host scheduling, registry publishing, or high availability.

Bring it up with:

```sh
make up
```

That generates the stack's credentials under `.secrets/stack/` and its variables
into `.env.stack`, brings every service up, and then runs `make doctor`. It is
idempotent: a second run reuses the existing credentials. `make doctor` reports
what is wrong when something is, and `make logs` tails every service.

Nothing needs to be copied by hand and no secret value belongs in `.env.stack`,
the image, a build argument, or `docker/gateway.docker.yaml`. The bearer token
and the Gateway PostgreSQL DSN live in files under `.secrets/stack/` at 0600, and
`.env.stack` holds only the paths that point at them. Both files reach Gateway as
Docker secrets at `/run/secrets/GATEWAY_AUTH_TOKEN` and
`/run/secrets/GATEWAY_POSTGRES_DSN`.

Every Compose invocation in the Makefile passes `--env-file .env.stack`
explicitly, including the ones that only read metadata. Without it Compose
auto-loads the repository's `./.env`, which holds live venue API credentials that
would then be interpolated into the stack's environment.

`docker/gateway.docker.yaml` deliberately starts with `accounts: []`. Add
accounts by copying it to an operator-owned file and pointing
`GATEWAY_CONFIG_FILE` at the copy. Each new `env://NAME` credential also needs a
matching Compose secret mounted at `/run/secrets/NAME`; the Docker helper
resolves only these explicit names and then execs Gateway as PID1.

The host publication is `127.0.0.1:18080` by default. Inside the container,
`allow_non_loopback_listen: true` explicitly permits `0.0.0.0:18080`; ordinary
non-container configs remain loopback-only.

Inspect authenticated health without printing the bearer token:

```sh
docker compose --env-file .env.stack -f compose.yaml exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind live
docker compose --env-file .env.stack -f compose.yaml exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind ready
```

Stopping a service sends SIGTERM to it as PID1. The stack allows 15 seconds,
exceeding Gateway's 10-second drain budget. For an upgrade, build the new
immutable image, run the container gates, then recreate the service. Startup
migrations and reconciliation run before write readiness reopens.

PostgreSQL persistence is the named `postgres_data` volume. `make down` retains
it; `make down-hard` deliberately deletes it. Back up and restore that volume
using normal PostgreSQL tooling.

`down-hard` is also the only way an edit to the initdb script takes effect: the
PostgreSQL image runs those scripts once, when the data directory is empty, so a
surviving volume means a changed role set is never applied.

For external PostgreSQL, deploy the image without the bundled PostgreSQL service
and point `GATEWAY_POSTGRES_DSN_FILE` at a TLS-enabled DSN for the external
database. Run the migration and restart gates against a disposable copy before
rollout. Keep the database and Gateway network private.

Terminate TLS at a same-host ingress or reverse proxy. The default loopback
publication is suitable for that shape. Publishing Gateway on another
interface is an explicit operator security decision and requires firewall,
TLS, bearer-token rotation, and access-log review. This single-host stack has
no leader election or automatic failover; use only one Gateway owner for each
configured exchange account.

Container verification. Each of these builds its own throwaway Compose project
with generated credentials and kernel-assigned ports, so none needs `make up` to
have run and none disturbs a stack that is already up:

```sh
make test-compose-config
make test-compose-build
make test-compose-security
make test-compose-smoke
make test-compose-persistence
make test-compose-postgres
make test-compose
```

## Verification

```sh
make test-gateway-contract
make test-gateway-offline
make test-gateway-race
make test-gateway-security
```

A fifth target, `make test-gateway-demo-testnet`, used to sit under these. It
was credentialed and state-changing, ran the twenty product rows serially
against each venue's demo or testnet environment, and was never part of the
default offline suite. It was deleted along with those environments, and
nothing replaced it, because the same run against live would place real orders.
The four targets above reach no venue, so none of them can establish that a row
is write-ready; that claim needs the evidence named in
[Adding a venue](../contributing/venues.md).
