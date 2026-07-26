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
REST + private WebSocket + /metrics
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

TLS should terminate at the deployment ingress. The bearer token and every
venue credential are resolved from explicit secret references; secret values
must not appear in the YAML file.

## Configuration

Run one statically configured account entry per venue/product account. The
service models twenty V1 rows across Binance, OKX, Lighter, Hyperliquid, Bybit,
Bitget, Gate, Aster, and Nado. Nineteen rows are write-ready by default;
Lighter Testnet Spot remains read-only until it proves two-sided liquidity and
exact lookup in its external environment.

```yaml
server:
  listen: 127.0.0.1:8080
  auth_token_ref: env://GATEWAY_AUTH_TOKEN
postgres:
  dsn_ref: env://GATEWAY_POSTGRES_DSN
accounts:
  - account_id: binance-demo-spot
    venue: binance
    product: spot
    environment: demo
    credentials:
      api_key_ref: env://BINANCE_DEMO_API_KEY
      api_secret_ref: env://BINANCE_DEMO_API_SECRET
```

The exact credential fields differ by venue and are strictly decoded. Unknown
fields, duplicate account IDs, invalid product/environment combinations, or
literal secret values prevent startup before the listener opens.

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
`/v1/positions`, `/v1/funding-rates`, `/v1/accounts`, `/v1/capabilities`,
`/v1/rules`, and `/v1/risk-limits`. The complete contract is
[OpenAPI 3.1](../../api/exchange-gateway/v1/openapi.yaml).

Gateway V1 does not execute leverage mutations. The stable leverage route
returns `501` and records a safe unsupported audit event until leverage changes
have the same durable idempotency and restart-reconciliation model as orders.
The underlying `exchange.PerpClient` capability remains available to direct
driver users.

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
