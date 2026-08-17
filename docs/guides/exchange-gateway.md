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
ledger and calls these resources, and it is the only kind of caller Gateway is
designed for. See [the trading service](trading-service.md).

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
- authenticated account-scoped public market events at `/v1/public/ws`;
- authenticated private events at `/v1/ws`;
- authenticated Prometheus metrics at `/metrics`;
- health at `/v1/health/live` and readiness at `/v1/health/ready`.

TLS should terminate at the deployment ingress. The bearer token and every
venue credential are resolved from explicit secret references; secret values
must not appear in the YAML file.

## Configuration

The YAML file configures the process only: listeners, the bearer token
reference, and the PostgreSQL DSN reference. Accounts are durable state rather
than file state. They are declared through the authenticated internal listener
at `PUT /internal/v1/accounts/desired/{accountId}/operations/{operationId}` and
stored in PostgreSQL, so a restart reloads them without editing any file.

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

Place an order with an account, gateway instrument ID, and idempotency key:

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: manual-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-spot",
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

Public market reads still require the Gateway bearer token plus an
`account_id` and canonical `instrument_id`. The account selects the
Gateway-owned venue client and binds the request to its configured
product/environment; callers never provide or receive venue credentials,
native symbols, native IDs, instrument encodings, or raw venue errors.

`/v1/public/ws` accepts one of `order_book`, `bbo`, `mark_price`, or
`funding_rate`. The Gateway subscribes to the venue stream before establishing
the authoritative snapshot, then sends that snapshot as sequence 1. Order book,
BBO, and funding use normalized REST snapshots; mark price uses the first
bounded mark stream event because the exchange contract exposes mark as an
independent stream capability rather than coupling it to funding. This stream
deliberately has no cursor replay: every connection has a boot ID and starts
from a fresh snapshot. A venue gap, malformed update, oversized payload, or
upstream error emits a bounded `resync_required` event and closes with
WebSocket policy violation; the client reconnects and receives a new snapshot.

Two reads on the internal listener are the exception to the account rule.
`GET /internal/v1/funding-board` and `GET /internal/v1/funding-history` take no
`account_id`, because the gateway answers them from venue clients it builds
without credentials. They resolve on a deployment where no account has been
declared yet, which is what makes a funding matrix readable on a fresh install.
They are still authenticated — the internal listener has its own bearer token —
and they still refuse to emit a native symbol, a native ID, or a raw venue
error.

The board is the whole table for the gateway's mode in one response: one column
per venue, product, and settlement asset, each carrying its rates, its
settlement interval, and whether that venue publishes history at all. It takes
no parameters, because there is nothing a caller could ask for that would make
one column right and another wrong. A column that cannot be read degrades to an
unavailable column with a reason rather than failing the response — one
unreachable venue must not blank out the other nine.

The history read takes a venue, an environment, a symbol, and an explicit
`since`/`until` pair, and returns one series of funding points with the
settlement interval measured between them. The canonical symbol is resolved to
the venue's native identifier out of the board's own cached column, so the
native form never crosses the boundary here either. A venue with no history
surface answers with a code that says so rather than an empty series, and a
symbol that is not on that column answers with a different one: "this venue
keeps no history" and "this venue does not list this instrument" are different
facts, and a caller that cannot tell them apart will retry the wrong one. The
bound is the caller's to choose and the caller's to keep sane — the trading
service admits two fixed windows precisely so that one read is one venue call
with no pagination.

## Private events

Connect to:

```text
GET /v1/ws?account_id=binance-spot
Authorization: Bearer <token>
```

Events carry a process boot ID, per-account sequence, event ID, resource
version, safe resource payload, and kind. Orders, fills, balances, positions,
account health, and `resync_required` are normalized. A stale boot cursor,
evicted replay cursor, stream gap, or process restart emits
`resync_required`; clients must fetch REST snapshots before resuming.

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
   uncertain commands;
6. establishes private streams;
7. opens write readiness only after all account gates are proven.

If a venue accepted an order before the process crashed, the original portable
client order ID and venue correlation are reused for exact lookup. A generic
write retry is forbidden.

## Metrics, logs, and audit

`/metrics` uses a private Prometheus registry. It reports bounded account
read/write readiness, command outcomes, uncertain work and reconciliation age,
stream gaps/resync/evictions, Catalog age/version, PostgreSQL latency and safe
error class, in-flight commands, and graceful shutdown outcomes.

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

That generates the stack's credentials under `.secrets/test/` and its variables
into `.env.stack`, brings every service up, and then runs `make doctor`. It is
idempotent: a second run reuses the existing credentials. `make doctor` reports
what is wrong when something is, and `make logs` tails every service.

Nothing needs to be copied by hand and no secret value belongs in `.env.stack`,
the image, a build argument, or `docker/gateway.docker.yaml`. The bearer token
and the Gateway PostgreSQL DSN live in files under `.secrets/test/` at 0600, and
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
