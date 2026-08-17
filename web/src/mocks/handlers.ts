import { HttpResponse, http } from "msw";

import type {
  Account,
  AccountRoute,
  ArbitrageExecution,
  ArbitrageLegView,
  ArbitragePair,
  ArbitragePreview,
  ArbitragePreviewLeg,
  ArbitragePreviewRequest,
  CandleInterval,
  Command,
  CredentialBundle,
  FundingWindow,
  Instrument,
  ManualCommandRequest,
  ManualPreviewRequest,
} from "@/api/types";

import { accounts, activity, hedges, orders, previews, session } from "./db";
import {
  ARBITRAGE_OPPORTUNITIES,
  CREDENTIALS,
  FILLS,
  FUNDING_BOARD,
  INSTRUMENTS,
  LEDGER,
  META,
  OBSERVED_AT,
  PNL,
  POSITIONS,
  PROJECTION,
  RATE_LIMIT,
  SESSION,
  SYSTEM,
  VENUES,
  VENUE_REVISION,
  candles,
  fundingHistory,
  instrumentOf,
  marketFact,
  runtimeOf,
} from "./fixtures";

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
 *   catalog/*                    {meta, data: T[], projection}
 *   market-data, candles,        {meta, data: T, rateLimit}
 *     funding-board, -history
 *   the trading ledgers          {meta, data: {items: T[]}}
 *   pnl, previews, commands      {meta, data: T}
 *   products/manual/venues       {meta, data: {revision, items}}
 *   products/arbitrage/…         {meta, data: {items, observedAt}}
 *     opportunities
 *   /api/v1/accounts/*           {data: {account | accounts}}   — no meta
 *
 * Two routes in the proxy table have no browser caller: `POST /reservations`
 * and `GET /audit` (which `endpoints.ts` records as being retired). They fall
 * through to the catch-all at the bottom rather than getting invented
 * behaviour, so nothing here implies a contract the frontend does not use.
 * ------------------------------------------------------------------------- */

const REQUEST_ID = META.requestId;

const envelope = <T,>(body: T, status = 200) =>
  HttpResponse.json(body as Record<string, unknown>, { status });

/** `{meta, data}` — a single object. */
const data = <T>(value: T, status = 200) => envelope({ meta: META, data: value }, status);

/** `{meta, data: {items}}` — every trading ledger and projection list. */
const items = <T>(list: T[]) => envelope({ meta: META, data: { items: list } });

/** `{meta, data, projection}` — the catalog reads, which carry a revision. */
const projected = <T>(list: T[]) => envelope({ meta: META, data: list, projection: PROJECTION });

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
 * of its forty entries. Login is the one exemption: it is how the session comes
 * into existence, and it is the only route the auth edge serves unauthenticated.
 *
 * Getting this wrong in the mock would hide the failure mode that matters most
 * — a screen that quietly renders against a session the server would have
 * rejected — so the gate is deliberately the first handler registered.
 */
const gate = http.all("/api/v1/*", ({ request }) => {
  const { pathname } = new URL(request.url);
  if (pathname === "/api/v1/auth/login") return undefined;

  if (!session.open) return failure(401, "AUTH_REQUIRED", "no session");

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
];

/* --- Catalog and market data ---------------------------------------------- */

const catalog = [
  http.get("/api/v1/catalog/venues", () => projected(VENUES)),
  http.get("/api/v1/catalog/instruments", () => projected(INSTRUMENTS)),

  http.get("/api/v1/market-data", ({ request }) => {
    const instrumentId = new URL(request.url).searchParams.get("instrumentId") ?? "";
    const instrument = instrumentOf(instrumentId);
    if (!instrument) return notFound("instrument");
    return metered(marketFact(instrument));
  }),

  http.get("/api/v1/candles", ({ request }) => {
    const query = new URL(request.url).searchParams;
    const instrument = instrumentOf(query.get("instrumentId") ?? "");
    if (!instrument) return notFound("instrument");

    const interval = (query.get("interval") ?? "15m") as CandleInterval;
    const limit = Number(query.get("limit") ?? 200);
    // The server's own bound. A chart that asks for more gets a 400 rather than
    // a silently truncated page, so the clamp has to live in the caller.
    if (!Number.isFinite(limit) || limit < 1 || limit > 500) return invalid("limit must be between 1 and 500");

    return metered({
      venue: instrument.venue,
      environment: instrument.environment,
      product: instrument.product,
      instrumentId: instrument.instrumentId,
      interval,
      candles: candles(instrument, interval, limit),
      observedAt: OBSERVED_AT,
    });
  }),
];

/* --- Funding -------------------------------------------------------------- */

const funding = [
  http.get("/api/v1/funding-board", () => metered(FUNDING_BOARD)),

  http.get("/api/v1/funding-history", ({ request }) => {
    const query = new URL(request.url).searchParams;
    const venue = query.get("venue") ?? "";
    const symbol = query.get("symbol") ?? "";
    const window = (query.get("window") ?? "7d") as FundingWindow;

    // A venue that publishes no history at all is a fact about the venue, not
    // about this moment, which is why it is answered with its own code and
    // `retryable: false`. The board shows okx unavailable for the same reason.
    const column = FUNDING_BOARD.venues.find((entry) => entry.venue === venue);
    if (!column || column.unavailable) {
      return failure(404, "FUNDING_HISTORY_UNSUPPORTED", `${venue || "venue"} publishes no funding history`);
    }

    const series = fundingHistory(venue, symbol, window);
    if (!series) {
      return failure(404, "FUNDING_HISTORY_INSTRUMENT_UNKNOWN", `${venue} does not list ${symbol}`);
    }
    return metered(series);
  }),
];

/* --- Realtime ------------------------------------------------------------- */

const realtime = [
  /**
   * The stream exists in the route table and is declared in `endpoints.ts`, but
   * nothing subscribes to it yet. An open, empty stream is the honest answer:
   * it holds the content type a subscriber would branch on without pushing
   * events the mock has no way to have observed.
   */
  http.get("/api/v1/realtime/stream", () =>
    new HttpResponse("", { status: 200, headers: { "Content-Type": "text/event-stream" } }),
  ),
];

/* --- Trading ledgers and projections -------------------------------------- */

const trading = [
  http.get("/api/v1/reservations", () => items([])),

  // Nothing here holds a reservation, so there is never one to release.
  http.delete("/api/v1/reservations/:reservationId", () => notFound("reservation")),

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

/* --- Manual product ------------------------------------------------------- */

/**
 * The blockers the instrument's own published rules imply, and nothing else.
 *
 * Only two rules can be checked without a venue: the lot floor and the notional
 * floor. Both are read from the same `rules` object the catalog served, so a
 * ticket that renders "min 0.00001" and a preview that rejects 0.000005 are
 * quoting one number rather than two that happen to agree.
 */
function manualBlockers(instrument: Instrument, request: ManualPreviewRequest): string[] {
  const blockers: string[] = [];
  const quantity = num(request.quantity);
  const minQuantity = num(instrument.rules.minQuantity);
  const minNotional = num(instrument.rules.minNotional);

  if (quantity <= 0) blockers.push("QUANTITY_REQUIRED");
  else if (minQuantity > 0 && quantity < minQuantity) blockers.push("BELOW_MIN_QUANTITY");

  const reference =
    request.orderType === "limit" ? num(request.limitPrice) : num(marketFact(instrument).bestAsk);
  if (minNotional > 0 && quantity * reference < minNotional) blockers.push("BELOW_MIN_NOTIONAL");

  return blockers;
}

const manual = [
  // The only route that speaks `{meta, data: {revision, items}}`.
  http.get("/api/v1/products/manual/venues", () =>
    envelope({ meta: META, data: { revision: VENUE_REVISION, items: VENUES } }),
  ),

  http.post("/api/v1/products/manual/execution-previews", async ({ request }) => {
    const body = (await request.json()) as ManualPreviewRequest;

    // A market order carrying a price is a client bug, not a preference: the
    // price would be silently discarded and the operator would believe it was
    // honoured.
    if (body.orderType === "market" && body.limitPrice) {
      return invalid("market order must not carry a price");
    }
    if (body.orderType === "limit" && !body.limitPrice) {
      return invalid("limit order must carry a price");
    }

    const instrument = instrumentOf(body.instrumentId);
    if (!instrument) return notFound("instrument");
    if (!accounts.find(body.accountId)) return notFound("account");

    const previewId = previews.seal(body);
    return data({ previewId, blockers: manualBlockers(instrument, body), warnings: [] }, 201);
  }),

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
      const placed = orders.place({
        previewId: body.previewId,
        lineageId: body.lineageId,
        idempotencyKey: body.idempotencyKey,
      });
      if (!placed) return notFound("execution preview");
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

    // `close` is in the request type but no screen sends it, and the request
    // carries neither a preview nor an order id for it to act on. Answering it
    // would mean inventing which position it meant.
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

/* --- Arbitrage product ---------------------------------------------------- */

const DAILY_INTERVALS = 3;

function previewLeg(accountId: string, instrumentId: string, side: "buy" | "sell", limitPrice?: string) {
  const instrument = instrumentOf(instrumentId);
  const fact = instrument ? marketFact(instrument) : null;
  const rate = fact?.fundingRate;
  const leg: ArbitragePreviewLeg = {
    accountId,
    instrumentId,
    venue: instrument?.venue ?? "",
    environment: instrument?.environment ?? "live",
    symbol: instrument?.symbol,
    side,
    limitPrice,
    rate,
    intervalHours: rate ? 8 : undefined,
    nextFundingTime: rate ? "2026-08-17T16:00:00Z" : undefined,
    markPrice: fact?.markPrice,
    dailyRate: rate ? String(num(rate) * DAILY_INTERVALS) : undefined,
  };
  return leg;
}

function legView(execution: { lineageId: string }, leg: "long" | "short", preview: ArbitragePreview): ArbitrageLegView {
  const source = leg === "long" ? preview.long : preview.short;
  return {
    leg,
    commandId: hedges.nextLegId(),
    lineageId: execution.lineageId,
    accountId: source.accountId,
    instrumentId: source.instrumentId,
    side: source.side,
    quantity: preview.quantity,
    orderType: preview.request.orderType,
    limitPrice: source.limitPrice,
    reduceOnly: false,
    // Submitted, not filled. There is no venue here to have filled against, and
    // reporting a fill the mock never observed is the one thing that would make
    // the arbitrage screens lie about exposure.
    state: "submitted",
    createdAt: OBSERVED_AT,
    updatedAt: OBSERVED_AT,
  };
}

function asPair(execution: ArbitrageExecution): ArbitragePair {
  return {
    lineageId: execution.lineageId,
    state: execution.state,
    legs: execution.legs,
    positions: [],
    funding: [],
    blockers: [],
    updatedAt: OBSERVED_AT,
  };
}

const arbitrage = [
  // `{meta, data: {items, observedAt}}` — this route's shape and no other's.
  http.get("/api/v1/products/arbitrage/opportunities", ({ request }) => {
    const held = new Set(new URL(request.url).searchParams.getAll("accountId"));
    const reachable = (accountId?: string) => !accountId || held.size === 0 || held.has(accountId);
    return envelope({
      meta: META,
      data: {
        items: ARBITRAGE_OPPORTUNITIES.filter(
          (opportunity) => reachable(opportunity.long.accountId) && reachable(opportunity.short.accountId),
        ),
        observedAt: OBSERVED_AT,
      },
    });
  }),

  http.post("/api/v1/products/arbitrage/execution-previews", async ({ request }) => {
    const body = (await request.json()) as ArbitragePreviewRequest;

    if (body.orderType === "market" && (body.long.limitPrice || body.short.limitPrice)) {
      return invalid("market order must not carry a price");
    }

    const long = instrumentOf(body.long.instrumentId);
    const short = instrumentOf(body.short.instrumentId);
    if (!long || !short) return notFound("instrument");
    if (!accounts.find(body.long.accountId) || !accounts.find(body.short.accountId)) return notFound("account");

    const blockers: string[] = [];
    // One account cannot hedge itself: both legs would net inside the same
    // venue position and the pair would carry no exposure at all.
    if (body.long.accountId === body.short.accountId) blockers.push("LEGS_SHARE_ONE_ACCOUNT");

    const quantity = num(body.quantity);
    for (const instrument of [long, short]) {
      const minQuantity = num(instrument.rules.minQuantity);
      if (minQuantity > 0 && quantity < minQuantity) blockers.push("BELOW_MIN_QUANTITY");
    }

    const longLeg = previewLeg(body.long.accountId, body.long.instrumentId, "buy", body.long.limitPrice);
    const shortLeg = previewLeg(body.short.accountId, body.short.instrumentId, "sell", body.short.limitPrice);
    const spread = num(shortLeg.dailyRate) - num(longLeg.dailyRate);
    const notional = quantity * num(marketFact(long).markPrice ?? marketFact(long).bestAsk);

    const preview: ArbitragePreview = {
      previewId: previews.nextArbitrageId(),
      request: body,
      long: longLeg,
      short: shortLeg,
      quantity: body.quantity,
      notional: String(notional),
      spreadDaily: String(spread),
      spreadApr: String(spread * 365),
      dailyFunding: String(spread * notional),
      blockers,
      warnings: [],
    };
    previews.sealArbitrage(preview);
    return data(preview, 201);
  }),

  http.post("/api/v1/products/arbitrage/commands", async ({ request }) => {
    const body = (await request.json()) as {
      previewId?: string;
      action: "open" | "close" | "cancel";
      lineageId: string;
      orderId?: string;
      idempotencyKey: string;
    };

    if (request.headers.get("Idempotency-Key") !== body.idempotencyKey) {
      return invalid("idempotency key header and body disagree");
    }

    if (body.action === "open") {
      const preview = body.previewId ? previews.getArbitrage(body.previewId) : undefined;
      if (!preview) return notFound("execution preview");

      const execution: ArbitrageExecution = {
        lineageId: body.lineageId,
        // Opening, not open: both legs are resting orders. A pair only becomes
        // open once both have filled, and `partially_open` is what one filled
        // leg looks like — states this mock has no way to reach honestly.
        state: "opening",
        legs: [],
        failures: [],
      };
      execution.legs = [
        legView(execution, "long", preview),
        legView(execution, "short", preview),
      ];
      hedges.push(execution);
      return data(execution, 202);
    }

    if (body.action === "close") {
      const pair = hedges.list().find((entry) => entry.lineageId === body.lineageId);
      if (!pair) return notFound("pair");
      const closing: ArbitrageExecution = { ...pair, state: "closing" };
      hedges.push(closing);
      return data(closing, 202);
    }

    // Cancelling one leg answers with that leg's command — the same URL as the
    // two above, with a different body type.
    const pair = hedges.list().find((entry) => entry.legs.some((leg) => leg.commandId === body.orderId));
    const leg = pair?.legs.find((entry) => entry.commandId === body.orderId);
    if (!pair || !leg) return notFound("order");
    leg.state = "canceled";
    leg.updatedAt = OBSERVED_AT;

    const command: Command = {
      commandId: leg.commandId,
      accountId: leg.accountId,
      lineageId: leg.lineageId,
      productId: "arbitrage",
      mode: "live",
      reservationId: null,
      idempotencyKey: body.idempotencyKey,
      commandKind: "cancel",
      state: "canceled",
      payload: { instrumentId: leg.instrumentId, side: leg.side, quantity: leg.quantity },
      blockers: [],
      createdAt: leg.createdAt,
      updatedAt: OBSERVED_AT,
    };
    return data(command, 202);
  }),

  http.get("/api/v1/products/arbitrage/positions", () => items(hedges.list().map(asPair))),
];

/* --- The gateway's account plane ------------------------------------------ */

/**
 * The desired-state handshake, byte for byte as `internal_routes.go` mints it.
 *
 * Absent is 428 rather than 400: the write is well-formed, it simply has not
 * said which version of the account it was built against. Stale is 409, which
 * is the case that actually protects anything — two operators editing the same
 * account from two screens.
 */
const etag = (revision: number) => `"desired-state-revision-${revision}"`;

function precondition(request: Request, account: Account) {
  const header = request.headers.get("If-Match");
  if (!header) return failure(428, "IF_MATCH_REQUIRED", "if-match is required for this write");
  if (header !== etag(account.desiredStateRevision)) {
    return failure(409, "DESIRED_STATE_REVISION_CONFLICT", "the account changed since it was read");
  }
  return null;
}

/** Runs a write that needs a live account and a matching precondition. */
async function guarded(
  request: Request,
  accountId: string,
  apply: (account: Account) => Account | undefined,
  status = 200,
) {
  const account = accounts.find(accountId);
  if (!account) return notFound("account");
  const rejected = precondition(request, account);
  if (rejected) return rejected;
  const updated = apply(account);
  return updated ? gateway({ account: updated }, status) : notFound("account");
}

const accountPlane = [
  http.get("/api/v1/accounts", () => gateway({ accounts: accounts.list() })),

  http.post("/api/v1/accounts", async ({ request }) => {
    const body = (await request.json()) as {
      venue: string;
      displayName: string;
      productPolicy: string[];
      routes: AccountRoute[];
      credentialBundle: CredentialBundle;
    };
    if (!request.headers.get("Idempotency-Key")) return invalid("idempotency key is required");
    if (!body.venue || !body.displayName) return invalid("venue and displayName are required");
    if (!body.routes?.length) return invalid("at least one route is required");
    if (!body.credentialBundle) return invalid("credential bundle is required");

    // The bundle is destructured away and never reaches storage. `Account` has
    // no field that could carry it back out, which is what makes the write-only
    // rule a property of the response type rather than a habit of this handler.
    const { credentialBundle: _secret, ...record } = body;
    return gateway({ account: accounts.create(record) }, 201);
  }),

  http.get("/api/v1/accounts/:accountId", ({ params }) => {
    const account = accounts.find(String(params.accountId));
    return account ? gateway({ account }) : notFound("account");
  }),

  http.patch("/api/v1/accounts/:accountId", async ({ params, request }) => {
    const body = (await request.json()) as { displayName: string; productIds: string[] };
    return guarded(request, String(params.accountId), (account) =>
      accounts.mutate(account.accountId, { displayName: body.displayName, productPolicy: body.productIds }),
    );
  }),

  http.delete("/api/v1/accounts/:accountId", ({ params, request }) => {
    if (!request.headers.get("Idempotency-Key")) return invalid("idempotency key is required");
    // 202: removal is a lifecycle transition the gateway drains in the
    // background, not a row disappearing under the caller.
    return guarded(
      request,
      String(params.accountId),
      (account) => accounts.mutate(account.accountId, { status: "removing" }),
      202,
    );
  }),

  http.post("/api/v1/accounts/:accountId/validate", ({ params, request }) =>
    guarded(request, String(params.accountId), (account) =>
      accounts.mutate(account.accountId, { lastValidatedAt: OBSERVED_AT, status: "active" }),
    ),
  ),

  http.post("/api/v1/accounts/:accountId/rotate", ({ params, request }) => {
    if (!request.headers.get("Idempotency-Key")) return invalid("idempotency key is required");
    return guarded(request, String(params.accountId), (account) => accounts.rotate(account.accountId));
  }),

  http.post("/api/v1/accounts/:accountId/disable", ({ params, request }) =>
    guarded(request, String(params.accountId), (account) =>
      accounts.mutate(account.accountId, { status: "disabled" }),
    ),
  ),

  http.post("/api/v1/accounts/:accountId/drain", ({ params, request }) =>
    guarded(request, String(params.accountId), (account) =>
      accounts.mutate(account.accountId, { status: "draining" }),
    ),
  ),
];

/* --- Anything else -------------------------------------------------------- */

/**
 * An empty trading list rather than a 404.
 *
 * A path that reaches here is one the frontend asked for and this file has not
 * implemented. Answering 404 would make that look like "the resource does not
 * exist", which is a thing screens render explanations for; an empty list makes
 * it look like "nothing to show", which is what an unimplemented read of a
 * ledger honestly is.
 */
const fallthrough = http.all("/api/v1/*", () => items([]));

export const handlers = [
  gate,
  ...auth,
  ...system,
  ...catalog,
  ...funding,
  ...realtime,
  ...trading,
  ...manual,
  ...arbitrage,
  ...accountPlane,
  fallthrough,
];
