# funding-trader backend

The backend is the authenticated, single-user control plane for funding-trader.
The browser never receives exchange credentials or the Gateway bearer token.
Gateway remains the only owner of exchange credentials and private trading
writes.

## Public market data

Production market data is read through the authenticated BolterTrader Gateway
public-market API. The provider binds every request to both a configured
`account_id` and canonical `instrument_id`; it does not synthesize or expose
venue-native symbols, transport metadata, or credentials.

The dedicated public client can only:

- `GET /v1/funding-rates`
- `GET /v1/funding-rates/history`
- `GET /v1/order-book`
- `GET /v1/bbo`
- `GET /v1/mark-price`
- connect to `/v1/public/ws` for `order_book`, `bbo`, `mark_price`, and
  `funding_rate`

Its concrete type and injected interfaces have no private order, balance,
position, fill, or private-event methods. WebSocket consumers require an
initial sequence-1 snapshot, enforce a stable boot ID and contiguous sequence,
and surface `resync_required` as a typed resnapshot error.

## Configuration

Copy `.env.example` as an operator-owned environment file and replace all
placeholder values. Set exactly one of `FT_GATEWAY_TOKEN` or
`FT_GATEWAY_TOKEN_FILE`; the file form is preferred for container secrets.

`FT_MODE` accepts `test` or `live`. The same discovery, instrument binding,
public-data, risk, and execution code paths are used for both modes. Test mode
unifies Gateway-native `demo` and `testnet`; Live mode uses Gateway-native
`live`.

Live Gateway transport must use HTTPS/WSS unless Gateway is an explicit
loopback sidecar (`localhost`, `127.0.0.0/8`, or `::1`). This is enforced by
both configuration validation and the Gateway client before the bearer token
can be sent.

`FT_MARKET_SYMBOLS` is the backend-owned funding universe. It defaults to
`BTC-USDT,ETH-USDT,SOL-USDT`, accepts a comma-separated list of normalized
markets, and accepts `*` only when the operator intentionally wants every
Gateway instrument. The same universe setting works in Test and Live modes and
prevents a large venue catalog from creating hundreds of
unnecessary REST requests and WebSocket subscriptions.

Live private writes remain disabled unless all of the following are true:

- `FT_MODE=live`
- `FT_COOKIE_SECURE=true`
- `FT_PRODUCTION_WRITES_ENABLED=true`
- the selected Gateway account is write-ready and exposes the exact requested
  operation

Leaving `FT_PRODUCTION_WRITES_ENABLED` unset or false is fail-closed and does
not prevent Live read-only market-data operation.

Login failures are rate-limited in-process by normalized email and direct
network source. Five failures within the bounded window lock that key for five
minutes; throttled responses use `429` and `Retry-After`, successful login
clears prior failures, and throttling is audited without retaining the email.

## Readiness

`GET /api/v1/system/readiness` reports Gateway, account, database, and
`publicData` independently. The public-data component refreshes eligible
read-ready perpetual funding accounts and instruments, then performs real
current-funding and BBO reads. It never reports ready from catalog metadata
alone.

Top-level readiness is not private write authorization. Every write still
requires session, CSRF, idempotency, and operation-specific backend readiness.
Open uses `openReady`, which includes private-event continuity, execution
readiness, and two-leg Gateway acceptance. Reduce-only close uses the separate
`closeReady` gate for a resolvable existing position; Live remains default-deny
without explicit emergency reduce-only approval.

## Test-mode public-data acceptance

The explicit acceptance test is read-only:

```sh
RUN_GATEWAY_READ=1 \
RUN_GATEWAY_PUBLIC_READ=1 \
FT_MODE=test \
FT_GATEWAY_BASE_URL=http://127.0.0.1:18082 \
FT_GATEWAY_TOKEN_FILE=/path/to/GATEWAY_AUTH_TOKEN \
FT_MARKET_SYMBOLS=BTC-USDT,ETH-USDT,SOL-USDT,BTC-USDC,ETH-USDC,SOL-USDC \
go test ./internal/integrations/boltertrader \
  -run 'TestGateway(ReadOnly|PublicMarket)Live' -count=1 -v
```

It discovers the configured Test-mode Gateway-native environments rather than
hard-coding an account, then verifies current and 30-day historical funding,
order book, BBO, and mark price for every configured market, plus the first
snapshot from all four public WebSocket kinds. It performs no private write.

## Test-mode private-write acceptance gate

Run the discovery gate before any Test-mode state-changing execution:

```sh
RUN_GATEWAY_ACCEPTANCE_GATE=1 \
FT_MODE=test \
FT_GATEWAY_BASE_URL=http://127.0.0.1:18082 \
FT_GATEWAY_TOKEN_FILE=/path/to/GATEWAY_AUTH_TOKEN \
go test ./internal/integrations/boltertrader \
  -run '^TestGatewayTwoLegWriteAcceptanceLive$' -count=1 -v
```

The gate is read-only. A real two-leg open is permitted only when it reports at
least two eligible distinct perpetual venues whose instruments share the same
settlement asset and expose actual account funding-payment reads. Gateway V1
does not provide normalized risk limits: capital and leverage remain controlled
by the authenticated single user, and `risk_unverifiable` is a visible preview
warning rather than a submit blocker. Balance, instrument-rule, capacity,
slippage, liquidation-buffer, and inherited blockers remain enforced from real
evidence. A reduce-only close uses the separate backend `closeReady` gate for a
resolvable existing position and may remain available while new opens are
blocked; Live close still requires an explicit emergency reduce-only approval.
Do not mix native `venueEnvironment` scopes, relabel settlement, or substitute
an unapproved account to make the gate pass. The latest sanitized acceptance
result is recorded in
[`docs/TEST_MODE_GATEWAY_ACCEPTANCE.md`](../docs/TEST_MODE_GATEWAY_ACCEPTANCE.md).
