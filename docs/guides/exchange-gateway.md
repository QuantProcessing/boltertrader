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

BolterTrader exposes three peer paths:

- use direct `exchange` clients for a convenient normalized API inside a Go
  process when the caller owns lifecycle, caching, and durability;
- use Exchange Gateway when a resident process must centralize accounts,
  credentials, Catalog, order state, reconciliation, and private events;
- use `runtime` for one strategy's stateful execution framework.

Gateway is built above `exchange`; it does not use `runtime` or replace either
peer product.

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
Lighter Testnet Spot remains read-only until it proves two-sided liquidity and
exact lookup in its external environment. The exact credential fields differ by
venue and are strictly decoded on the account declaration.

Start the service:

```sh
go run ./cmd/exchange-gateway -config ./gateway.yaml
```

## Resource flow

Resolve instruments from the service before placing an order:

```sh
curl -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  "http://127.0.0.1:8080/v1/instruments?account_id=binance-demo-spot"
```

Place an order with an account, gateway instrument ID, and idempotency key:

```sh
curl -X POST \
  -H "Authorization: Bearer $GATEWAY_AUTH_TOKEN" \
  -H "Content-Type: application/json" \
  -H "Idempotency-Key: strategy-a-10001" \
  http://127.0.0.1:8080/v1/orders \
  -d '{
    "account_id":"binance-demo-spot",
    "instrument_id":"binance:demo:spot:BTC-USDT",
    "idempotency_key":"strategy-a-10001",
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

## Private events

Connect to:

```text
GET /v1/ws?account_id=binance-demo-spot
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

The reference deployment requires Docker Engine with Compose v2. It is a
single-host operational example: one Gateway container, one PostgreSQL
container, and one named PostgreSQL volume. It does not provide Kubernetes,
multi-host scheduling, registry publishing, or high availability.

Copy the public template and create the three referenced files outside the
repository:

```sh
cp docker/gateway.env.example gateway.env
chmod 600 gateway.env ./gateway-secrets/*
```

`gateway.env` contains paths and public settings only. The actual bearer token,
Gateway PostgreSQL DSN, and PostgreSQL password live in those files and enter
the containers as Docker secrets. The DSN used by the reference stack names
the Compose service, for example
`postgres://gateway:<password>@postgres:5432/gateway?sslmode=disable`.
Do not put any secret value in `gateway.env`, the image, a build argument, or
`docker/gateway.docker.yaml`.

The supplied Docker config deliberately starts with `accounts: []`. Add
accounts by copying that config to an operator-owned file. Each new
`env://NAME` credential also needs a matching Compose secret mounted at
`/run/secrets/NAME`; the Docker helper resolves only these explicit names and
then execs Gateway as PID1.

Build and start:

```sh
docker compose --env-file gateway.env config
docker compose --env-file gateway.env up -d --build --wait
```

The host publication is `127.0.0.1:18080` by default. Inside the container,
`allow_non_loopback_listen: true` explicitly permits `0.0.0.0:18080`; ordinary
non-container configs remain loopback-only.

Inspect authenticated health and logs without printing the bearer token:

```sh
docker compose --env-file gateway.env exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind live
docker compose --env-file gateway.env exec -T gateway \
  /usr/local/bin/exchange-gateway-docker-support health \
  -config /etc/boltertrader/gateway.yaml -kind ready
docker compose --env-file gateway.env logs --tail=200 gateway
```

`docker compose stop gateway` sends SIGTERM to Gateway as PID1. The reference
stack allows 15 seconds, exceeding Gateway's 10-second drain budget. For an
upgrade, build the new immutable image, run the Docker verification targets,
then recreate Gateway. Startup migrations and reconciliation run before write
readiness reopens.

PostgreSQL persistence is the named `gateway_postgres_data` volume.
`docker compose down` retains it; `docker compose down -v` deliberately deletes
it. Back up and restore that volume using normal PostgreSQL tooling. The
reference Compose file never publishes PostgreSQL to the host; the random
loopback publication in `docker/compose.test.yaml` is test-only.

For external PostgreSQL, deploy the Gateway image without the reference
PostgreSQL service and point `GATEWAY_POSTGRES_DSN_FILE` at a TLS-enabled DSN
for the external database. Run the migration and restart gates against a
disposable copy before rollout. Keep the database and Gateway network private.

Terminate TLS at a same-host ingress or reverse proxy. The default loopback
publication is suitable for that shape. Publishing Gateway on another
interface is an explicit operator security decision and requires firewall,
TLS, bearer-token rotation, and access-log review. This single-host stack has
no leader election or automatic failover; use only one Gateway owner for each
configured exchange account.

Docker verification:

```sh
make test-gateway-docker-config
make test-gateway-docker-build
make test-gateway-docker-security
make test-gateway-docker-smoke
make test-gateway-docker-persistence
make test-gateway-docker-postgres
make test-gateway-docker
```

## Verification

```sh
make test-gateway-contract
make test-gateway-offline
make test-gateway-race
make test-gateway-security
```

`make test-gateway-demo-testnet` is credentialed and state-changing. It runs the
twenty product rows serially and is never part of the default offline suite.
Lighter Testnet Spot may be one-sided; its explicit certification waiver
validates reads and invalid-command barriers, performs zero valid order
mutations, and does not mark that row write-ready.
