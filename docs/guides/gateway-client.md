# Gateway Go client

> English is canonical. [简体中文](../zh-CN/guides/gateway-client.md)

`clients/gateway` lets a Go application consume Gateway without importing Trading,
HTTP handlers, exchange adapters or venue SDKs. The shared wire types live beside
the OpenAPI documents in `api/exchange-gateway/v1`.

Public market data needs a Gateway service token but no exchange account or venue
credentials. Keep using the existing browser login and Settings page to add accounts.

## Read-only example

Start Gateway using the [service guide](exchange-gateway.md). Supply its capability
listener token through `GATEWAY_TOKEN`, then run:

```sh
go run ./cmd/gateway-market -gateway http://127.0.0.1:18080
```

This lists the public catalog. Copy an `instrument_id` from that response:

```sh
go run ./cmd/gateway-market -instrument 'binance:live:spot:BTC-USDT'
go run ./cmd/gateway-market -instrument 'binance:live:spot:BTC-USDT' -stream -duration 1m
```

The example only reads catalog, BBO snapshots and market subscriptions. Every run
is bounded by `-duration` (30 seconds by default), and Ctrl-C closes subscriptions.
It never loads venue credentials or submits an order. Catalog availability depends
on the running Gateway's public venue connections.

For a persistent application using these same subscriptions, see the independent
[price and data-gap monitor](gateway-monitor.md).

## Embed in an application

```go
import gateway "github.com/QuantProcessing/boltertrader/clients/gateway"

client, err := gateway.New(gateway.Config{
    URL:   "http://127.0.0.1:18080",
    Token: gatewayToken,
})
if err != nil { return err }

snapshot, err := client.MarketSnapshot(ctx, gateway.MarketTopic{
    Topic:        gateway.BestBidOffer,
    InstrumentID: instrumentID,
})
if err != nil { return err }
quote, err := gateway.DecodeBBO(snapshot.Snapshot)
```

Prices and quantities remain decimal strings, including BBO levels and fees. Use
an arbitrary precision decimal library for arithmetic. Instrument IDs are opaque;
do not reconstruct them from native symbols or unescape embedded `%3A` segments.
The client encodes them for transport.

The implemented methods are:

| Methods | Contract |
| --- | --- |
| `Accounts`, `Account`, `AccountConnection` | Registered accounts, routing and current local connection state |
| `Balances`, `PositionSnapshot` | Current account amounts and explicit position freshness |
| `PlaceOrder`, `Order`, `OrderByKey`, `CancelOrder`, `Orders` | Durable order identity, scoped reads and explicit writes |
| `Fills` | Complete bounded traversal for one account/instrument/order; fees retain their sign and native currency |
| `MarketCatalog`, `MarketCandles`, `MarketSnapshot` | Public directory, bounded candle history and shared snapshots |
| `SubscribeMarkets` | Order book, BBO, candles and mark price on one multiplexed socket |

Account directory and connection reads currently use `/internal/v1`. For a split
Gateway deployment, set `InternalURL` and `InternalToken` together. Other operations
use the capability listener. Neither token is an exchange API key. The client does
not expose account creation, deletion, permission management or private account
WebSocket events; those are outside this batch's application-client surface.

## Errors, writes and recovery

The client never follows HTTP redirects. Each HTTP request and WebSocket handshake
has a deadline (30 seconds by default). Responses and WebSocket messages are bounded
by `ResponseLimit` (8 MiB by default). Unknown response fields are accepted; malformed
JSON, duplicate keys, missing identities, wrong account/order scope and unknown
order states are rejected.

`*gateway.Error` carries HTTP status, server code, safe message, venue code,
correlation ID, retry hint and the handoff outcome. Its default string representation
omits response text; inspect the structured fields when presenting an error.
Context cancellation and deadlines remain detectable with `errors.Is`.

- Input rejection or a canceled context before sending means `not_attempted`.
- A definitive HTTP 4xx other than 408 is `rejected`.
- Network failure, an ambiguous HTTP response or invalid successful response means
  `unknown`. A failed read never becomes an empty successful result.
- `retryable` and `RetryAfter` do not authorize automatic order resubmission.

There are no automatic write retries, including transport replay of a POST carrying
an idempotency key. Keep one stable key for an intended placement. After losing its
response, use `OrderByKey` with the original key, account and instrument. A 404 still
requires investigation; it is not proof that the original request cannot complete.
Order state `prepared`, `dispatching` or `uncertain` also needs further reads.

`CancelOrder` first reads and verifies the immutable order/account/instrument
identity. A mismatch causes no DELETE. Cancellation outcomes are read back by order
identity; the client does not infer fills or create replacement orders.

## Pagination and freshness

`Orders` returns a bounded page and opaque cursor. `Fills` traverses at most 100
pages of 100 rows, rejects cursor cycles, and discards partial results on any error.
Balances are a complete, non-paginated read across the account routes. The response
requires `complete: true`; up to 10,000 non-zero rows are accepted, and larger
snapshots fail instead of being truncated. Routes are read sequentially, so this
is not an atomic cross-venue snapshot.

A position snapshot carries `freshness`, `verified_at` and row observation times.
An unavailable account with no rows is not a verified flat account. A market
snapshot retains its original `observedAt`; HTTP success and WebSocket pong are
not market freshness signals. Choose the acceptable market age in the application.
Candle history is one bounded window; its `Page` describes source completeness,
not a promise that an entire history has been loaded.

## Subscriptions

`SubscribeMarkets` returns a `MarketStream` for a fixed set of up to 64 topics.
Read `Events()` until it closes, then inspect `Err()`. Every event includes the
requested instrument alongside the server message. Call `Close()` when finished.

- Initial data starts at `snapshot`. Sequence and generation are tracked separately
  for each server subscription; duplicate updates are ignored.
- BBO updates contain a complete quote. Gateway bounds their fanout to at most
  one delta per 50 ms; increasing sequence jumps are allowed for this topic.
  This stream is not a lossless record of every native quote. Order-book deltas
  still require contiguous sequences.
- Upstream gaps emit `resync_required`, invalidating the previous state until a new
  generation snapshot arrives.
- Socket loss or a non-BBO sequence gap emits a gap and resubscribes after bounded backoff
  (1–30 seconds by default). There is no persistent replay of missed ticks.
- A peer that pongs but never acknowledges or supplies initial snapshots is bounded
  by the client timeout. Later data staleness is an application policy.
- Heartbeats detect silently broken connections. Unsupported topics, bad protocol
  responses and authentication failures end the stream with an error.
- A slow consumer ends with `ErrConsumerLagged`; buffered events must not be treated
  as a complete stream. The caller can start a new subscription and rebuild state.
- Closing the stream or canceling its context closes the socket and releases all
  its server subscriptions.

## Verification

```sh
make test-gateway-client
go test -race -short ./clients/gateway ./gateway/httpapi ./services/trading/gateway
```

The gate uses local fake venues, real Gateway HTTP handlers and the real Market Hub.
It covers wire compatibility, scope, decimals, error outcomes, response loss,
pagination, reconnect, generation gaps, heartbeats, slow consumers and cancellation.
It does not use production credentials or claim live-venue acceptance.
