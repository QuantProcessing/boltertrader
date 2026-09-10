/* ---------------------------------------------------------------------------
 * Domain types, mirrored from the Go services.
 *
 * Every type here has exactly one source of truth in the backend, named above
 * it. When a Go struct changes, this file is wrong until it is changed too —
 * there is no code generation and no runtime validation to catch the drift, so
 * the citation is the only thing keeping the two in step. Keep it accurate.
 *
 * Two conventions inherited from the wire and deliberately not "improved":
 *
 *   - Every quantity, price and amount is a STRING. They are decimals on the
 *     server (shopspring/decimal) and parsing them into a JS number would round
 *     a balance. Format them with `lib/format`; never do arithmetic on them.
 *   - Every timestamp is an RFC 3339 string, not a Date. Parse at the point of
 *     display so the raw value stays available for sorting and for showing what
 *     the server actually said.
 * ------------------------------------------------------------------------- */

/* --- Identity ------------------------------------------------------------ */

/** `services/authedge/session.Principal` */
export interface Principal {
  id: string;
  username: string;
}

/** `services/authedge/session.Session` — the token itself is HttpOnly and never appears here. */
export interface SessionInfo {
  csrfToken: string;
  expiresAt: string;
}

/** `services/authedge/session.Result` — the body of login, `/me` and `/auth/session`. */
export interface SessionState {
  principal: Principal;
  session: SessionInfo;
}

/* --- Orders -------------------------------------------------------------- */

/**
 * `orders.State`.
 *
 * All six render distinctly. `uncertain` especially: it means the venue was
 * asked and did not answer, which PRODUCT.md requires be told apart from both
 * "working" and "done" rather than collapsed into either.
 */
export type CommandState =
  | "accepted"
  | "submitted"
  | "uncertain"
  | "filled"
  | "canceled"
  | "failed";

/** The states the server will still act on — `resumableManualState` in `manual_product.go`. */
export const RESUMABLE_STATES: readonly CommandState[] = ["accepted", "submitted", "uncertain"];

export function isResumable(state: CommandState): boolean {
  return RESUMABLE_STATES.includes(state);
}

/** `orders.Blocker` */
export interface Blocker {
  code: string;
}

/** `orders.OrderEventEvidence` */
export interface OrderEventEvidence {
  eventId?: string;
  state?: string;
  resourceVersion?: number;
  occurredAt?: string;
}

/** `orders.FillEvidence` */
export interface FillEvidence {
  fillId?: string;
  accountId?: string;
  instrumentId?: string;
  side?: string;
  quantity?: string;
  price?: string;
  fee?: string;
  feeCurrency?: string;
  settlementCurrency?: string;
  occurredAt?: string;
}

/**
 * `orders.ExecutionEvidence` — what the venue actually told us.
 *
 * Everything is optional because the struct is `omitempty` all the way down: a
 * command in `accepted` has none of it yet. Absence is the normal case, not an
 * error, and the UI must render the gap rather than a zero.
 */
export interface ExecutionEvidence {
  commandId?: string;
  gatewayOrderId?: string;
  clientOrderId?: string;
  state?: string;
  resourceVersion?: number;
  /** Safe normalized venue rejection details, never raw credential material. */
  venueCode?: string;
  venueMessage?: string;
  orderEvents?: OrderEventEvidence[];
  fills?: FillEvidence[];
}

/**
 * `orders.Command` — the full record, returned by the manual write routes and
 * by `GET /api/v1/products/manual/orders`.
 *
 * `payload` is the sealed intent (`instrumentId`, `side`, `quantity`,
 * `orderType`, `limitPrice`, `timeInForce`, `reduceOnly`, `leverage`, `routeId` and the
 * three account fences). It is opaque JSON on the wire; `ManualIntent` below
 * types the manual product's version of it.
 */
export interface Command {
  commandId: string;
  accountId: string;
  lineageId: string;
  productId: string;
  /** Historical association only; new commands have no reservation. */
  reservationId: string | null;
  idempotencyKey: string;
  commandKind: string;
  state: CommandState;
  payload: unknown;
  blockers: Blocker[];
  createdAt: string;
  updatedAt: string;
  executionEvidence?: ExecutionEvidence;
  reconciliation?: { reason: string; nextAttemptAt: string; attemptCount: number };
}

/** The shape `manual.submitPlace` seals into `Command.payload`. */
export interface ManualIntent {
  instrumentId: string;
  side: OrderSide;
  quantity: string;
  orderType: OrderType;
  limitPrice: string;
  timeInForce: string;
  reduceOnly: boolean;
  /** Perpetual-only target applied immediately before the durable order handoff. Zero means unchanged. */
  leverage: number;
  routeId: string;
  /** Internal stale-preview fence; not an account version and not rendered. */
  runtimeId: string;
}

/**
 * `httpapi.CommandProjection` — the trimmed record served by `GET /api/v1/commands`
 * and `GET /api/v1/commands/{commandId}`.
 *
 * It carries no payload and no evidence. When a view needs the intent it has to
 * read the command through the manual product instead.
 */
export interface CommandProjection {
  commandId: string;
  accountId: string;
  lineageId: string;
  productId: string;
  /** Historical association only; new commands have no reservation. */
  reservationId: string | null;
  idempotencyKey: string;
  commandKind: string;
  state: CommandState;
  blockers: Blocker[];
  createdAt: string;
  updatedAt: string;
}

/* --- Manual product ------------------------------------------------------ */

export type OrderSide = "buy" | "sell";
export type OrderType = "limit" | "market";
export type ManualAction = "place" | "cancel";

/**
 * The manual order intent carried by a place command.
 *
 * `limitPrice` must be empty for a market order and a positive decimal for a
 * limit order. Trading validates the shape and catalog rules in the same
 * request that creates the command.
 */
export interface ManualOrderRequest {
  accountId: string;
  instrumentId: string;
  side: OrderSide;
  orderType: OrderType;
  quantity: string;
  limitPrice: string;
  timeInForce: string;
  reduceOnly: boolean;
  /** Perpetual-only target. Spot requests must send zero. */
  leverage: number;
}

/**
 * The command request body.
 *
 * The two actions are distinguished by which fields are present, and the
 * server rejects any other combination with 400:
 *
 *   place  → order set,   orderId empty
 *   cancel → order empty, orderId set
 *
 * `idempotencyKey` must be byte-identical to the `Idempotency-Key` header.
 */
export interface ManualCommandRequest {
  action: ManualAction;
  lineageId: string;
  orderId: string;
  idempotencyKey: string;
  order?: ManualOrderRequest;
}

/* --- Catalog ------------------------------------------------------------- */

/** `catalog.TradeRules` */
export interface TradeRules {
  [rule: string]: unknown;
  priceIncrement?: string;
  quantityIncrement?: string;
  minQuantity?: string;
  minNotional?: string;
}

/**
 * `catalog.CredentialField`.
 *
 * `metadata` decides which half of the credential bundle the value belongs in:
 * a metadata field is an identifier the venue needs to address the account and
 * is stored alongside the record, a non-metadata field is secret material. The
 * UI must not guess this from the key name — Hyperliquid's `accountAddress` and
 * `apiWalletPrivateKey` sit in the same form and only this flag separates them.
 */
export interface CredentialField {
  key: string;
  label: string;
  metadata?: boolean;
  required: boolean;
  valueType: string;
}

/** `catalog.CredentialSchema` — what a venue needs before it will authenticate. */
export interface CredentialSchema {
  schemaVersion: number;
  authKind: string;
  fields: CredentialField[];
}

/**
 * `catalog.RouteProfile` — one tradable lane on a venue.
 *
 * `routeId` is the account-local name for the lane and `routeProfileId` is the
 * registry's identifier for it. Both travel back in `AccountRoute`; the gateway
 * re-resolves the profile and rejects the route if the pair disagrees.
 */
export interface VenueRouteProfile {
  routeId: string;
  product: string;
  settlementAsset?: string;
  routeProfileId: string;
  label: string;
}

/** `catalog.Venue` */
export interface Venue {
  venue: string;
  environment: string;
  nativeEnvironment: string;
  ready: boolean;
  products: string[];
  credentialSchema: CredentialSchema;
  routeProfiles: VenueRouteProfile[];
}

/** `catalog.Instrument` */
export type MarketFamily = "spot" | "venue_perpetual" | "builder_perpetual";

export type AssetClass =
  | "unknown"
  | "crypto"
  | "equity"
  | "commodity"
  | "index"
  | "fx"
  | "rates"
  | "pre_ipo"
  | "rwa_other";

export interface Instrument {
  instrumentId: string;
  venue: string;
  environment: string;
  product: string;
  settlement?: string;
  symbol: string;
  baseAsset: string;
  quoteAsset: string;
  marketFamily: MarketFamily;
  assetClass: AssetClass;
  classificationSource?: string;
  rawAssetType?: string;
  rawCategories?: string[];
  rules: TradeRules;
  ready: boolean;
}

/** Revision and observation time from the Gateway catalog snapshot. */
export interface CatalogProjectionMeta {
  revision: string;
  observedAt: string;
}

/* --- Market data --------------------------------------------------------- */

/** `marketdata.Level` */
export interface DepthLevel {
  price: string;
  quantity: string;
}

/** `marketdata.Fact` */
export interface MarketFact {
  venue: string;
  environment: string;
  product: string;
  instrumentId: string;
  bestBid: string;
  bestBidSize: string;
  bestAsk: string;
  bestAskSize: string;
  bids: DepthLevel[];
  asks: DepthLevel[];
  markPrice?: string;
  fundingRate?: string;
  openInterest?: string;
  sourceSequence?: string;
  fundingObservedAt?: string;
  fundingTime?: string;
  nextFundingTime?: string;
  observedAt: string;
}

export interface MarketDataQuery {
  instrumentId: string;
}

/**
 * `marketdata.BudgetStatus` — the `rateLimit` sibling that `market-data` and
 * `candles` return beside `data`. It is the venue's remaining allowance, not
 * ours, which is why a chart that reloads on every keystroke can exhaust it.
 */
export interface RateLimitStatus {
  limit: number;
  used: number;
  remaining: number;
  windowEnds: string;
}

/* --- Candles ------------------------------------------------------------- */

/**
 * The intervals the Gateway accepts, and the only ones it accepts.
 *
 * Venues do not agree on spelling — OKX writes `1H`, Binance writes `1h` — so
 * the Gateway enforces this portable set on both planes and rejects anything
 * else with a 400. Widening it here without widening it there produces a
 * request that always fails.
 */
export const CANDLE_INTERVALS = ["1m", "5m", "15m", "30m", "1h", "4h", "12h", "1d"] as const;

export type CandleInterval = (typeof CANDLE_INTERVALS)[number];

/**
 * `marketdata.Candle`. Decimals stay strings the whole way through, as
 * everywhere else on this surface; the chart adapter is the one place that
 * parses them, because that is where precision loss stops being a lie about
 * the data and becomes a pixel.
 */
export interface Candle {
  openTime: string;
  closeTime: string;
  open: string;
  high: string;
  low: string;
  close: string;
  volume: string;
  /** False for the bar still forming, which is the only one that moves. */
  complete: boolean;
}

/** `marketdata.CandlePage`. Candles arrive in strictly ascending open time. */
export interface CandlePage {
  venue: string;
  environment: string;
  product: string;
  instrumentId: string;
  interval: CandleInterval;
  candles: Candle[];
  observedAt: string;
}

/** `limit` is optional; absent means the Gateway's default screenful. */
export interface CandleQuery extends MarketDataQuery {
  interval: CandleInterval;
  limit?: number;
}

/** The bound the Gateway and trading both enforce — `marketdata.MaxCandles`. */
export const MAX_CANDLES = 500;

/* --- Funding board ------------------------------------------------------- */

/**
 * `marketdata.BoardRow` — one instrument's funding on one venue.
 *
 * Almost every field is optional, and that is the point rather than laxity: the
 * Go side carries `omitempty` on each of these because several of the nine funding
 * venues leave open interest out of their bulk call, and two publish a bare rate
 * with no settlement clock. An absent cell arrives absent, and the matrix has to
 * render it as "—" rather than as a zero the venue never quoted.
 *
 * `rate` is the rate for one interval, not annualised. Converting it to APR
 * needs `intervalHours`, which is itself optional — a row without one can be
 * shown per-period but cannot honestly be shown per-year.
 */
export interface FundingBoardRow {
  instrumentId?: string;
  symbol: string;
  baseAsset?: string;
  quoteAsset?: string;
  marketFamily: MarketFamily;
  assetClass: AssetClass;
  classificationSource?: string;
  rawAssetType?: string;
  rawCategories?: string[];
  rate?: string;
  intervalHours?: number;
  nextFundingTime?: string;
  markPrice?: string;
  indexPrice?: string;
  /** Quote/USD notional; see openInterestNotionalAsset for its denomination. */
  openInterestNotional?: string;
  /** The stablecoin denomination of openInterestNotional, normally USDT/USDC. */
  openInterestNotionalAsset?: string;
  /** Venue-native base units or contracts; never use for cross-venue sorting. */
  openInterest?: string;
  fundingObservedAt?: string;
  markObservedAt?: string;
  indexObservedAt?: string;
  priceObservedAt?: string;
  openInterestObservedAt?: string;
  staleFields?: Array<"funding" | "mark" | "index" | "open_interest">;
  missingFields?: Array<"funding" | "mark" | "index" | "open_interest">;
}

/**
 * `marketdata.BoardVenue` — one column.
 *
 * `stale` and `unavailable` answer different questions. Stale means these are
 * the venue's last known rates and older than the rest of the table; unavailable
 * means the feed itself is unavailable. Rows may retain their last known values
 * with staleFields so the outage does not erase diagnostic context. Neither may be
 * rendered as an ordinary empty column: a spread computed against a column that
 * silently vanished is a spread against a venue the operator never chose.
 */
export interface FundingBoardVenue {
  venue: string;
  environment: string;
  product: string;
  settlement?: string;
  routeProfileId: string;
  observedAt?: string;
  stale: boolean;
  unavailable: boolean;
  status: "live" | "degraded" | "unavailable";
  lastSuccessAt?: string;
  reconnects?: number;
  rows: FundingBoardRow[];
}

/**
 * `marketdata.BoardSnapshot` — the whole cross-venue matrix in one read.
 *
 * Unlike every other market read on this surface it takes no query at all: the
 * server rejects any query string with a 400, because there is exactly one board
 * and every visitor asks for it identically. That is also why it is cached and
 * single-flighted server-side, and why polling it faster here would buy nothing.
 */
export interface FundingBoardSnapshot {
  observedAt: string;
  venues: FundingBoardVenue[];
}

/* --- Funding history ----------------------------------------------------- */

/**
 * The two windows the server will serve, and the only two.
 *
 * A name rather than a pair of instants because every venue's history endpoint
 * is paginated, and these are the two spans that fit in one page everywhere on
 * the board. Anything else is a 400 — the server refuses to truncate silently,
 * because a funding curve missing its middle looks exactly like a complete one.
 */
export const FUNDING_WINDOWS = ["7d", "30d"] as const;

export type FundingWindow = (typeof FUNDING_WINDOWS)[number];

/**
 * `marketdata.FundingHistoryPoint` — one settlement.
 *
 * `rate` is for one interval, not annualised, exactly as on the board; and
 * `intervalHours` is optional for the same reason it is optional there, so a
 * point without one can be drawn per-period but cannot honestly be annualised.
 */
export interface FundingHistoryPoint {
  fundingTime: string;
  rate: string;
  intervalHours?: number;
}

/**
 * `marketdata.FundingHistorySeries` — one venue's settled funding over a window.
 *
 * The series carries its own identity and bounds rather than only its points:
 * two of these are drawn together as the legs of one spread, and a response that
 * arrived under the wrong venue or over a narrower span would render as a
 * plausible curve rather than as a visible gap. `points` are strictly ascending
 * in `fundingTime`.
 */
export interface FundingHistorySeries {
  instrumentId: string;
  venue: string;
  since: string;
  until: string;
  observedAt: string;
  points: FundingHistoryPoint[];
}

/** Every field required; the server rejects an unknown fifth outright. */
export interface FundingHistoryQuery {
  instrumentId: string;
  window: FundingWindow;
}

/**
 * The code a venue that publishes no history at all answers with.
 *
 * It arrives as a 404 with `retryable: false`, which is what stops the shared
 * retry policy from spinning on a chart that can never exist. One board column
 * is in this state permanently, so the surface has to name the condition rather
 * than render it as a failed read.
 */
export const FUNDING_HISTORY_UNSUPPORTED = "FUNDING_HISTORY_UNSUPPORTED";

/**
 * The code a venue that publishes history, but not for this symbol, answers with.
 *
 * Also a 404 with `retryable: false`, and kept apart from the code above because
 * only that one is a claim about the venue. A leg that reported "this venue
 * publishes no funding history" because the venue happens not to list one
 * underlying would be telling the operator something false about every other
 * pair on the same venue.
 */
export const FUNDING_HISTORY_INSTRUMENT_UNKNOWN = "FUNDING_HISTORY_INSTRUMENT_UNKNOWN";

/* --- Portfolio and ledger ------------------------------------------------ */

/** `portfolio.Position`. A negative `quantity` is short. */
export interface Position {
  accountingStatus?: "ready" | "legacy_fee_review";
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  settlementCurrency: string;
  quantity: string;
  averageEntryPrice: string;
  markPrice: string;
  realizedPnl: string;
  unrealizedPnl: string;
  updatedAt: string;
}

/** Live account balance projected through trading for one owned account. */
export interface AccountBalance {
  accountId: string;
  product: string;
  settlementAsset?: string;
  asset: string;
  available: string;
  availableMargin?: string;
  locked: string;
  total: string;
}

export type PositionFreshness = "syncing" | "live" | "delayed" | "stale" | "unavailable";
export type PositionRouteHealth = "live" | "degraded" | "unavailable";
export type PositionSlot = "net" | "long" | "short";

/** Venue-authoritative position for one real account route. */
export interface AccountPosition {
  accountId: string;
  routeId?: string;
  instrumentId: string;
  positionSlot?: PositionSlot;
  accountLabel?: string;
  venue?: string;
  environment?: string;
  product?: string;
  settlementAsset?: string;
  symbol?: string;
  side?: OrderSide;
  quantity: string;
  entryPrice: string;
  markPrice: string;
  markPriceSource?: "venue_position" | "venue_public" | "estimated" | "unavailable";
  unrealizedPnl: string;
  unrealizedPnlCurrency?: string;
  unrealizedPnlSource?: "venue_position" | "estimated" | "unavailable";
  liquidationPrice?: string;
  leverage?: string;
  marginUsed?: string;
  marginMode?: string;
  routeHealth?: PositionRouteHealth;
  freshness?: PositionFreshness;
  writeReady?: boolean;
  blockedReason?: string;
  resourceVersion?: number;
  observedAt?: string;
  verifiedAt?: string;
  receivedAt?: string;
}

export interface AccountPositionState {
  accountId: string;
  label: string;
  venue: string;
  environment: string;
  status: string;
  routeHealth: PositionRouteHealth;
  freshness: PositionFreshness;
  writeReady: boolean;
  reason?: string;
  verifiedAt?: string;
  receivedAt: string;
}

export interface PositionSettlementSummary {
  settlementAsset: string;
  positionCount: number;
  liveCount: number;
  staleCount: number;
  unrealizedPnl?: string;
  excludedPnlCount: number;
}

export interface AccountPositionSnapshot {
  projectionRevision: number;
  verifiedAt?: string;
  summary: {
    positionCount: number;
    accountCount: number;
    staleAccountCount: number;
    bySettlementAsset: PositionSettlementSummary[];
  };
  accounts: AccountPositionState[];
  items: AccountPosition[];
}

/** `ledger.Entry` */
export interface LedgerEntry {
	accountingStatus?: "ready" | "legacy_fee_review";
  entryId: string;
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  kind: string;
  amount: string;
  currency: string;
  occurredAt: string;
}

/** `ledger.PnLSnapshot` */
export interface PnLSnapshot {
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  byCurrency: Array<{
    currency: string;
    realizedPnl: string;
    fees: string;
    funding: string;
    bookedNetPnl: string;
    unrealizedPnl?: string;
    netPnl?: string;
  }>;
  asOf: string;
  valuationAsOf?: string;
}

/** `httpapi.FillProjection` */
export interface Fill {
  fillId: string;
  commandId: string;
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  side: OrderSide;
  quantity: string;
  price: string;
  fee: string;
  feeCurrency: string;
  settlementCurrency?: string;
  venueSequence: number | null;
  occurredAt: string;
}

/* --- Activity ------------------------------------------------------------ */

/**
 * `httpapi.ActivityEvent`.
 *
 * The three scope fields are nullable pointers on the server: an event that is
 * not about a product genuinely has no product, and rendering that as an empty
 * string would make it indistinguishable from one whose product is unknown.
 */
export interface ActivityEvent {
  eventId: string;
  sequence: number;
  kind: string;
  productId: string | null;
  accountId: string | null;
  resourceId: string | null;
  payload: unknown;
  observedAt: string;
}

/* --- Accounts ------------------------------------------------------------ */


/** A saved account has already passed its initial balance check. */
export type AccountStatus = "active";

/** Adapter routes derived by Gateway */
export interface AccountRoute {
  routeId: string;
  product: string;
  settlementAsset?: string;
  routeProfileId: string;
}

/**
 * What the gateway accepts as credential material.
 *
 * The shape is fixed by `validateCredentialBundle` in
 * `gateway/httpapi/account_registration.go`: five top-level keys, and inside
 * `metadata` / `secrets` only the keys the credential decoder knows. Anything
 * else is a 400 that deliberately does not say which field was wrong, so the
 * UI has to build this from the venue's own `CredentialSchema` rather than
 * from what an operator typed.
 *
 * A value of this type is live secret material. It exists for exactly as long
 * as one request takes: never stored, never logged, never put in query state.
 */
export interface CredentialBundle {
  schemaVersion: number;
  venue: string;
  authKind: string;
  /**
   * BundleV1 requires both nested objects on the wire. API-key venues have no
   * metadata fields, but their valid representation is still `metadata: {}`
   * rather than an absent value.
   */
  // Identifier metadata is usually textual, but some venue protocols use
  // JSON numbers. Lighter's accountIndex/keyIndex must remain numbers for the
  // gateway's strict credential decoder.
  metadata: Record<string, string | number>;
  secrets: Record<string, string>;
}

/**
 * Saved account metadata served by Gateway.
 *
 * Note there is no credential material anywhere in this type, by construction:
 * the gateway never returns a secret it was given. PRODUCT.md's write-only rule
 * is enforced on the server, and this type is the proof of it.
 */
export interface Account {
  accountId: string;
  venue: string;
  nativeEnvironment: string;
  displayName: string;
  routes?: AccountRoute[];
  status: AccountStatus;
  lastValidatedAt: string | null;
  updatedAt: string;
}

/** `httpapi.ProjectionReadiness` */
export interface ProjectionReadiness {
  ready: boolean;
  reason?: string;
}

/** A local exchange connection, not a permission grant. */
export interface AccountRuntime {
 accountId: string;
 connected: boolean;
 observedAt: string;
}

/* --- System -------------------------------------------------------------- */

/** `internal/system.Component` */
export interface SystemComponent {
  ready: boolean;
  reason: string;
  retryAfterSeconds?: number;
  details?: Record<string, unknown>;
  observedAt?: string;
}

/**
 * `internal/system.Snapshot`.
 *
 * `GET /api/v1/system/readiness` answers 503 when `ready` is false, so the
 * client surfaces it as an error. `GET /api/v1/system/status` always answers
 * 200 and is the one to poll for a degraded-but-serving banner.
 */
export interface SystemSnapshot {
  ready: boolean;
  components: Record<string, SystemComponent>;
}
