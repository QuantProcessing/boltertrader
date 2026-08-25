/* ---------------------------------------------------------------------------
 * The requests the browser makes, and the table they are drawn from.
 *
 * The set of reachable paths is not a design decision made here — it is
 * `services/authedge/proxy/routes.go`, which is the only thing that decides what
 * a browser can talk to. A path absent from that table returns 404 from the auth
 * edge no matter how correct it looks, so `paths` below mirrors it and the
 * functions are written against that mirror.
 *
 * `paths` is the full table; the functions are the subset the UI calls. Two
 * routes are deliberately listed without a wrapper — `POST /api/v1/reservations`
 * and `GET /api/v1/audit`. Trading opens reservations itself as part of placing
 * an order, and the audit route is being retired, so neither has a caller worth
 * inventing a request type for.
 *
 * Two consequences of that table are worth stating outright, because both look
 * like bugs from the frontend:
 *
 *   - Historical market reads arrive through trading. The credential-free
 *     public WebSocket is the one narrow Gateway exception.
 *   - `/api/v1/accounts/{id}/runtimes` is a trading projection while every
 *     other `/api/v1/accounts` path is the gateway's account lifecycle. They
 *     differ by suffix alone. That is why the table is explicit and why these
 *     functions are grouped by owning service rather than by URL prefix.
 *
 * Response envelopes differ per service and per route — seven shapes across
 * this surface. Each function unwraps its own, so nothing above this layer ever
 * writes `.data.items`.
 * ------------------------------------------------------------------------- */

import { apiRequest, advanceSessionEpoch, setCsrfToken, type ResponseMeta } from "./client";
import type {
  Account,
  AccountBalance,
  AccountPositionSnapshot,
  AccountRoute,
  AccountRuntime,
  ActivityEvent,
  ArbitrageCommandRequest,
  ArbitrageExecution,
  ArbitrageOpportunity,
  ArbitragePair,
  ArbitragePreview,
  ArbitragePreviewRequest,
  CandlePage,
  CandleQuery,
  Command,
  CommandProjection,
  CredentialBundle,
  Fill,
  FundingBoardSnapshot,
  FundingHistoryQuery,
  FundingHistorySeries,
  Instrument,
  LedgerEntry,
  ManualCommandRequest,
  ManualPreview,
  ManualPreviewRequest,
  MarketDataQuery,
  MarketFact,
  PnLSnapshot,
  Position,
  RateLimitStatus,
  Reservation,
  SessionState,
  SystemSnapshot,
  Venue,
  CatalogProjectionMeta,
} from "./types";

/* --- Envelopes ----------------------------------------------------------- */

/** `{meta, data}` — the common case. */
interface DataEnvelope<T> {
  meta: ResponseMeta;
  data: T;
}

/** `{meta, data: {items}}` — every trading list route. */
interface ItemsEnvelope<T> {
  meta: ResponseMeta;
  data: { items: T[] };
}

/** `{meta, data: [...], projection}` — catalog, where `data` is a bare array. */
interface ProjectionEnvelope<T> {
  meta: ResponseMeta;
  data: T[];
  projection: CatalogProjectionMeta;
}

/** `{meta, data, rateLimit}` — market data and candles, which spend venue budget. */
interface RateLimitedEnvelope<T> {
  meta: ResponseMeta;
  data: T;
  rateLimit: RateLimitStatus;
}

/**
 * `{data: {accounts | account}}` — the gateway plane, which has NO `meta` key.
 *
 * It is not an oversight to fix here: those routes are served by the gateway's
 * own writer, not by `internal/httpx`, and the auth edge forwards the body
 * untouched. Reading `.meta` off one of these is `undefined`.
 */
interface GatewayEnvelope<T> {
  data: T;
}

/** `{meta, data: {revision, items}}` — the manual product's venue list alone. */
interface RevisionedItemsEnvelope<T> {
  meta: ResponseMeta;
  data: { revision: string; items: T[] };
}

/**
 * `{meta, data: {items, observedAt}}` — the arbitrage opportunity scan alone.
 *
 * The timestamp is the board snapshot every row was ranked against, not the
 * time of the response. Two rows in one payload are therefore comparable with
 * each other, and a stale scan is visible as one number rather than having to
 * be inferred per row.
 */
interface ObservedItemsEnvelope<T> {
  meta: ResponseMeta;
  data: { items: T[]; observedAt: string };
}

/** What a catalog read carries besides its rows. */
export interface Projected<T> {
  items: T[];
  projection: CatalogProjectionMeta;
}

/** What a market read carries besides its value: the venue's remaining budget. */
export interface Metered<T> {
  value: T;
  rateLimit: RateLimitStatus;
}

/* --- Paths --------------------------------------------------------------- */

/**
 * The literal paths, in one place, matching `proxy.routes` exactly.
 *
 * Kept separate from the functions so a route table change can be diffed
 * against the Go table by eye — which is the only check there is, since nothing
 * validates these strings until a request 404s.
 */
export const paths = {
  login: "/api/v1/auth/login",
  session: "/api/v1/auth/session",
  me: "/api/v1/me",

  systemStatus: "/api/v1/system/status",
  systemReadiness: "/api/v1/system/readiness",
  venueConnectivity: "/api/v1/venues/connectivity",

  catalogVenues: "/api/v1/catalog/venues",
  catalogInstruments: "/api/v1/catalog/instruments",
  marketData: "/api/v1/market-data",
  candles: "/api/v1/candles",
  realtimeSocket: "/api/v1/realtime/ws",
  fundingBoard: "/api/v1/funding-board",
  fundingHistory: "/api/v1/funding-history",

  reservations: "/api/v1/reservations",
  reservation: (reservationId: string) => `/api/v1/reservations/${encodeURIComponent(reservationId)}`,
  commands: "/api/v1/commands",
  command: (commandId: string) => `/api/v1/commands/${encodeURIComponent(commandId)}`,

  positions: "/api/v1/positions",
  balances: "/api/v1/balances",
  accountPositions: "/api/v1/account-positions",
  portfolioPositions: "/api/v1/portfolio/positions",
  ledger: "/api/v1/ledger",
  pnl: "/api/v1/pnl",
  fills: "/api/v1/fills",
  activity: "/api/v1/activity",
  audit: "/api/v1/audit",

  accountRuntimes: (accountId: string) => `/api/v1/accounts/${encodeURIComponent(accountId)}/runtimes`,

  manualVenues: "/api/v1/products/manual/venues",
  manualPreviews: "/api/v1/products/manual/execution-previews",
  manualCommands: "/api/v1/products/manual/commands",
  manualOrders: "/api/v1/products/manual/orders",
  manualOrder: (orderId: string) => `/api/v1/products/manual/orders/${encodeURIComponent(orderId)}`,

  arbitrageOpportunities: "/api/v1/products/arbitrage/opportunities",
  arbitragePreviews: "/api/v1/products/arbitrage/execution-previews",
  arbitrageCommands: "/api/v1/products/arbitrage/commands",
  arbitragePositions: "/api/v1/products/arbitrage/positions",

  accounts: "/api/v1/accounts",
  account: (accountId: string) => `/api/v1/accounts/${encodeURIComponent(accountId)}`,
  accountDisable: (accountId: string) => `/api/v1/accounts/${encodeURIComponent(accountId)}/disable`,
} as const;

/* --- Auth ---------------------------------------------------------------- */

/**
 * Signing in and out both move the session boundary, so both advance the epoch:
 * anything still in flight was authorized as someone else and must not land.
 *
 * The CSRF token is captured here rather than by the caller because forgetting
 * it produces a session that reads fine and cannot write — a failure that shows
 * up much later, at the first order, as an unexplained 403.
 */
export const auth = {
  async login(username: string, password: string, signal?: AbortSignal): Promise<SessionState> {
    advanceSessionEpoch();
    const body = await apiRequest<DataEnvelope<SessionState>>(paths.login, {
      method: "POST",
      body: { username, password },
      signal,
    });
    setCsrfToken(body.data.session.csrfToken);
    return body.data;
  },

  /**
   * Who is signed in, according to the cookie the browser cannot read.
   *
   * This is how a reload recovers a session: the cookie survives, the in-memory
   * CSRF token does not, and this is what mints a usable one again.
   */
  async me(signal?: AbortSignal): Promise<SessionState> {
    const body = await apiRequest<DataEnvelope<SessionState>>(paths.me, { signal });
    setCsrfToken(body.data.session.csrfToken);
    return body.data;
  },

  /** 204, no body. The server clears the cookie; we clear everything else. */
  async logout(signal?: AbortSignal): Promise<void> {
    try {
      await apiRequest<void>(paths.session, { method: "DELETE", signal });
    } finally {
      // Even a failed logout must not leave a token behind that suggests the
      // session is still good. Whatever the server did, this client is done
      // with it.
      setCsrfToken(null);
      advanceSessionEpoch();
    }
  },
};

/* --- System -------------------------------------------------------------- */

export const system = {
  /** Always 200, even when degraded. Poll this for the banner. */
  status: async (signal?: AbortSignal): Promise<SystemSnapshot> =>
    (await apiRequest<DataEnvelope<SystemSnapshot>>(paths.systemStatus, { signal })).data,

  /**
   * Answers 503 when not ready, which surfaces as an ApiError rather than a
   * snapshot with `ready: false`. Use `status` for anything that needs to
   * render the degraded state instead of failing on it.
   */
  readiness: async (signal?: AbortSignal): Promise<SystemSnapshot> =>
    (await apiRequest<DataEnvelope<SystemSnapshot>>(paths.systemReadiness, { signal })).data,

  venueConnectivity: async (signal?: AbortSignal): Promise<VenueConnectivity[]> =>
    (await apiRequest<DataEnvelope<VenueConnectivity[]>>(paths.venueConnectivity, { signal })).data,
};

export interface VenueConnectivity {
  venue: string;
  state: "connecting" | "online" | "delayed" | "offline";
  latencyMs: number;
  ewmaLatencyMs: number;
  consecutiveFailures: number;
  lastSuccessAt?: string;
  lastErrorAt?: string;
}

/* --- Catalog and market data --------------------------------------------- */

export const catalog = {
  async venues(signal?: AbortSignal): Promise<Projected<Venue>> {
    const body = await apiRequest<ProjectionEnvelope<Venue>>(paths.catalogVenues, { signal });
    return { items: body.data, projection: body.projection };
  },

  async instruments(signal?: AbortSignal): Promise<Projected<Instrument>> {
    const body = await apiRequest<ProjectionEnvelope<Instrument>>(paths.catalogInstruments, { signal });
    return { items: body.data, projection: body.projection };
  },
};

export const market = {
  /**
   * Top of book plus depth, mark price and funding for one instrument.
   *
   * `instrumentId` is the only query identity. Venue, product and settlement
   * are catalog facts resolved by the server, so callers cannot send a mixed
   * identity whose fields disagree. Any second identity field is rejected.
   */
  async data(query: MarketDataQuery, signal?: AbortSignal): Promise<Metered<MarketFact>> {
    const body = await apiRequest<RateLimitedEnvelope<MarketFact>>(paths.marketData, {
      query: { ...query },
      signal,
    });
    return { value: body.data, rateLimit: body.rateLimit };
  },

  /**
   * Historical bars, ascending by open time, with the newest possibly still
   * forming (`complete: false`).
   *
   * `limit` is the one optional field. Omitting it takes the server's default;
   * sending 0 or anything over 500 is a 400, so the chart must clamp before it
   * asks rather than after it fails.
   */
  async candles(query: CandleQuery, signal?: AbortSignal): Promise<Metered<CandlePage>> {
    const body = await apiRequest<RateLimitedEnvelope<CandlePage>>(paths.candles, {
      query: { ...query },
      signal,
    });
    return { value: body.data, rateLimit: body.rateLimit };
  },
};

/* --- Funding ------------------------------------------------------------- */

export const funding = {
  /**
   * The whole cross-venue funding matrix, in one read, with no parameters.
   *
   * Deliberately no query argument: the server calls `validateQuery(r, nil)` on
   * this route, so a stray `?venue=binance` is a 400 rather than a filter. Every
   * narrowing the board offers — venue chips, search, minimum open interest, the
   * per-period/APR switch — is therefore client-side by construction, which is
   * also what lets them all be instant.
   *
   * The read only copies Trading's local projection. It carries no venue
   * rate-limit budget because no exchange request happens on this path.
   */
  async board(signal?: AbortSignal): Promise<FundingBoardSnapshot> {
    const body = await apiRequest<DataEnvelope<FundingBoardSnapshot>>(paths.fundingBoard, { signal });
    return body.data;
  },

  /**
   * One instrument's settled funding over a named window.
   *
   * The opposite read from the board in every way that matters: one canonical
   * instrument id rather than the whole matrix. The server resolves its venue
   * and native address because no venue offers a bulk history call.
   *
   * `window` is one of two literals. The server will not take a range: every
   * venue's endpoint is paginated, and these are the two spans that come back
   * whole in a single page from all of them.
   *
   * A venue with no history endpoint answers 404 `FUNDING_HISTORY_UNSUPPORTED`
   * with `retryable: false` — a fact about the venue, not about the moment, and
   * the caller is expected to say so rather than retry.
   */
  async history(query: FundingHistoryQuery, signal?: AbortSignal): Promise<Metered<FundingHistorySeries>> {
    const body = await apiRequest<RateLimitedEnvelope<FundingHistorySeries>>(paths.fundingHistory, {
      query: { ...query },
      signal,
    });
    return { value: body.data, rateLimit: body.rateLimit };
  },
};

/* --- Trading ledgers and projections ------------------------------------- */

const items = <T,>(envelope: ItemsEnvelope<T>): T[] => envelope.data.items;

export const trading = {
  positions: async (query: PositionQuery = {}, signal?: AbortSignal): Promise<Position[]> =>
    items(await apiRequest<ItemsEnvelope<Position>>(paths.positions, { query: { ...query }, signal })),

  ledger: async (query: LedgerQuery = {}, signal?: AbortSignal): Promise<LedgerEntry[]> =>
    items(await apiRequest<ItemsEnvelope<LedgerEntry>>(paths.ledger, { query: { ...query }, signal })),

  /** A single aggregate, not a list — this one is a bare `{meta, data}`. */
  pnl: async (query: LedgerQuery = {}, signal?: AbortSignal): Promise<PnLSnapshot> =>
    (await apiRequest<DataEnvelope<PnLSnapshot>>(paths.pnl, { query: { ...query }, signal })).data,

  fills: async (query: FillQuery = {}, signal?: AbortSignal): Promise<Fill[]> =>
    items(await apiRequest<ItemsEnvelope<Fill>>(paths.fills, { query: { ...query }, signal })),

  activity: async (query: ActivityQuery = {}, signal?: AbortSignal): Promise<ActivityEvent[]> =>
    items(await apiRequest<ItemsEnvelope<ActivityEvent>>(paths.activity, { query: { ...query }, signal })),

  /** The trimmed projection: no payload, no execution evidence. */
  commands: async (query: CommandQuery = {}, signal?: AbortSignal): Promise<CommandProjection[]> =>
    items(await apiRequest<ItemsEnvelope<CommandProjection>>(paths.commands, { query: { ...query }, signal })),

  command: async (commandId: string, signal?: AbortSignal): Promise<CommandProjection> =>
    (await apiRequest<DataEnvelope<CommandProjection>>(paths.command(commandId), { signal })).data,

  reservations: async (signal?: AbortSignal): Promise<Reservation[]> =>
    items(await apiRequest<ItemsEnvelope<Reservation>>(paths.reservations, { signal })),

  releaseReservation: async (reservationId: string, signal?: AbortSignal): Promise<Reservation> =>
    (await apiRequest<DataEnvelope<Reservation>>(paths.reservation(reservationId), { method: "DELETE", signal })).data,

  /** Trading's own view of an account's runtime — NOT the gateway's account record. */
  accountRuntimes: async (accountId: string, signal?: AbortSignal): Promise<AccountRuntime[]> =>
    items(await apiRequest<ItemsEnvelope<AccountRuntime>>(paths.accountRuntimes(accountId), { signal })),
};

/** Private account facts projected through trading's principal boundary. */
export const accountData = {
  balances: async (accountId: string, signal?: AbortSignal): Promise<AccountBalance[]> =>
    items(
      await apiRequest<ItemsEnvelope<AccountBalance>>(paths.balances, {
        query: { accountId },
        signal,
      }),
    ),
  positions: async (accountId: string, instrumentId: string, signal?: AbortSignal): Promise<AccountPositionSnapshot> =>
    (
      await apiRequest<DataEnvelope<AccountPositionSnapshot>>(paths.accountPositions, {
        query: { accountId, instrumentId },
        signal,
      })
    ).data,
  portfolioPositions: async (query: PortfolioPositionQuery = {}, signal?: AbortSignal): Promise<AccountPositionSnapshot> =>
    (
      await apiRequest<DataEnvelope<AccountPositionSnapshot>>(paths.portfolioPositions, {
        query: { ...query },
        signal,
      })
    ).data,
};

/**
 * Every optional filter below is an exact-match allow-list on the server: an
 * unknown field, a repeated one, or one present but blank is a 400, not a
 * silently ignored parameter. `buildUrl` drops empty values for that reason.
 */
export interface PositionQuery {
  accountId?: string;
  productId?: string;
}

export interface PortfolioPositionQuery {
  accountId?: string;
  instrumentId?: string;
  venue?: string;
  product?: string;
  status?: string;
}

export interface LedgerQuery {
  productId?: string;
  lineageId?: string;
  accountId?: string;
  instrumentId?: string;
}

export interface FillQuery {
  productId?: string;
  accountId?: string;
  instrumentId?: string;
  lineageId?: string;
}

export interface ActivityQuery {
  kind?: string;
  productId?: string;
  accountId?: string;
  resourceId?: string;
}

export interface CommandQuery {
  productId?: string;
  accountId?: string;
  state?: string;
}

/* --- The manual product -------------------------------------------------- */

/**
 * The order path, and the only one the browser may use to trade.
 *
 * `POST /api/v1/commands` exists and looks general, but trading rejects any
 * body with `productId: "manual"` on it — manual orders must carry a sealed
 * preview, and that route has no way to check one. Do not route orders there.
 */
export const manual = {
  /** `{meta, data: {revision, items}}` — this route's shape and no other's. */
  async venues(signal?: AbortSignal): Promise<{ revision: string; items: Venue[] }> {
    const body = await apiRequest<RevisionedItemsEnvelope<Venue>>(paths.manualVenues, { signal });
    return body.data;
  },

  /**
   * Step one of two: price and validate the intent, and get back a sealed
   * `previewId`.
   *
   * 201, and the seal expires 30 seconds later — `MANUAL_PREVIEW_TTL_MS`. The
   * deadline is inside the token and is not returned, so the countdown starts
   * when this resolves.
   */
  preview: async (request: ManualPreviewRequest, signal?: AbortSignal): Promise<ManualPreview> =>
    (await apiRequest<DataEnvelope<ManualPreview>>(paths.manualPreviews, { method: "POST", body: request, signal })).data,

  /**
   * Step two: act on the sealed preview.
   *
   * One route, three actions, told apart by which identifiers are set — see
   * `ManualCommandRequest`. The returned status varies (202 for a new place,
   * 200 for a replay) but the body is always the full `Command`, so the caller
   * reads `state` rather than the status code.
   *
   * `idempotencyKey` goes in the body and the header both; the server compares
   * them and rejects a mismatch, so they are set from one value here.
   */
  command: async (request: ManualCommandRequest, signal?: AbortSignal): Promise<Command> =>
    (
      await apiRequest<DataEnvelope<Command>>(paths.manualCommands, {
        method: "POST",
        body: request,
        idempotencyKey: request.idempotencyKey,
        signal,
      })
    ).data,

  /** The full records, with payload and execution evidence — unlike `trading.commands`. */
  orders: async (signal?: AbortSignal): Promise<Command[]> =>
    items(await apiRequest<ItemsEnvelope<Command>>(paths.manualOrders, { signal })),

  order: async (orderId: string, signal?: AbortSignal): Promise<Command> =>
    (await apiRequest<DataEnvelope<Command>>(paths.manualOrder(orderId), { signal })).data,
};

/* --- The arbitrage product ----------------------------------------------- */

/** `action: "open"` — spend a sealed preview and place both legs. */
export interface ArbitrageOpenRequest {
  previewId: string;
  lineageId: string;
  idempotencyKey: string;
}

/** `action: "close"` — reduce-only market orders against whatever the pair holds. */
export interface ArbitrageCloseRequest {
  lineageId: string;
  idempotencyKey: string;
}

/** `action: "cancel"` — dispose of one leg, which is what a half-open hedge needs. */
export interface ArbitrageCancelRequest {
  lineageId: string;
  orderId: string;
  idempotencyKey: string;
}

/**
 * The cross-venue hedge: one underlying, two venues, one long and one short.
 *
 * `POST .../commands` is one route with three actions, and the server validates
 * which identifiers may be set for each — `open` needs a `previewId` and no
 * `orderId`, `close` needs neither, `cancel` needs an `orderId` and no
 * `previewId`. Sending the wrong combination is a 400, not a lenient
 * best-effort, so the three are separate functions here and the exclusive
 * fields are not reachable from the wrong one.
 *
 * They also differ in what comes back: `open` and `close` answer with the pair's
 * `ArbitrageExecution`, `cancel` answers with the single leg's `Command`. That
 * is why `cancelLeg` is not folded into `execute` despite sharing a URL.
 */
export const arbitrage = {
  /**
   * The ranked cross-venue spreads, largest first.
   *
   * Reachability is opt-in: pass the account ids the operator holds and each
   * leg comes back carrying the account that can trade it and `executable`
   * true. Pass none and the scan is still complete — every venue pair is
   * ranked, just not executable — which is what makes this readable before any
   * account exists. Naming an account the session does not own does not reveal
   * anything: the lookup is principal-scoped, so it simply leaves that venue
   * unreachable.
   *
   * The server caps the list at 32 ids and rejects a longer one.
   */
  async opportunities(
    accountIds: string[] = [],
    signal?: AbortSignal,
  ): Promise<{ items: ArbitrageOpportunity[]; observedAt: string }> {
    const body = await apiRequest<ObservedItemsEnvelope<ArbitrageOpportunity>>(paths.arbitrageOpportunities, {
      query: { accountId: accountIds },
      signal,
    });
    return body.data;
  },

  /**
   * Step one: price both legs against the funding board and seal the result.
   *
   * 201, and the seal expires 30 seconds later — `ARBITRAGE_PREVIEW_TTL_MS`.
   * A preview with `blockers` is still returned rather than refused, because
   * the reasons are what the operator needs to see; it just cannot be spent.
   */
  preview: async (request: ArbitragePreviewRequest, signal?: AbortSignal): Promise<ArbitragePreview> =>
    (await apiRequest<DataEnvelope<ArbitragePreview>>(paths.arbitragePreviews, { method: "POST", body: request, signal }))
      .data,

  /**
   * Step two: place both legs.
   *
   * 202 when the legs were submitted now, 200 when this is a replay of a
   * submission that already happened — the body is identical either way, so
   * read `state` rather than the status. A `partially_open` result is a
   * success at the HTTP level and a problem at the trading level: one leg
   * stood up and the other did not, and nothing will unwind it automatically.
   */
  open: async (request: ArbitrageOpenRequest, signal?: AbortSignal): Promise<ArbitrageExecution> =>
    submitArbitrageCommand<ArbitrageExecution>({ ...request, action: "open" }, signal),

  /** Closes both legs of a pair with reduce-only market orders. */
  close: async (request: ArbitrageCloseRequest, signal?: AbortSignal): Promise<ArbitrageExecution> =>
    submitArbitrageCommand<ArbitrageExecution>({ ...request, action: "close" }, signal),

  /**
   * Cancels one leg by command id — the way out of `partially_open`.
   *
   * Answers with that leg's `Command`, not the pair, because the other leg is
   * untouched and reporting the pair would suggest otherwise.
   */
  cancelLeg: async (request: ArbitrageCancelRequest, signal?: AbortSignal): Promise<Command> =>
    submitArbitrageCommand<Command>({ ...request, action: "cancel" }, signal),

  /** One row per lineage: both legs, what they still hold, and accrued funding. */
  positions: async (signal?: AbortSignal): Promise<ArbitragePair[]> =>
    items(await apiRequest<ItemsEnvelope<ArbitragePair>>(paths.arbitragePositions, { signal })),
};

/**
 * The one request the three command actions share.
 *
 * `idempotencyKey` goes in the body and the header both; the server compares
 * them and rejects a mismatch, so they are set from one value here — the same
 * rule as the manual product.
 */
async function submitArbitrageCommand<T>(request: ArbitrageCommandRequest, signal?: AbortSignal): Promise<T> {
  const body = await apiRequest<DataEnvelope<T>>(paths.arbitrageCommands, {
    method: "POST",
    body: request,
    idempotencyKey: request.idempotencyKey,
    signal,
  });
  return body.data;
}

/* --- Accounts (the gateway plane) ---------------------------------------- */

/**
 * `httpapi.createAccountRecordRequest`.
 *
 * There is no `nativeEnvironment` field, deliberately: the gateway derives it
 * from the route profiles, so a client that sent one could disagree with the
 * routes it also sent. `routes` is what decides it.
 */
export interface CreateAccountRequest {
  venue: string;
  displayName: string;
  productPolicy: string[];
  routes: AccountRoute[];
  credentialBundle: CredentialBundle;
}

/**
 * The account lifecycle, forwarded to the gateway's INTERNAL plane because it
 * accepts credential material.
 *
 * Nothing here ever returns a secret: the gateway's `PublicAccount` has no
 * field for one. A credential is written once and cannot be read or replaced;
 * replacement is modeled as removing the account and adding a new one.
 *
 * Three protocol facts every mutation below is written around:
 *
 * Credentials are immutable. Create writes them once; changing them means
 * deleting the account and adding it again. Create and remove carry an
 * `Idempotency-Key`; disable is a bodyless state change. Accounts expose no
 * revisions and clients never participate in runtime fencing.
 *
 * Note the envelope: `{data: ...}` with no `meta`.
 */
export const accounts = {
  list: async (signal?: AbortSignal): Promise<Account[]> =>
    (await apiRequest<GatewayEnvelope<{ accounts: Account[] }>>(paths.accounts, { signal })).data.accounts,

  get: async (accountId: string, signal?: AbortSignal): Promise<Account> =>
    (await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.account(accountId), { signal })).data.account,

  /** 201. The only mutation with no revision to match — the account does not exist yet. */
  create: async (request: CreateAccountRequest, idempotencyKey: string, signal?: AbortSignal): Promise<Account> =>
    (
      await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.accounts, {
        method: "POST",
        body: request,
        idempotencyKey,
        signal,
      })
    ).data.account,

  /**
   * 202, not 204: removal is accepted rather than done. The credential is only
   * deleted once the runtime confirms the account is tombstoned, so the account
   * comes back in state `removing` and the caller keeps watching it.
   */
  remove: async (
    accountId: string,
    idempotencyKey: string,
    signal?: AbortSignal,
  ): Promise<Account> =>
    (
      await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.account(accountId), {
        method: "DELETE",
        idempotencyKey,
        signal,
      })
    ).data.account,

  disable: async (accountId: string, signal?: AbortSignal): Promise<Account> =>
    (
      await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.accountDisable(accountId), {
        method: "POST",
        signal,
      })
    ).data.account,

};
