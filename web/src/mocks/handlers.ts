import { HttpResponse, http } from "msw";

import type {
  Account,
  CandleInterval,
  Command,
  CredentialBundle,
  FundingBoardRow,
  FundingBoardSnapshot,
  FundingBoardVenue,
  Instrument,
  ManualCommandRequest,
  ManualOrderRequest,
} from "@/api/types";

import { accountPositions, accounts, activity, orders, session } from "./db";
import { PUBLIC_PATHS } from "./public-paths";
import {
  CREDENTIALS,
  ACCOUNT_POSITIONS,
  BALANCES,
  FILLS,
  INSTRUMENTS,
  LEDGER,
  META,
  OBSERVED_AT,
  PNL,
  POSITIONS,
  RATE_LIMIT,
  SESSION,
  SYSTEM,
  VENUES,
  VENUE_REVISION,
  instrumentOf,
  runtimeOf,
} from "./fixtures";
import { fetchCandles, fetchFundingBoard, fetchFundingHistory, fetchMarketFact } from "./venues";

/* ---------------------------------------------------------------------------
 * The mock edge.
 *
 * This is not a mock of the frontend's own calls. It answers by path, exactly
 * as `services/authedge/proxy/routes.go` does, and it speaks each route's real
 * response envelope — seven different ones, because the browser surface has
 * seven. A frontend that unwraps the wrong envelope fails here rather than in
 * front of an operator, which is the only reason it is worth mirroring the
 * table rather than returning one convenient shape everywhere:
 *
 *   catalog/instruments          {data: {revision, observedAt, instruments}}
 *   market-data, candles,        {meta, data: T, rateLimit}
 *     funding-board, -history
 *   the trading ledgers          {meta, data: {items: T[]}}
 *   pnl, commands      {meta, data: T}
 *   products/manual/venues       {meta, data: {revision, items}}
 *   /api/v1/accounts/*           {data: {account | accounts}}   — no meta
 *
 * ------------------------------------------------------------------------- */

const REQUEST_ID = META.requestId;

const envelope = <T,>(body: T, status = 200) =>
  HttpResponse.json(body as Record<string, unknown>, { status });

/** `{meta, data}` — a single object. */
const data = <T>(value: T, status = 200) => envelope({ meta: META, data: value }, status);

/** `{meta, data: {items}}` — every trading ledger and projection list. */
const items = <T>(list: T[]) => envelope({ meta: META, data: { items: list } });

/** `{meta, data, projection}` — the catalog reads, which carry a revision. */

/** `{meta, data, rateLimit}` — the metered reads, which spend a venue budget. */
const metered = <T>(value: T) => envelope({ meta: META, data: value, rateLimit: RATE_LIMIT });

/** `{data}` with no meta — the gateway's account plane speaks only this. */
const gateway = <T>(value: T, status = 200) => envelope({ data: value }, status);

/**
 * The error body `client.ts` parses. Every field is present on every failure,
 * because a client that has to guess whether `retryable` was omitted or false
 * will guess wrong under load.
 */
const failure = (status: number, code: string, message: string, retryable = false) =>
  envelope({ error: { code, message, details: {}, retryable, requestId: REQUEST_ID } }, status);

const notFound = (what: string) => failure(404, "NOT_FOUND", `no such ${what}`);
const invalid = (message: string) => failure(400, "VALIDATION_ERROR", message);

const num = (value: unknown): number => {
  const parsed = Number(value);
  return Number.isFinite(parsed) ? parsed : 0;
};

/* --- The session gate ----------------------------------------------------- */

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/**
 * Everything under `/api/v1` needs a session, and every unsafe method needs the
 * CSRF token that was minted with it.
 *
 * Returning `undefined` hands the request to the next matching handler, so this
 * is a gate in front of the table rather than a copy of the check inside each
 * of its forty entries. Login is the one exemption of its own: it is how the
 * session comes into existence. Beyond it sits `PUBLIC_PATHS`, which is a
 * licence to read and nothing more — a signed-out write still fails here,
 * because the path is consulted only for a safe method.
 *
 * One asymmetry with the real edge is worth naming: it distinguishes "no cookie
 * arrived" from "a cookie arrived and was stale", and only the first is allowed
 * through anonymously. The mock has a single boolean, so signed-out here always
 * means the first. A stale session downgrading silently to anonymous is a bug
 * the mock structurally cannot reproduce; the Go test named for it is what
 * covers that case.
 *
 * Getting this wrong in the mock would hide the failure mode that matters most
 * — a screen that quietly renders against a session the server would have
 * rejected — so the gate is deliberately the first handler registered.
 */
const gate = http.all("/api/v1/*", ({ request }) => {
  const { pathname } = new URL(request.url);
  if (pathname === "/api/v1/auth/login") return undefined;

  if (!session.open) {
    if (SAFE_METHODS.has(request.method) && PUBLIC_PATHS.has(pathname)) return undefined;
    return failure(401, "AUTH_REQUIRED", "no session");
  }

  if (!SAFE_METHODS.has(request.method)) {
    const token = request.headers.get("X-CSRF-Token");
    if (token !== SESSION.session.csrfToken) {
      return failure(403, "CSRF_REQUIRED", "csrf token is missing or no longer valid");
    }
  }
  return undefined;
});

/* --- Auth ----------------------------------------------------------------- */

const auth = [
  http.post("/api/v1/auth/login", async ({ request }) => {
    const body = (await request.json()) as { username?: string; password?: string };
    if (body.username !== CREDENTIALS.username || body.password !== CREDENTIALS.password) {
      // Says which of the two was wrong: neither. Naming the field would tell an
      // attacker which half of the guess to keep.
      return failure(401, "AUTH_REQUIRED", "invalid username or password");
    }
    session.signIn();
    return data(SESSION);
  }),

  http.get("/api/v1/me", () => data(SESSION)),

  http.delete("/api/v1/auth/session", () => {
    session.signOut();
    // 204, and deliberately no body: `client.ts` returns undefined for these and
    // a body here would be parsed as a session that no longer exists.
    return new HttpResponse(null, { status: 204 });
  }),
];

/* --- System --------------------------------------------------------------- */

const system = [
  http.get("/api/v1/system/status", () => data(SYSTEM)),
  // Ready, so 200. The 503 branch is real but there is nothing here that can be
  // unready — no database to be behind and no venue to be unreachable.
  http.get("/api/v1/system/readiness", () => data(SYSTEM)),
  http.get("/api/v1/venues/connectivity", () => data(
    ["binance", "bybit", "bitget", "gate", "okx", "hyperliquid", "lighter", "nado", "aster"].map(
      (venue, index) => ({
        venue,
        state: "online",
        latencyMs: 35 + index * 4,
        ewmaLatencyMs: 40 + index * 4,
        consecutiveFailures: 0,
        lastSuccessAt: new Date().toISOString(),
      }),
    ),
  )),
];

/* --- Catalog and market data ---------------------------------------------- */

/**
 * The market plane is the one part of this file with a venue behind it.
 *
 * A book, a chart and a funding rate are public everywhere: no key, no
 * signature, no account. Mocking them was a mistake with a visible cost — the
 * synthetic prices were drawn around anchors written down once, so BTC showed
 * 114,250 months after the market left, and an instrument with no anchor priced
 * at 100. What the mock is for is the credentialed half: placing an order,
 * reading a balance, sealing a preview. Those never leave the page.
 *
 * When a venue does not answer, that is reported as the unavailability it is.
 * `PUBLIC_DATA_NOT_READY` is the code the real trading service returns for the
 * same condition, so the screen behaves the same way against either edge.
 */
const unreachable = (venue: string, error: unknown) =>
  failure(
    503,
    "PUBLIC_DATA_NOT_READY",
    `${venue} public market data is unavailable: ${error instanceof Error ? error.message : String(error)}`,
    true,
  );

const catalog = [
  http.get("/api/v1/catalog/instruments", () => data({ revision: "mock-catalog-v1", observedAt: new Date().toISOString(), instruments: INSTRUMENTS })),

  http.get("/api/v1/market-data", async ({ request }) => {
    const instrumentId = new URL(request.url).searchParams.get("instrumentId") ?? "";
    const instrument = instrumentOf(instrumentId);
    if (!instrument) return notFound("instrument");
    try {
      return metered(await fetchMarketFact(instrument));
    } catch (error) {
      return unreachable(instrument.venue, error);
    }
  }),

  http.get("/api/v1/candles", async ({ request }) => {
    const query = new URL(request.url).searchParams;
    const instrument = instrumentOf(query.get("instrumentId") ?? "");
    if (!instrument) return notFound("instrument");

    const interval = (query.get("interval") ?? "15m") as CandleInterval;
    const limit = Number(query.get("limit") ?? 200);
    // The server's own bound. A chart that asks for more gets a 400 rather than
    // a silently truncated page, so the clamp has to live in the caller.
    if (!Number.isFinite(limit) || limit < 1 || limit > 500) return invalid("limit must be between 1 and 500");

    try {
      const bars = await fetchCandles(instrument, interval, limit);
      return metered({
        venue: instrument.venue,
        environment: instrument.environment,
        product: instrument.product,
        instrumentId: instrument.instrumentId,
        interval,
        candles: bars,
        // The venue's observation, not a constant: these bars were read now.
        observedAt: new Date().toISOString(),
      });
    } catch (error) {
      return unreachable(instrument.venue, error);
    }
  }),
];

/* --- Funding -------------------------------------------------------------- */

const funding = [
  http.get("/api/v1/funding-board", async () => {
    try {
      return data(await fetchFundingBoard());
    } catch (error) {
      return unreachable("funding board", error);
    }
  }),

  http.get("/api/v1/funding-history", async ({ request }) => {
    const query = new URL(request.url).searchParams;
    const instrument = instrumentOf(query.get("instrumentId") ?? "");
    const since = Date.parse(query.get("since") ?? "");
    const until = Date.parse(query.get("until") ?? "");
    const span = until - since;
    if (!Number.isFinite(span) || span <= 0 || span > 30 * 86_400_000) {
      return failure(400, "FUNDING_HISTORY_WINDOW_UNSUPPORTED", "Invalid history range");
    }

    if (!instrument) return notFound("instrument");

    // A venue that publishes no history at all is a fact about the venue, not
    // about this moment, which is why it is answered with its own code and
    // `retryable: false`.
    if (!FUNDING_HISTORY_VENUES.has(instrument.venue)) {
      return failure(404, "FUNDING_HISTORY_UNSUPPORTED", `${instrument.venue} publishes no funding history`);
    }

    try {
      const series = await fetchFundingHistory(instrument, new Date(since), new Date(until));
      // Every venue here reports an unknown instrument as a 200 — an empty
      // array, or a failure code in the body — so "no points" is the only
      // signal there is, and it is the one the venue gives for "not listed".
      if (!series) {
        return failure(404, "FUNDING_HISTORY_INSTRUMENT_UNKNOWN", `${instrument.venue} does not list ${instrument.symbol}`);
      }
      return metered(series);
    } catch (error) {
      return unreachable(instrument.venue, error);
    }
  }),
];

/** The four venues with a public funding-history endpoint. */
const FUNDING_HISTORY_VENUES = new Set(["binance", "bybit", "okx", "lighter"]);

/* --- Trading ledgers and projections -------------------------------------- */

const trading = [


  http.get("/api/v1/commands", ({ request }) => {
    const query = new URL(request.url).searchParams;
    const accountId = query.get("accountId");
    const productId = query.get("productId");
    const state = query.get("state");
    return items(
      orders
        .projections()
        .filter((order) => !accountId || order.accountId === accountId)
        .filter((order) => !productId || order.productId === productId)
        .filter((order) => !state || order.state === state),
    );
  }),

  http.get("/api/v1/commands/:commandId", ({ params }) => {
    const order = orders.find(String(params.commandId));
    if (!order) return notFound("command");
    const { payload: _payload, executionEvidence: _evidence, ...projection } = order;
    return data(projection);
  }),

  http.get("/api/v1/positions", ({ request }) => {
    const accountId = new URL(request.url).searchParams.get("accountId");
    return items(POSITIONS.filter((position) => !accountId || position.accountId === accountId));
  }),

  http.get("/api/v1/balances", ({ request }) => {
    const accountId = new URL(request.url).searchParams.get("accountId");
    return items(BALANCES.filter((balance) => !accountId || balance.accountId === accountId));
  }),

  http.get("/api/v1/account-positions", ({ request }) => {
    const query = new URL(request.url).searchParams;
    return data(filterAccountPositions({
      accountId: query.get("accountId"),
      instrumentId: query.get("instrumentId"),
    }));
  }),

  http.get("/api/v1/portfolio/positions", ({ request }) => {
    const query = new URL(request.url).searchParams;
    return data(filterAccountPositions({
      accountId: query.get("accountId"),
      instrumentId: query.get("instrumentId"),
      venue: query.get("venue"),
      product: query.get("product"),
      status: query.get("status"),
    }));
  }),

  http.get("/api/v1/ledger", () => items(LEDGER)),

  // The one ledger route that is not a list: a single aggregate in a bare
  // `{meta, data}`. Wrapping it in `items` would typecheck and render nothing.
  http.get("/api/v1/pnl", () => data(PNL)),

  http.get("/api/v1/fills", ({ request }) => {
    const query = new URL(request.url).searchParams;
    const accountId = query.get("accountId");
    const instrumentId = query.get("instrumentId");
    return items(
      FILLS.filter((fill) => !accountId || fill.accountId === accountId).filter(
        (fill) => !instrumentId || fill.instrumentId === instrumentId,
      ),
    );
  }),

  http.get("/api/v1/activity", ({ request }) => {
    const query = new URL(request.url).searchParams;
    const kind = query.get("kind");
    const accountId = query.get("accountId");
    return items(
      activity
        .list()
        .filter((event) => !kind || event.kind === kind)
        .filter((event) => !accountId || event.accountId === accountId),
    );
  }),

  /**
   * Trading's runtime projection, not the gateway's account record. They share
   * a URL prefix and nothing else, which is exactly why the proxy table names
   * this one route separately.
   */
  http.get("/api/v1/accounts/:accountId/runtimes", ({ params }) => {
    const account = accounts.find(String(params.accountId));
    if (!account) return notFound("account");
    return items([runtimeOf(account)]);
  }),
];

function filterAccountPositions(filters: Record<string, string | null>) {
  const selected = accountPositions.list().filter((position) =>
    (!filters.accountId || position.accountId === filters.accountId) &&
    (!filters.instrumentId || position.instrumentId === filters.instrumentId) &&
    (!filters.venue || position.venue === filters.venue) &&
    (!filters.product || position.product === filters.product) &&
    (!filters.status || position.freshness === filters.status),
  );
  const accountIds = new Set(selected.map((position) => position.accountId));
  return {
    ...ACCOUNT_POSITIONS,
    summary: {
      ...ACCOUNT_POSITIONS.summary,
      positionCount: selected.length,
      accountCount: accountIds.size,
    },
    accounts: ACCOUNT_POSITIONS.accounts.filter((account) => accountIds.has(account.accountId)),
    items: selected,
  };
}

/* --- Manual product ------------------------------------------------------- */

/**
 * The book, for the previews that need a reference price.
 *
 * Unlike the market-data route, a preview must not fail because a venue was
 * briefly unreachable — the operator is mid-ticket and the read is incidental
 * to what they asked for. So this answers null and each caller decides what a
 * missing price means for the field it was filling.
 */
async function touch(instrument: Instrument) {
  try {
    return await fetchMarketFact(instrument);
  } catch {
    return null;
  }
}

/**
 * The blockers the instrument's own published rules imply, and nothing else.
 *
 * Only two rules can be checked without a venue: the lot floor and the notional
 * floor. Both are read from the same `rules` object the catalog served, so a
 * ticket that renders "min 0.00001" and a preview that rejects 0.000005 are
 * quoting one number rather than two that happen to agree.
 *
 * A market order has no price of its own, so the venue checks its notional at
 * submission. The mock does not invent a price for that check.
 */
function manualBlockers(instrument: Instrument, request: ManualOrderRequest): string[] {
  const blockers: string[] = [];
  const quantity = num(request.quantity);
  const minQuantity = num(instrument.rules.minQuantity);
  const minNotional = num(instrument.rules.minNotional);

  if (quantity <= 0) blockers.push("QUANTITY_REQUIRED");
  else if (minQuantity > 0 && quantity < minQuantity) blockers.push("BELOW_MIN_QUANTITY");

  const reference = request.orderType === "limit" ? num(request.limitPrice) : 0;
  if (minNotional > 0 && reference > 0 && quantity * reference < minNotional) {
    blockers.push("BELOW_MIN_NOTIONAL");
  }

  return blockers;
}

const manual = [
  // The only route that speaks `{meta, data: {revision, items}}`.
  http.get("/api/v1/products/manual/venues", () =>
    envelope({ meta: META, data: { revision: VENUE_REVISION, items: VENUES } }),
  ),

  http.post("/api/v1/products/manual/commands", async ({ request }) => {
    const body = (await request.json()) as ManualCommandRequest;

    // The header and the body carry the same key, and the server compares them.
    // A retry that changed one and not the other is a different request wearing
    // the first one's identity, which is the one thing idempotency must not let
    // through.
    if (request.headers.get("Idempotency-Key") !== body.idempotencyKey) {
      return invalid("idempotency key header and body disagree");
    }

    if (body.action === "place") {
      const order = body.order;
      if (!order) return invalid("place command requires an order");
      if (order.orderType === "market" && order.limitPrice) return invalid("market order must not carry a price");
      if (order.orderType === "limit" && !order.limitPrice) return invalid("limit order must carry a price");
      const instrument = instrumentOf(order.instrumentId);
      if (!instrument) return notFound("instrument");
      if (!accounts.find(order.accountId)) return notFound("account");
      if (manualBlockers(instrument, order).length > 0) return invalid("order violates venue rules");
      const placed = orders.place({
        order,
        lineageId: body.lineageId,
        idempotencyKey: body.idempotencyKey,
      });
      // 200 for the replay, 202 for the order that was actually sent. The body
      // is identical, which is the point: a retry is indistinguishable from the
      // original except in the status line.
      return data(placed.command, placed.replay ? 200 : 202);
    }

    if (body.action === "cancel") {
      const canceled = orders.cancel(body.orderId);
      if (!canceled) return notFound("order");
      return data(canceled, 202);
    }

    return invalid(`unsupported action ${String(body.action)}`);
  }),

  http.get("/api/v1/products/manual/orders", ({ request }) => {
    const instrumentId = new URL(request.url).searchParams.get("instrumentId");
    return items(
      orders
        .list()
        .filter(
          (order) =>
            !instrumentId || (order.payload as { instrumentId?: string }).instrumentId === instrumentId,
        ),
    );
  }),

  http.get("/api/v1/products/manual/orders/:orderId", ({ params }) => {
    const order = orders.find(String(params.orderId));
    return order ? data(order) : notFound("order");
  }),
];

const accountPlane = [
  http.get("/api/v1/accounts", () => gateway({ accounts: accounts.list() })),

  http.post("/api/v1/accounts", async ({ request }) => {
    const body = (await request.json()) as {
      venue: string;
      displayName: string;
      credentialBundle: CredentialBundle;
    };
    if (!request.headers.get("Idempotency-Key")) return invalid("idempotency key is required");
    if (!body.venue || !body.displayName) return invalid("venue and displayName are required");
    if (!body.credentialBundle) return invalid("credential bundle is required");

    // The bundle is destructured away and never reaches storage. `Account` has
    // no field that could carry it back out, which is what makes the write-only
    // rule a property of the response type rather than a habit of this handler.
    const { credentialBundle: _secret, ...record } = body;
    return gateway({ account: accounts.create({venue:record.venue, displayName:record.displayName}) }, 201);
  }),

  http.get("/api/v1/accounts/:accountId", ({ params }) => {
    const account = accounts.find(String(params.accountId));
    return account ? gateway({ account }) : notFound("account");
  }),

  http.delete("/api/v1/accounts/:accountId", ({ params }) => {
    accounts.remove(String(params.accountId));
    return new HttpResponse(null, { status: 204 });
  }),
];

/* --- Anything else -------------------------------------------------------- */

/** Unknown routes must fail, as they do on the auth edge. */
const fallthrough = http.all("/api/v1/*", ({ request }) => {
  // The command collection still has a GET route, matching Auth Edge's 405.
  if (new URL(request.url).pathname === "/api/v1/commands") {
    return failure(405, "METHOD_NOT_ALLOWED", "command collection is read-only");
  }
  return notFound("route");
});

export const handlers = [
  gate,
  ...auth,
  ...system,
  ...catalog,
  ...funding,
  ...trading,
  ...manual,
  ...accountPlane,
  fallthrough,
];
