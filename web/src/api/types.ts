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
  /** Governs the entire session: a test principal cannot reach live routes. */
  mode: Mode;
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

/** `orders.Mode`. The whole vocabulary. */
export type Mode = "test" | "live";

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
  orderEvents?: OrderEventEvidence[];
  fills?: FillEvidence[];
}

/**
 * `orders.Command` — the full record, returned by the manual write routes and
 * by `GET /api/v1/products/manual/orders`.
 *
 * `payload` is the sealed intent (`instrumentId`, `side`, `quantity`,
 * `orderType`, `limitPrice`, `timeInForce`, `reduceOnly`, `routeId` and the
 * three account fences). It is opaque JSON on the wire; `ManualIntent` below
 * types the manual product's version of it.
 */
export interface Command {
  commandId: string;
  accountId: string;
  lineageId: string;
  productId: string;
  mode: Mode;
  reservationId: string | null;
  idempotencyKey: string;
  commandKind: string;
  state: CommandState;
  payload: unknown;
  blockers: Blocker[];
  createdAt: string;
  updatedAt: string;
  executionEvidence?: ExecutionEvidence;
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
  routeId: string;
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
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
  mode: Mode;
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
export type ManualAction = "place" | "cancel" | "close";

/**
 * The preview request body.
 *
 * `manualPreviewRequest` carries no json tags, so Go matches these names
 * case-insensitively. camelCase is what the rest of the API uses and what the
 * server's own tests send; do not "correct" it to PascalCase.
 *
 * `limitPrice` must be empty for a market order and a positive decimal for a
 * limit order — `validateManualPreview` rejects anything else with 400.
 */
export interface ManualPreviewRequest {
  accountId: string;
  instrumentId: string;
  side: OrderSide;
  orderType: OrderType;
  quantity: string;
  limitPrice: string;
  timeInForce: string;
  reduceOnly: boolean;
  /** Must equal the session principal's mode or the server answers 400. */
  mode: Mode;
}

/**
 * The preview response.
 *
 * The server echoes the request back beside these three fields, but under
 * PascalCase keys — the embedded struct has no tags either. We ignore the echo
 * entirely: the request is already in hand, and consuming a key whose casing is
 * an accident of Go's default marshalling is how a rename breaks the UI.
 */
export interface ManualPreview {
  previewId: string;
  blockers: string[];
  warnings: string[];
}

/**
 * How long a sealed preview stays valid: `manualPreviewTTL` in
 * `services/trading/httpapi/manual_product.go`.
 *
 * The deadline is sealed inside the token and is NOT returned, so the countdown
 * is computed client-side from the moment the response arrived. That makes it a
 * lower bound — network latency is spent before the clock starts, never after —
 * which is the safe direction to be wrong in.
 */
export const MANUAL_PREVIEW_TTL_MS = 30_000;

/**
 * The command request body.
 *
 * The three actions are distinguished by which identifiers are present, and the
 * server rejects any other combination with 400:
 *
 *   place  → previewId set,   orderId empty
 *   cancel → previewId empty, orderId set
 *   close  → both empty       (matched to a position by lineageId)
 *
 * `idempotencyKey` must be byte-identical to the `Idempotency-Key` header.
 */
export interface ManualCommandRequest {
  previewId: string;
  action: ManualAction;
  lineageId: string;
  orderId: string;
  idempotencyKey: string;
}

/* --- Catalog ------------------------------------------------------------- */

/** `catalog.TradeRules` */
export interface TradeRules {
  [rule: string]: unknown;
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
export interface Instrument {
  instrumentId: string;
  venue: string;
  environment: string;
  product: string;
  settlement?: string;
  symbol: string;
  baseAsset: string;
  quoteAsset: string;
  rules: TradeRules;
  ready: boolean;
}

/** The `projection` sibling that `catalog/*` returns beside `data`. */
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
  observedAt: string;
}

export interface MarketDataQuery {
  venue: string;
  environment: string;
  product: string;
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
 * Go side carries `omitempty` on each of these because seven of the ten funding
 * venues leave open interest out of their bulk call, and two publish a bare rate
 * with no settlement clock. An absent cell arrives absent, and the matrix has to
 * render it as "—" rather than as a zero the venue never quoted.
 *
 * `rate` is the rate for one interval, not annualised. Converting it to APR
 * needs `intervalHours`, which is itself optional — a row without one can be
 * shown per-period but cannot honestly be shown per-year.
 */
export interface FundingBoardRow {
  symbol: string;
  nativeSymbol?: string;
  nativeId?: string;
  baseAsset?: string;
  quoteAsset?: string;
  rate: string;
  intervalHours?: number;
  nextFundingTime?: string;
  markPrice?: string;
  indexPrice?: string;
  openInterest?: string;
}

/**
 * `marketdata.BoardVenue` — one column.
 *
 * `stale` and `unavailable` answer different questions. Stale means these are
 * the venue's last known rates and older than the rest of the table; unavailable
 * means there is nothing recent enough to put beside another venue's number, and
 * the server guarantees such a column carries no rows at all. Neither may be
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
  mode: Mode;
  observedAt: string;
  venues: FundingBoardVenue[];
}

/* --- Portfolio and ledger ------------------------------------------------ */

/** `portfolio.Position`. A negative `quantity` is short. */
export interface Position {
  lineageId: string;
  productId: string;
  mode: Mode;
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

/** `ledger.Entry` */
export interface LedgerEntry {
  entryId: string;
  mode: Mode;
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
  pnlSnapshotId: string;
  lineageId: string;
  productId: string;
  accountId: string;
  instrumentId: string;
  realizedPnl: string;
  unrealizedPnl: string;
  fees: string;
  funding: string;
  netPnl: string;
  asOf: string;
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
  cursor: string;
  kind: string;
  productId: string | null;
  accountId: string | null;
  resourceId: string | null;
  payload: unknown;
  observedAt: string;
}

/** `realtime.Event`, as delivered over SSE. Same shape, different transport. */
export interface RealtimeEvent {
  eventId: string;
  sequence: number;
  cursor: string;
  kind: string;
  observedAt: string;
  payload: unknown;
}

/* --- Accounts ------------------------------------------------------------ */

/** `lifecycle.RouteState` */
export type RouteState = "active" | "disabled";

/**
 * `lifecycle.State`. The whole vocabulary, from `gateway/account/lifecycle/state.go`.
 *
 * Transitions are linear and irreversible except for the rotation and drain
 * excursions, which is why the UI shows the state rather than deriving a
 * "healthy / unhealthy" boolean from it: `draining` and `disabled` are both
 * deliberate operator choices, not faults.
 */
export type AccountStatus =
  | "creating"
  | "active"
  | "rotating"
  | "draining"
  | "drained"
  | "disabled"
  | "removing"
  | "removed";

/** `lifecycle.AccountRoute` */
export interface AccountRoute {
  routeId: string;
  product: string;
  settlementAsset?: string;
  routeProfileId: string;
  requestedState: RouteState;
}

/**
 * What the gateway accepts as credential material.
 *
 * The shape is fixed by `validateCredentialBundle` in
 * `gateway/httpapi/account_lifecycle.go`: five top-level keys, and inside
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
  metadata?: Record<string, string>;
  secrets?: Record<string, string>;
}

/**
 * `lifecycle.PublicAccount`, served by the gateway's account plane.
 *
 * Note there is no credential material anywhere in this type, by construction:
 * the gateway never returns a secret it was given. PRODUCT.md's write-only rule
 * is enforced on the server, and this type is the proof of it.
 */
export interface Account {
  accountId: string;
  venue: string;
  mode: Mode;
  nativeEnvironment: string;
  displayName: string;
  productPolicy: string[];
  routes?: AccountRoute[];
  status: AccountStatus;
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  lastValidatedAt: string | null;
  updatedAt: string;
}

/** `httpapi.ProjectionReadiness` */
export interface ProjectionReadiness {
  ready: boolean;
  reason?: string;
}

/** `httpapi.CapabilityOperations` — what the venue lets this account actually do. */
export interface CapabilityOperations {
  place: boolean;
  amend: boolean;
  cancel: boolean;
  openOrders: boolean;
  orderHistory: boolean;
  fills: boolean;
  balances: boolean;
  account: boolean;
  positions: boolean;
  leverage: boolean;
  privateStream: boolean;
  marginMode: boolean;
  positionMode: boolean;
  fundsTransfer: boolean;
}

/** `httpapi.AccountCapabilityProjection` */
export interface AccountCapability {
  rowId: string;
  venue: string;
  product: string;
  settlement?: string;
  writeReady: boolean;
  operations: CapabilityOperations;
}

/** `httpapi.RouteRuntimeProjection` */
export interface RouteRuntime {
  routeId: string;
  product: string;
  settlementAsset?: string;
  routeProfileId: string;
  requestedState: string;
  ready: boolean;
  reason?: string;
}

/** `httpapi.RuntimeSnapshotProjection` */
export interface RuntimeSnapshot {
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  requestedState: string;
  routes: RouteRuntime[];
}

/** `httpapi.PendingRuntimeSnapshotProjection` */
export interface PendingRuntimeSnapshot extends RuntimeSnapshot {
  state: string;
  reason: string;
}

/**
 * `httpapi.AccountRuntimeProjection`.
 *
 * `active` / `pending` / `retiring` are the three generations that can coexist
 * during a credential rotation. An account showing a `pending` alongside an
 * `active` is mid-rotation, not broken — the UI has to say which is which
 * rather than picking one and hiding the other.
 */
export interface AccountRuntime {
  accountId: string;
  active?: RuntimeSnapshot;
  pending?: PendingRuntimeSnapshot;
  retiring: RuntimeSnapshot[];
  desiredStateRevision: number;
  runtimeGeneration: number;
  credentialVersion: number;
  runtimeVersion: number;
  readiness: ProjectionReadiness;
  writeReadiness: ProjectionReadiness;
  lastAppliedDesiredStateRevision: number;
  lastAppliedRuntimeGeneration: number;
  blockedReason: string | null;
  capabilities: AccountCapability[];
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

/* --- Reservations -------------------------------------------------------- */

/** `reservations.NettingScope` */
export interface NettingScope {
  accountId: string;
  venue: string;
  instrumentId: string;
  positionMode: string | null;
  settlementScope: string | null;
  venueEnvironment: string;
}

/** `reservations.Reservation` */
export interface Reservation {
  reservationId: string;
  mode: Mode;
  productId: string;
  reservationKind: string;
  nettingScope: NettingScope;
  quantity: string | null;
  notional: string | null;
  currency: string | null;
  status: string;
  expiresAt: string | null;
  blockedReason: string | null;
  createdAt: string;
  updatedAt: string;
}
