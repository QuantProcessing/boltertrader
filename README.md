# BolterTrader

A self-hosted, multi-venue trading stack for one operator.

```text
web        React terminal (Hyperliquid-style, see DESIGN.md)
  │  cookie session, same origin
trader     password login, session, proxy to gateway, serves web   — no database
  │  bearer token, internal network only
gateway    venue-neutral REST + WebSocket API                       — one encrypted file
  │
exchange   normalized clients and models
  │
sdk        native venue APIs
```

- **sdk/** implements each venue's native API faithfully.
- **exchange/** normalizes venues into one typed client contract.
- **gateway/** exposes that contract over HTTP/WebSocket. It is business-agnostic:
  accounts + encrypted credentials, market data fan-out, order commands, and live
  position/balance snapshots. It keeps no order ledger — idempotency is the
  venue's `clientOrderId`, and the venue is the source of truth.
- **trader/** is the manual-trading app. Other apps (hedging, arbitrage) talk to
  the gateway directly and own their own storage.

Supported: Binance, OKX, Bybit, Bitget, Gate (spot + USDT perps; Bybit/Bitget also
USDC perps), Hyperliquid and Lighter (spot + perps).

API and security contract: [docs/gateway-api.md](docs/gateway-api.md).
Exchange library: [docs/exchange/README.md](docs/exchange/README.md).

## Run

Locally (Go 1.26, Node 22):

```sh
scripts/dev.sh                                   # http://127.0.0.1:8080
HTTPS_PROXY=http://127.0.0.1:7892 scripts/dev.sh # if venues need a proxy
```

With Docker:

```sh
docker compose up -d --build --force-recreate
docker compose logs trader | grep password       # first-run password, shown once
```

The first start prints a generated password once. Change it with
`go run ./cmd/trader passwd` (or `docker compose run --rm trader passwd`).
Connect exchanges from the **Connect** button in the app.

## Safety

Every order reaches a real exchange. Use keys without withdrawal permission and
start small. Credentials are write-only: they are verified against the venue,
stored encrypted (`data/gateway/accounts.enc`, key in `data/gateway/master.key`),
and never returned by any API or written to logs. Back up `master.key`; without it
the stored accounts cannot be decrypted.

Only `127.0.0.1` is bound by default. For remote access use an SSH tunnel or a
VPN, or put a TLS reverse proxy in front and set `TRADER_SECURE_COOKIE=1`.

## Test

```sh
make test
```
