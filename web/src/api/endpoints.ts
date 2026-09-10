/* ---------------------------------------------------------------------------
 * The requests the browser makes, and the table they are drawn from.
 *
 * The set of reachable paths is not a design decision made here — it is
 * `services/authedge/proxy/routes.go`, which is the only thing that decides what
 * a browser can talk to. A path absent from that table returns 404 from the auth
 * edge no matter how correct it looks, so `paths` below mirrors it and the
 * functions are written against that mirror.
 *
 * `paths` lists browser routes; the functions cover their current callers.
 *
 * Two consequences of that table are worth stating outright, because both look
 * like bugs from the frontend:
 *
 *   - Market reads are owned by Gateway. The credential-free
 *     public WebSocket is the one narrow Gateway exception.
 *   - `/api/v1/accounts/{id}/runtimes` is a trading projection while every
 *     other `/api/v1/accounts` path is Gateway account registration. They
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
  AccountRuntime,
  ActivityEvent,
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
  MarketDataQuery,
  MarketFact,
  PnLSnapshot,
  Position,
  RateLimitStatus,
  SessionState,
  SystemSnapshot,
  Venue,
  CatalogProjectionMeta,
} from "./types";

/* --- Envelopes ----------------------------------------------------------- */

/** `{data}` with optional Trading metadata. */
interface DataEnvelope<T> {
  meta?: ResponseMeta;
  data: T;
}

/** `{meta, data: {items}}` — every trading list route. */
interface ItemsEnvelope<T> {
  meta: ResponseMeta;
  data: { items: T[] };
}

/** Public market reads. Gateway may expose a venue budget when one is meaningful. */
interface RateLimitedEnvelope<T> {
  meta?: ResponseMeta;
  data: T;
  rateLimit?: RateLimitStatus;
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


/** What a catalog read carries besides its rows. */
export interface Projected<T> {
  items: T[];
  projection: CatalogProjectionMeta;
}

/** What a market read carries besides its value. */
export interface Metered<T> {
  value: T;
  rateLimit?: RateLimitStatus;
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

  catalogInstruments: "/api/v1/catalog/instruments",
  marketData: "/api/v1/market-data",
  candles: "/api/v1/candles",
  marketRealtimeSocket: "/api/v1/market/realtime/ws",
  accountRealtimeSocket: "/api/v1/account/realtime/ws",
  fundingBoard: "/api/v1/funding-board",
  fundingHistory: "/api/v1/funding-history",

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
  manualCommands: "/api/v1/products/manual/commands",
  manualOrders: "/api/v1/products/manual/orders",
  manualOrder: (orderId: string) => `/api/v1/products/manual/orders/${encodeURIComponent(orderId)}`,


  accounts: "/api/v1/accounts",
  account: (accountId: string) => `/api/v1/accounts/${encodeURIComponent(accountId)}`,
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
  async instruments(signal?: AbortSignal): Promise<Projected<Instrument>> {
    const body = await apiRequest<DataEnvelope<{ revision: string; observedAt: string; instruments: Instrument[] }>>(paths.catalogInstruments, { signal });
    const snapshot = body.data;
    if (!snapshot || !Array.isArray(snapshot.instruments) || !snapshot.revision || !Number.isFinite(Date.parse(snapshot.observedAt))) {
      throw new Error("Invalid Gateway instrument catalog");
    }
    return { items: snapshot.instruments, projection: { revision: snapshot.revision, observedAt: snapshot.observedAt } };
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
   * Gateway answers from its resident derivative-fact projection. No Trading
   * cache or second venue request is involved.
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
   * `window` is one of two literals. The UI offers two windows; the API receives their exact date range. Each
   * venue's endpoint is paginated, and these are the two spans that come back
   * whole in a single page from all of them.
   *
   * A venue with no history endpoint answers 404 `FUNDING_HISTORY_UNSUPPORTED`
   * with `retryable: false` — a fact about the venue, not about the moment, and
   * the caller is expected to say so rather than retry.
   */
  async history(query: FundingHistoryQuery, signal?: AbortSignal): Promise<Metered<FundingHistorySeries>> {
    const until = new Date(Math.floor(Date.now() / 1000) * 1000);
    const since = new Date(until.getTime() - (query.window === "7d" ? 7 : 30) * 86_400_000);
    const body = await apiRequest<RateLimitedEnvelope<FundingHistorySeries>>(paths.fundingHistory, {
      query: { instrumentId: query.instrumentId, since: since.toISOString(), until: until.toISOString() },
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
 * The generic command collection is read-only. Manual orders pass through
 * the account, catalog, route and runtime checks in the manual application.
 */
export const manual = {
  /** `{meta, data: {revision, items}}` — this route's shape and no other's. */
  async venues(signal?: AbortSignal): Promise<{ revision: string; items: Venue[] }> {
    const body = await apiRequest<RevisionedItemsEnvelope<Venue>>(paths.manualVenues, { signal });
    return body.data;
  },

  /**
   * Submit a manual order or act on an existing command. One route, two
   * actions, told apart by which fields are set, see
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
  credentialBundle: CredentialBundle;
}

/** Account credentials are accepted only on creation and never returned.
 * Add validates synchronously and uses an idempotency key; DELETE is bodyless.
 * Replacing credentials means deleting the local account and adding a new one.
 */
export const accounts = {
  list: async (signal?: AbortSignal): Promise<Account[]> =>
    (await apiRequest<GatewayEnvelope<{ accounts: Account[] }>>(paths.accounts, { signal })).data.accounts,

  get: async (accountId: string, signal?: AbortSignal): Promise<Account> =>
    (await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.account(accountId), { signal })).data.account,

  /** 201. A saved account whose balance query succeeded. */
  create: async (request: CreateAccountRequest, idempotencyKey: string, signal?: AbortSignal): Promise<Account> =>
    (
      await apiRequest<GatewayEnvelope<{ account: Account }>>(paths.accounts, {
        method: "POST",
        body: request,
        idempotencyKey,
        signal,
      })
    ).data.account,

  /** Delete local credentials and disconnect; exchange orders and positions remain. */
  remove: async (accountId: string, signal?: AbortSignal): Promise<void> => {
    await apiRequest<void>(paths.account(accountId), { method: "DELETE", signal });
  },
};
