import type { Page, Route } from "@playwright/test";

import type {
  Account,
  ActivityEvent,
  ArbitrageOpportunity,
  Candle,
  CandlePage,
  Command,
  FundingBoardSnapshot,
  Instrument,
  ManualIntent,
  MarketFact,
  SessionState,
  Venue,
} from "../src/api/types";

/* ---------------------------------------------------------------------------
 * A fake auth edge, installed in the page.
 *
 * It is not a mock of the frontend's own calls — it answers by path, exactly as
 * `services/authedge/proxy/routes.go` does, and it speaks each route's real
 * envelope: `{data:{accounts}}` with no meta for the gateway plane,
 * `{meta,data:{items}}` for trading's lists, `{meta,data,rateLimit}` for the
 * metered market reads. A frontend that unwraps the wrong one fails here rather
 * than in front of an operator.
 *
 * The order flow is stateful because the assertion that matters is a sequence:
 * an order that is placed must then be visible, and cancelling it must change
 * its state. A per-request stub could not tell those apart.
 *
 * Unknown paths answer with an empty trading list rather than 404, so a screen
 * that grows a new query does not fail an unrelated test. Anything a test
 * genuinely depends on is spelled out below.
 * ------------------------------------------------------------------------- */

const META = { requestId: "req-e2e", contractVersion: "1", apiVersion: "v1" };

export const CREDENTIALS = { username: "operator", password: "hunter2" };

export const INSTRUMENT: Instrument = {
  instrumentId: "binance:testnet:spot:BTCUSDT",
  venue: "binance",
  environment: "test",
  product: "spot",
  symbol: "BTCUSDT",
  baseAsset: "BTC",
  quoteAsset: "USDT",
  rules: { tickSize: "0.01", stepSize: "0.00001" },
  ready: true,
};

export const ACCOUNT: Account = {
  accountId: "acct-binance-testnet",
  venue: "binance",
  mode: "test",
  nativeEnvironment: "testnet",
  displayName: "Binance testnet",
  productPolicy: ["manual", "arbitrage"],
  routes: [
    { routeId: "spot", product: "spot", routeProfileId: "binance:testnet:spot", requestedState: "active" },
  ],
  status: "active",
  desiredStateRevision: 3,
  runtimeGeneration: 1,
  credentialVersion: 1,
  lastValidatedAt: "2026-08-13T09:00:00Z",
  updatedAt: "2026-08-13T09:00:00Z",
};

/**
 * The second venue, which is the whole of what makes a hedge possible.
 *
 * Arbitrage is the one product that cannot be exercised by a single account: it
 * buys on one exchange and sells on another, so a fixture with one account can
 * only ever prove the refusal. This one carries the perpetual route the funding
 * board's bybit column quotes.
 */
export const HEDGE_ACCOUNT: Account = {
  accountId: "acct-bybit-testnet",
  venue: "bybit",
  mode: "test",
  nativeEnvironment: "testnet",
  displayName: "Bybit testnet",
  productPolicy: ["manual", "arbitrage"],
  routes: [{ routeId: "perp", product: "perp", routeProfileId: "bybit:testnet:perp", requestedState: "active" }],
  status: "active",
  desiredStateRevision: 2,
  runtimeGeneration: 1,
  credentialVersion: 1,
  lastValidatedAt: "2026-08-13T09:00:00Z",
  updatedAt: "2026-08-13T09:00:00Z",
};

const VENUE: Venue = {
  venue: "binance",
  environment: "test",
  nativeEnvironment: "testnet",
  ready: true,
  products: ["spot"],
  credentialSchema: {
    schemaVersion: 1,
    authKind: "hmac",
    fields: [
      { key: "apiKey", label: "API key", required: true, valueType: "string" },
      { key: "apiSecret", label: "API secret", required: true, valueType: "string" },
    ],
  },
  routeProfiles: [
    { routeId: "spot", product: "spot", routeProfileId: "binance:testnet:spot", label: "Spot" },
  ],
};

/** A book wide enough that the mid, the spread and the depth bars all differ. */
const FACT: MarketFact = {
  venue: "binance",
  environment: "test",
  product: "spot",
  instrumentId: INSTRUMENT.instrumentId,
  bestBid: "64000.10",
  bestBidSize: "0.85000",
  bestAsk: "64001.40",
  bestAskSize: "1.20000",
  bids: [
    { price: "64000.10", quantity: "0.85000" },
    { price: "63999.50", quantity: "1.40000" },
    { price: "63998.20", quantity: "2.10000" },
  ],
  asks: [
    { price: "64001.40", quantity: "1.20000" },
    { price: "64002.60", quantity: "0.90000" },
    { price: "64004.00", quantity: "3.30000" },
  ],
  observedAt: "2026-08-13T09:00:00Z",
};

/**
 * Two venues quoting one instrument, which is the least that makes a matrix a
 * matrix: a spread needs two columns, and the third column here is unavailable
 * so the page's degraded marker has something real to mark. Lighter is the
 * venue that publishes a rate with no interval and no open interest, so the row
 * that cannot honestly be annualised is present rather than hypothetical.
 */
const FUNDING_BOARD: FundingBoardSnapshot = {
  mode: "test",
  observedAt: "2026-08-13T09:00:00Z",
  venues: [
    {
      venue: "binance",
      environment: "test",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "binance.perp.usdt.test.v1",
      observedAt: "2026-08-13T09:00:00Z",
      stale: false,
      unavailable: false,
      rows: [
        {
          symbol: "BTC-USDT",
          baseAsset: "BTC",
          quoteAsset: "USDT",
          rate: "0.0001",
          intervalHours: 8,
          nextFundingTime: "2026-08-13T16:00:00Z",
          markPrice: "64001.00",
        },
        {
          symbol: "ETH-USDT",
          baseAsset: "ETH",
          quoteAsset: "USDT",
          rate: "-0.00025",
          intervalHours: 8,
          nextFundingTime: "2026-08-13T16:00:00Z",
        },
      ],
    },
    {
      venue: "bybit",
      environment: "test",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "bybit.perp.usdt.test.v1",
      observedAt: "2026-08-13T09:00:00Z",
      stale: false,
      unavailable: false,
      rows: [
        {
          symbol: "BTC-USDT",
          baseAsset: "BTC",
          quoteAsset: "USDT",
          rate: "0.00042",
          intervalHours: 8,
          nextFundingTime: "2026-08-13T16:00:00Z",
          openInterest: "1520.44",
        },
      ],
    },
    {
      venue: "lighter",
      environment: "test",
      product: "perp",
      settlement: "USDC",
      routeProfileId: "lighter.perp.usdc.test.v1",
      observedAt: "2026-08-13T08:58:00Z",
      stale: true,
      unavailable: false,
      rows: [{ symbol: "BTC-USDC", baseAsset: "BTC", quoteAsset: "USDC", rate: "0.00007" }],
    },
    {
      venue: "okx",
      environment: "test",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "okx.perp.usdt.test.v1",
      stale: false,
      unavailable: true,
      rows: [],
    },
  ],
};

/**
 * The one pair the board's two full columns can make.
 *
 * Derived by hand from `FUNDING_BOARD` so the two screens agree: binance pays
 * 0.0001 every eight hours and bybit pays 0.00042, which is 0.0003 and 0.00126
 * a day. Longs pay where the rate is higher, so the hedge is long binance and
 * short bybit, and the daily spread is the difference. Lighter quotes BTC-USDC
 * — a different quote asset, and so not the same underlying — which is why one
 * opportunity rather than three comes out of a board with three live venues.
 */
const ARBITRAGE_OPPORTUNITIES: ArbitrageOpportunity[] = [
  {
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    long: {
      venue: "binance",
      environment: "test",
      accountId: ACCOUNT.accountId,
      instrumentId: "binance:testnet:perp:BTC-USDT",
      rate: "0.0001",
      intervalHours: 8,
      nextFundingTime: "2026-08-13T16:00:00Z",
      markPrice: "64001.00",
      dailyRate: "0.0003",
      stale: false,
    },
    short: {
      venue: "bybit",
      environment: "test",
      accountId: HEDGE_ACCOUNT.accountId,
      instrumentId: "bybit:testnet:perp:BTC-USDT",
      rate: "0.00042",
      intervalHours: 8,
      nextFundingTime: "2026-08-13T16:00:00Z",
      openInterest: "1520.44",
      dailyRate: "0.00126",
      stale: false,
    },
    spreadDaily: "0.00096",
    spreadApr: "0.3504",
    executable: true,
    blockers: [],
  },
];

/** Deterministic bars: a chart that redraws differently per run is untestable. */
function candles(count: number): Candle[] {
  const step = 15 * 60_000;
  const start = Date.parse("2026-08-10T00:00:00Z");
  return Array.from({ length: count }, (_unused, index) => {
    const open = 63500 + index * 4;
    const openTime = start + index * step;
    return {
      openTime: new Date(openTime).toISOString(),
      closeTime: new Date(openTime + step - 1).toISOString(),
      open: open.toFixed(2),
      high: (open + 30).toFixed(2),
      low: (open - 25).toFixed(2),
      close: (open + 8).toFixed(2),
      volume: (10 + index).toFixed(4),
      complete: index < count - 1,
    } satisfies Candle;
  });
}

export class FakeEdge {
  private signedIn = false;
  private orders: Command[] = [];
  private activity: ActivityEvent[] = [];
  private sequence = 0;

  /** Every request the page makes, in order — used to assert what was called. */
  readonly seen: { method: string; path: string }[] = [];

  async install(page: Page): Promise<void> {
    await page.route("**/api/v1/**", (route) => this.handle(route));
  }

  /** Skips the login screen for tests that are about something else. */
  signIn(): void {
    this.signedIn = true;
  }

  private handle(route: Route): Promise<void> {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname;
    const method = request.method();
    this.seen.push({ method, path });

    const body = (): Record<string, unknown> => {
      const raw = request.postData();
      return raw === null ? {} : (JSON.parse(raw) as Record<string, unknown>);
    };

    /* --- Auth ------------------------------------------------------------ */

    if (path === "/api/v1/auth/login" && method === "POST") {
      const payload = body();
      if (payload.username !== CREDENTIALS.username || payload.password !== CREDENTIALS.password) {
        return fail(route, 401, "AUTH_REQUIRED", "invalid username or password");
      }
      this.signedIn = true;
      return ok(route, { meta: META, data: session() });
    }

    if (path === "/api/v1/me" || (path === "/api/v1/auth/session" && method === "GET")) {
      if (!this.signedIn) return fail(route, 401, "AUTH_REQUIRED", "no session");
      return ok(route, { meta: META, data: session() });
    }

    if (path === "/api/v1/auth/session" && method === "DELETE") {
      this.signedIn = false;
      return route.fulfill({ status: 204, body: "" });
    }

    if (!this.signedIn) return fail(route, 401, "AUTH_REQUIRED", "no session");

    /* --- Gateway plane: no meta key -------------------------------------- */

    if (path === "/api/v1/accounts" && method === "GET") {
      return ok(route, { data: { accounts: [ACCOUNT, HEDGE_ACCOUNT] } });
    }

    /* --- Catalog: a bare array beside a projection ------------------------ */

    if (path === "/api/v1/catalog/instruments") {
      return ok(route, { meta: META, data: [INSTRUMENT], projection: projection() });
    }

    if (path === "/api/v1/catalog/venues") {
      return ok(route, { meta: META, data: [VENUE], projection: projection() });
    }

    /* --- Market: metered -------------------------------------------------- */

    if (path === "/api/v1/market-data") {
      return ok(route, { meta: META, data: FACT, rateLimit: budget() });
    }

    if (path === "/api/v1/funding-board") {
      return ok(route, { meta: META, data: FUNDING_BOARD, rateLimit: budget() });
    }

    if (path === "/api/v1/candles") {
      const interval = url.searchParams.get("interval") ?? "15m";
      const limit = Number(url.searchParams.get("limit") ?? "300");
      const page: CandlePage = {
        venue: "binance",
        environment: "test",
        product: "spot",
        instrumentId: INSTRUMENT.instrumentId,
        interval: interval as CandlePage["interval"],
        candles: candles(Math.min(limit, 120)),
        observedAt: "2026-08-13T09:00:00Z",
      };
      return ok(route, { meta: META, data: page, rateLimit: budget() });
    }

    /* --- The manual product ---------------------------------------------- */

    if (path === "/api/v1/products/manual/execution-previews" && method === "POST") {
      const intent = body();
      // The server refuses a market order that carries a price rather than
      // ignoring it, so the frontend sending one must fail here too.
      if (intent.orderType === "market" && intent.limitPrice !== "") {
        return fail(route, 400, "INVALID_ARGUMENT", "market order must not carry a price");
      }
      return ok(
        route,
        { meta: META, data: { previewId: `prev-${++this.sequence}`, blockers: [], warnings: [] } },
        201,
      );
    }

    if (path === "/api/v1/products/manual/commands" && method === "POST") {
      const payload = body();
      const header = request.headers()["idempotency-key"];
      if (header !== payload.idempotencyKey) {
        return fail(route, 400, "INVALID_ARGUMENT", "idempotency key header and body disagree");
      }
      return payload.action === "cancel"
        ? this.cancel(route, String(payload.orderId ?? ""))
        : this.place(route, payload);
    }

    if (path === "/api/v1/products/manual/orders" && method === "GET") {
      return ok(route, { meta: META, data: { items: this.orders } });
    }

    /* --- The arbitrage product ------------------------------------------- */

    // `{items, observedAt}` rather than `{items}`: the scan states which board
    // snapshot it ranked, and the page prints that rather than the clock.
    if (path === "/api/v1/products/arbitrage/opportunities") {
      return ok(route, {
        meta: META,
        data: { items: ARBITRAGE_OPPORTUNITIES, observedAt: FUNDING_BOARD.observedAt },
      });
    }

    if (path === "/api/v1/products/arbitrage/positions") {
      return ok(route, { meta: META, data: { items: [] } });
    }

    /* --- Trading projections --------------------------------------------- */

    if (path === "/api/v1/activity") {
      return ok(route, { meta: META, data: { items: this.activity } });
    }

    if (path === "/api/v1/commands") {
      return ok(route, { meta: META, data: { items: this.orders } });
    }

    if (path === "/api/v1/system/status" || path === "/api/v1/system/readiness") {
      return ok(route, { meta: META, data: { ready: true, components: {} } });
    }

    return ok(route, { meta: META, data: { items: [] } });
  }

  /* --- Order state ------------------------------------------------------- */

  private place(route: Route, payload: Record<string, unknown>): Promise<void> {
    // The preview is spent by the command; a second use of the same one is the
    // replay case, which the real server answers 200 with the original command.
    const existing = this.orders.find((order) => order.idempotencyKey === payload.idempotencyKey);
    if (existing) return ok(route, { meta: META, data: existing });

    const intent: ManualIntent = {
      instrumentId: INSTRUMENT.instrumentId,
      side: "buy",
      quantity: "0.00100",
      orderType: "limit",
      limitPrice: "50000.00",
      timeInForce: "GTC",
      reduceOnly: false,
      routeId: "spot",
      desiredStateRevision: ACCOUNT.desiredStateRevision,
      runtimeGeneration: ACCOUNT.runtimeGeneration,
      credentialVersion: ACCOUNT.credentialVersion,
    };

    const command: Command = {
      commandId: `cmd-${++this.sequence}`,
      accountId: ACCOUNT.accountId,
      lineageId: String(payload.lineageId ?? `lin-${this.sequence}`),
      productId: "manual",
      mode: "test",
      reservationId: null,
      idempotencyKey: String(payload.idempotencyKey ?? ""),
      commandKind: "place",
      state: "submitted",
      payload: intent,
      blockers: [],
      createdAt: "2026-08-13T09:00:00Z",
      updatedAt: "2026-08-13T09:00:00Z",
    };

    this.orders = [command, ...this.orders];
    this.record("manual.order.placed", command.commandId);
    return ok(route, { meta: META, data: command }, 202);
  }

  private cancel(route: Route, orderId: string): Promise<void> {
    const target = this.orders.find((order) => order.commandId === orderId);
    if (!target) return fail(route, 404, "NOT_FOUND", "no such order");

    const canceled: Command = { ...target, state: "canceled", updatedAt: "2026-08-13T09:05:00Z" };
    this.orders = this.orders.map((order) => (order.commandId === orderId ? canceled : order));
    this.record("manual.order.canceled", orderId);
    return ok(route, { meta: META, data: canceled });
  }

  private record(kind: string, resourceId: string): void {
    const sequence = this.activity.length + 1;
    // Newest first, as trading's ring buffer serves it.
    this.activity = [
      {
        eventId: `evt-${sequence}`,
        sequence,
        cursor: String(sequence),
        kind,
        productId: "manual",
        accountId: ACCOUNT.accountId,
        resourceId,
        payload: {},
        observedAt: "2026-08-13T09:05:00Z",
      },
      ...this.activity,
    ];
  }
}

/* --- Envelope helpers ----------------------------------------------------- */

const session = (): SessionState => ({
  principal: { id: "principal-1", mode: "test", username: CREDENTIALS.username },
  session: { csrfToken: "csrf-e2e", expiresAt: "2099-01-01T00:00:00Z" },
});

const projection = () => ({ revision: "42", observedAt: "2026-08-13T09:00:00Z" });

const budget = () => ({ limit: 1200, used: 30, remaining: 1170, windowEnds: "2026-08-13T09:01:00Z" });

function ok(route: Route, body: unknown, status = 200): Promise<void> {
  return route.fulfill({ status, contentType: "application/json", body: JSON.stringify(body) });
}

function fail(route: Route, status: number, code: string, message: string): Promise<void> {
  return route.fulfill({
    status,
    contentType: "application/json",
    body: JSON.stringify({ error: { code, message, details: {}, retryable: status >= 500, requestId: "req-e2e" } }),
  });
}
