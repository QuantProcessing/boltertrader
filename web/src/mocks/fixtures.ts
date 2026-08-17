import type {
  Account,
  AccountRuntime,
  ArbitrageOpportunity,
  Candle,
  CandleInterval,
  CatalogProjectionMeta,
  CredentialSchema,
  Fill,
  FundingBoardSnapshot,
  FundingHistorySeries,
  FundingWindow,
  Instrument,
  LedgerEntry,
  MarketFact,
  PnLSnapshot,
  Position,
  RateLimitStatus,
  SessionState,
  SystemSnapshot,
  Venue,
  VenueRouteProfile,
} from "@/api/types";

/* ---------------------------------------------------------------------------
 * The world the mock serves.
 *
 * Every value here is shaped by something in the backend rather than invented
 * to look plausible:
 *
 *   - Instrument rules use the four keys `catalog.Instrument` actually emits
 *     (`priceIncrement`, `quantityIncrement`, `minQuantity`, `minNotional`) and
 *     the increments the venues actually publish. `TradeRules` is an index
 *     signature, so a wrong key would typecheck and then silently fail to
 *     format a price — which is exactly why these are copied, not guessed.
 *   - Route profile ids are built the way `routeprofile.profileID` builds them:
 *     dot-joined, settlement lowercased, environment then `v1`.
 *   - Credential schemas are the entries of `catalog.browserCredentialVariants`,
 *     field for field, including which fields are metadata rather than secret.
 *   - Venues carry `products: ["manual"]` and `nativeEnvironment === environment`
 *     because `enrichBrowserVenues` overwrites both on the way out.
 *   - The readiness components are `system.RequiredComponents`.
 *
 * Everything is live: there is one environment now, and a fixture that still
 * said `testnet` would be describing a deployment that no longer exists.
 * ------------------------------------------------------------------------- */

/** The instant the whole fixture set was "observed". Fixed, so runs compare. */
export const OBSERVED_AT = "2026-08-17T09:00:00Z";

export const META = { requestId: "req-mock", contractVersion: "1", apiVersion: "v1" } as const;

export const PROJECTION: CatalogProjectionMeta = { revision: "42", observedAt: OBSERVED_AT };

/** The manual-venues list is revisioned separately from the catalog projection. */
export const VENUE_REVISION = "42";

export const RATE_LIMIT: RateLimitStatus = {
  limit: 1200,
  used: 30,
  remaining: 1170,
  windowEnds: "2026-08-17T09:01:00Z",
};

/**
 * The only account that exists in the mock, which is why it is a constant and
 * not a form field. Anything else is a real deployment's problem.
 */
export const CREDENTIALS = { username: "operator", password: "hunter2" };

export const SESSION: SessionState = {
  principal: { id: "principal-1", mode: "live", username: CREDENTIALS.username },
  session: { csrfToken: "csrf-mock", expiresAt: "2099-01-01T00:00:00Z" },
};

/* --- Instruments ---------------------------------------------------------- */

/**
 * Four instruments across two venues and both products.
 *
 * The spot pair leads because the terminal falls back to the first ready
 * instrument in catalog order, and spot is the one the binance account holds a
 * route for. The two BTC perpetuals are the pair the arbitrage scan ranks — a
 * hedge needs the same underlying quoted on two exchanges, and this is it.
 */
export const INSTRUMENTS: Instrument[] = [
  {
    instrumentId: "binance:live:spot:BTC-USDT",
    venue: "binance",
    environment: "live",
    product: "spot",
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    // Binance spot BTCUSDT: PRICE_FILTER 0.01, LOT_SIZE 0.00001, NOTIONAL 5.
    rules: { priceIncrement: "0.01", quantityIncrement: "0.00001", minQuantity: "0.00001", minNotional: "5" },
    ready: true,
  },
  {
    instrumentId: "binance:live:spot:ETH-USDT",
    venue: "binance",
    environment: "live",
    product: "spot",
    symbol: "ETH-USDT",
    baseAsset: "ETH",
    quoteAsset: "USDT",
    rules: { priceIncrement: "0.01", quantityIncrement: "0.0001", minQuantity: "0.0001", minNotional: "5" },
    ready: true,
  },
  {
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    venue: "binance",
    environment: "live",
    product: "perp",
    settlement: "USDT",
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    // Binance USDT-M BTCUSDT: tick 0.1, step 0.001, min notional 100.
    rules: { priceIncrement: "0.1", quantityIncrement: "0.001", minQuantity: "0.001", minNotional: "100" },
    ready: true,
  },
  {
    instrumentId: "bybit:live:perp:USDT:BTC-USDT",
    venue: "bybit",
    environment: "live",
    product: "perp",
    settlement: "USDT",
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    // Bybit linear BTCUSDT: tick 0.10, qty step 0.001, min order value 5.
    rules: { priceIncrement: "0.1", quantityIncrement: "0.001", minQuantity: "0.001", minNotional: "5" },
    ready: true,
  },
];

export function instrumentOf(instrumentId: string): Instrument | undefined {
  return INSTRUMENTS.find((candidate) => candidate.instrumentId === instrumentId);
}

/** The level each instrument's synthetic book and chart are drawn around. */
const ANCHOR: Record<string, number> = {
  "binance:live:spot:BTC-USDT": 114_250,
  "binance:live:spot:ETH-USDT": 4_120,
  "binance:live:perp:USDT:BTC-USDT": 114_262,
  "bybit:live:perp:USDT:BTC-USDT": 114_258,
};

/** Decimal places implied by a tick, so prices are formatted to the venue's grid. */
function decimalsOf(increment: string): number {
  const dot = increment.indexOf(".");
  return dot === -1 ? 0 : increment.length - dot - 1;
}

function priceStep(instrument: Instrument): number {
  return Number(instrument.rules.priceIncrement ?? "0.01");
}

function toPrice(instrument: Instrument, value: number): string {
  const step = priceStep(instrument);
  const snapped = Math.round(value / step) * step;
  return snapped.toFixed(decimalsOf(String(instrument.rules.priceIncrement ?? "0.01")));
}

/* --- Venues --------------------------------------------------------------- */

/**
 * Route profiles, built the way `enrichBrowserVenues` builds them: one per
 * (product, settlement) the registry knows for the venue, with the routeId and
 * label derived from the pair rather than written out.
 */
function routeProfiles(
  venue: string,
  rows: readonly { product: "spot" | "perp"; settlement?: string }[],
): VenueRouteProfile[] {
  return rows.map(({ product, settlement }) => {
    const suffix = settlement === undefined ? "" : `-${settlement.toLowerCase()}`;
    const base = product === "spot" ? "Spot" : "Perp";
    return {
      routeId: `${product}${suffix}`,
      product,
      settlementAsset: settlement,
      routeProfileId: [venue, product, settlement?.toLowerCase(), "live", "v1"].filter(Boolean).join("."),
      label: settlement === undefined ? base : `${base} / ${settlement}`,
    };
  });
}

/** `credential.AuthAPIKeySecret` and friends, with the fields the catalog publishes. */
const CREDENTIAL_SCHEMAS = {
  apiKeySecret: {
    schemaVersion: 1,
    authKind: "api_key_secret",
    fields: [
      { key: "apiKey", label: "API key", required: true, valueType: "string" },
      { key: "apiSecret", label: "API secret", required: true, valueType: "string" },
    ],
  },
  apiKeySecretPassphrase: {
    schemaVersion: 1,
    authKind: "api_key_secret_passphrase",
    fields: [
      { key: "apiKey", label: "API key", required: true, valueType: "string" },
      { key: "apiSecret", label: "API secret", required: true, valueType: "string" },
      { key: "passphrase", label: "Passphrase", required: true, valueType: "string" },
    ],
  },
  lighterSigner: {
    schemaVersion: 1,
    authKind: "lighter_signer",
    fields: [
      { key: "accountIndex", label: "Account index", metadata: true, required: true, valueType: "integer" },
      { key: "keyIndex", label: "Key index", metadata: true, required: true, valueType: "integer" },
      { key: "privateKey", label: "Private key", required: true, valueType: "string" },
    ],
  },
} satisfies Record<string, CredentialSchema>;

export const VENUES: Venue[] = [
  {
    venue: "binance",
    environment: "live",
    nativeEnvironment: "live",
    ready: true,
    products: ["manual"],
    credentialSchema: CREDENTIAL_SCHEMAS.apiKeySecret,
    routeProfiles: routeProfiles("binance", [{ product: "spot" }, { product: "perp", settlement: "USDT" }]),
  },
  {
    venue: "bybit",
    environment: "live",
    nativeEnvironment: "live",
    ready: true,
    products: ["manual"],
    credentialSchema: CREDENTIAL_SCHEMAS.apiKeySecret,
    routeProfiles: routeProfiles("bybit", [
      { product: "spot" },
      { product: "perp", settlement: "USDT" },
      { product: "perp", settlement: "USDC" },
    ]),
  },
  {
    venue: "okx",
    environment: "live",
    nativeEnvironment: "live",
    ready: true,
    products: ["manual"],
    // OKX is the venue whose key is useless without the passphrase that was
    // chosen when it was minted, which is why its schema has a third field.
    credentialSchema: CREDENTIAL_SCHEMAS.apiKeySecretPassphrase,
    routeProfiles: routeProfiles("okx", [{ product: "spot" }, { product: "perp", settlement: "USDT" }]),
  },
  {
    venue: "lighter",
    environment: "live",
    nativeEnvironment: "live",
    ready: true,
    products: ["manual"],
    // Not an API key at all: two integers that identify the sub-account and the
    // signing slot, plus the key itself. The two integers are metadata and the
    // key is a secret, and only the flag says which is which.
    credentialSchema: CREDENTIAL_SCHEMAS.lighterSigner,
    routeProfiles: routeProfiles("lighter", [{ product: "spot" }, { product: "perp", settlement: "USDC" }]),
  },
];

/* --- Accounts ------------------------------------------------------------- */

export const ACCOUNT: Account = {
  accountId: "acct-binance-live",
  venue: "binance",
  mode: "live",
  nativeEnvironment: "live",
  displayName: "Binance",
  productPolicy: ["manual", "arbitrage"],
  routes: [
    { routeId: "spot", product: "spot", routeProfileId: "binance.spot.live.v1", requestedState: "active" },
    {
      routeId: "perp-usdt",
      product: "perp",
      settlementAsset: "USDT",
      routeProfileId: "binance.perp.usdt.live.v1",
      requestedState: "active",
    },
  ],
  status: "active",
  desiredStateRevision: 3,
  runtimeGeneration: 1,
  credentialVersion: 1,
  lastValidatedAt: OBSERVED_AT,
  updatedAt: OBSERVED_AT,
};

/**
 * The second venue, which is the whole of what makes a hedge possible.
 *
 * Arbitrage is the one product that cannot be exercised by a single account: it
 * is long on one exchange and short on another, and the server refuses a hedge
 * whose legs share an account. A fixture set with one account could only ever
 * prove the refusal.
 */
export const HEDGE_ACCOUNT: Account = {
  accountId: "acct-bybit-live",
  venue: "bybit",
  mode: "live",
  nativeEnvironment: "live",
  displayName: "Bybit",
  productPolicy: ["manual", "arbitrage"],
  routes: [
    {
      routeId: "perp-usdt",
      product: "perp",
      settlementAsset: "USDT",
      routeProfileId: "bybit.perp.usdt.live.v1",
      requestedState: "active",
    },
  ],
  status: "active",
  desiredStateRevision: 2,
  runtimeGeneration: 1,
  credentialVersion: 1,
  lastValidatedAt: OBSERVED_AT,
  updatedAt: OBSERVED_AT,
};

/** Every operation a fully promoted account can perform, in the declared order. */
const ALL_OPERATIONS = {
  place: true,
  amend: true,
  cancel: true,
  openOrders: true,
  orderHistory: true,
  fills: true,
  balances: true,
  account: true,
  positions: true,
  leverage: true,
  privateStream: true,
  marginMode: true,
  positionMode: true,
  fundsTransfer: false,
} as const;

/**
 * The runtime projection for an account: what the gateway has actually applied,
 * as opposed to what the account asks for.
 *
 * The two revisions matching (`lastApplied*` equal to the desired pair) is what
 * "settled" looks like; settings renders a pending badge when they diverge.
 */
export function runtimeOf(account: Account): AccountRuntime {
  const routes = (account.routes ?? []).map((route) => ({
    routeId: route.routeId,
    product: route.product,
    settlementAsset: route.settlementAsset,
    routeProfileId: route.routeProfileId,
    requestedState: route.requestedState,
    ready: true,
  }));
  const snapshot = {
    desiredStateRevision: account.desiredStateRevision,
    runtimeGeneration: account.runtimeGeneration,
    credentialVersion: account.credentialVersion,
    requestedState: "active",
    routes,
  };
  return {
    accountId: account.accountId,
    active: snapshot,
    retiring: [],
    desiredStateRevision: account.desiredStateRevision,
    runtimeGeneration: account.runtimeGeneration,
    credentialVersion: account.credentialVersion,
    runtimeVersion: 1,
    readiness: { ready: true },
    writeReadiness: { ready: true },
    lastAppliedDesiredStateRevision: account.desiredStateRevision,
    lastAppliedRuntimeGeneration: account.runtimeGeneration,
    blockedReason: null,
    capabilities: (account.routes ?? []).map((route) => ({
      // Capability rows are keyed without the environment: a capability is a
      // property of the venue's API surface, not of which host serves it.
      rowId: [account.venue, route.product, route.settlementAsset].filter(Boolean).join(":"),
      venue: account.venue,
      product: route.product,
      settlement: route.settlementAsset,
      writeReady: true,
      operations: { ...ALL_OPERATIONS },
    })),
    observedAt: OBSERVED_AT,
  };
}

/* --- Market data ---------------------------------------------------------- */

/**
 * A book drawn around the instrument's anchor, one tick of spread at the touch
 * and widening from there, so the mid, the spread and the depth bars all differ.
 */
export function marketFact(instrument: Instrument): MarketFact {
  const step = priceStep(instrument);
  const anchor = ANCHOR[instrument.instrumentId] ?? 100;
  const bid = anchor - step / 2;
  const ask = anchor + step / 2;
  const sizes = instrument.product === "spot" ? [0.85, 1.4, 2.1, 3.05] : [1.2, 0.9, 3.3, 5.4];
  const depth = (from: number, direction: -1 | 1) =>
    sizes.map((size, index) => ({
      price: toPrice(instrument, from + direction * index * step * 6),
      quantity: (size + index * 0.35).toFixed(5),
    }));

  return {
    venue: instrument.venue,
    environment: instrument.environment,
    product: instrument.product,
    instrumentId: instrument.instrumentId,
    bestBid: toPrice(instrument, bid),
    bestBidSize: sizes[0]!.toFixed(5),
    bestAsk: toPrice(instrument, ask),
    bestAskSize: sizes[1]!.toFixed(5),
    bids: depth(bid, -1),
    asks: depth(ask, 1),
    ...(instrument.product === "perp"
      ? {
          markPrice: toPrice(instrument, anchor),
          fundingRate: instrument.venue === "bybit" ? "0.00042" : "0.0001",
          openInterest: instrument.venue === "bybit" ? "1520.44" : "9841.72",
        }
      : {}),
    observedAt: OBSERVED_AT,
  };
}

const INTERVAL_MS: Record<CandleInterval, number> = {
  "1m": 60_000,
  "5m": 300_000,
  "15m": 900_000,
  "30m": 1_800_000,
  "1h": 3_600_000,
  "4h": 14_400_000,
  "12h": 43_200_000,
  "1d": 86_400_000,
};

/**
 * A deterministic walk that reads like a market.
 *
 * Three sines with incommensurable periods, so the series has trends and
 * pullbacks at different scales without ever depending on `Math.random` — a
 * chart that redraws differently per run is untestable.
 */
function wobble(index: number): number {
  return Math.sin(index / 7) * 0.45 + Math.sin(index / 23) * 1.1 + Math.sin(index / 61) * 1.9;
}

/** Bars ending at `OBSERVED_AT`, with only the last one still forming. */
export function candles(instrument: Instrument, interval: CandleInterval, count: number): Candle[] {
  const step = INTERVAL_MS[interval];
  const anchor = ANCHOR[instrument.instrumentId] ?? 100;
  const end = Date.parse(OBSERVED_AT);
  const start = end - count * step;

  return Array.from({ length: count }, (_unused, index) => {
    const openTime = start + index * step;
    const open = anchor * (1 + wobble(index) / 100);
    const close = anchor * (1 + wobble(index + 1) / 100);
    const spread = Math.abs(close - open) + anchor * 0.0006;
    return {
      openTime: new Date(openTime).toISOString(),
      closeTime: new Date(openTime + step - 1).toISOString(),
      open: toPrice(instrument, open),
      high: toPrice(instrument, Math.max(open, close) + spread * 0.6),
      low: toPrice(instrument, Math.min(open, close) - spread * 0.6),
      close: toPrice(instrument, close),
      volume: (12 + Math.abs(wobble(index)) * 9).toFixed(4),
      // The bar the venue has not closed yet is the one being traded in.
      complete: index < count - 1,
    } satisfies Candle;
  });
}

/* --- Funding -------------------------------------------------------------- */

/**
 * Four venues quoting perpetuals, which is the least that makes a matrix a
 * matrix: a spread needs two full columns, a third that answered late shows
 * what stale looks like, and a fourth that did not answer at all shows that a
 * venue which is down is still a column rather than a gap.
 *
 * Lighter is the venue that publishes a rate with no interval and no open
 * interest, so the row that cannot honestly be annualised is present rather
 * than hypothetical.
 */
export const FUNDING_BOARD: FundingBoardSnapshot = {
  mode: "live",
  observedAt: OBSERVED_AT,
  venues: [
    {
      venue: "binance",
      environment: "live",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "binance.perp.usdt.live.v1",
      observedAt: OBSERVED_AT,
      stale: false,
      unavailable: false,
      rows: [
        {
          symbol: "BTC-USDT",
          nativeSymbol: "BTCUSDT",
          baseAsset: "BTC",
          quoteAsset: "USDT",
          rate: "0.0001",
          intervalHours: 8,
          nextFundingTime: "2026-08-17T16:00:00Z",
          markPrice: "114262.0",
          openInterest: "9841.72",
        },
        {
          symbol: "ETH-USDT",
          nativeSymbol: "ETHUSDT",
          baseAsset: "ETH",
          quoteAsset: "USDT",
          rate: "-0.00025",
          intervalHours: 8,
          nextFundingTime: "2026-08-17T16:00:00Z",
          markPrice: "4120.15",
        },
      ],
    },
    {
      venue: "bybit",
      environment: "live",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "bybit.perp.usdt.live.v1",
      observedAt: OBSERVED_AT,
      stale: false,
      unavailable: false,
      rows: [
        {
          symbol: "BTC-USDT",
          nativeSymbol: "BTCUSDT",
          baseAsset: "BTC",
          quoteAsset: "USDT",
          rate: "0.00042",
          intervalHours: 8,
          nextFundingTime: "2026-08-17T16:00:00Z",
          markPrice: "114258.0",
          openInterest: "1520.44",
        },
      ],
    },
    {
      venue: "lighter",
      environment: "live",
      product: "perp",
      settlement: "USDC",
      routeProfileId: "lighter.perp.usdc.live.v1",
      observedAt: "2026-08-17T08:58:00Z",
      stale: true,
      unavailable: false,
      rows: [{ symbol: "BTC-USDC", baseAsset: "BTC", quoteAsset: "USDC", rate: "0.00007" }],
    },
    {
      venue: "okx",
      environment: "live",
      product: "perp",
      settlement: "USDT",
      routeProfileId: "okx.perp.usdt.live.v1",
      stale: false,
      unavailable: true,
      rows: [],
    },
  ],
};

/** The rate each venue's history oscillates around, matched to the board. */
const FUNDING_BASELINE: Record<string, number> = {
  "binance:BTC-USDT": 0.0001,
  "binance:ETH-USDT": -0.00025,
  "bybit:BTC-USDT": 0.00042,
  "lighter:BTC-USDC": 0.00007,
};

const WINDOW_DAYS: Record<FundingWindow, number> = { "7d": 7, "30d": 30 };

/**
 * A settled funding series: one point every eight hours back across the window.
 *
 * Returns null for a pair the venue does not quote, which is the case the
 * server answers 404 `FUNDING_HISTORY_INSTRUMENT_UNKNOWN` for.
 */
export function fundingHistory(venue: string, symbol: string, window: FundingWindow): FundingHistorySeries | null {
  const baseline = FUNDING_BASELINE[`${venue}:${symbol}`];
  if (baseline === undefined) return null;

  const step = 8 * 3_600_000;
  const until = Date.parse(OBSERVED_AT);
  const since = until - WINDOW_DAYS[window] * 86_400_000;
  const count = Math.floor((until - since) / step);

  return {
    venue,
    environment: "live",
    symbol,
    since: new Date(since).toISOString(),
    until: new Date(until).toISOString(),
    observedAt: OBSERVED_AT,
    points: Array.from({ length: count }, (_unused, index) => ({
      fundingTime: new Date(since + index * step).toISOString(),
      // Funding oscillates around its baseline rather than holding it; the
      // chart exists to show that shape.
      rate: (baseline * (1 + Math.sin(index / 5) * 0.55) + Math.sin(index / 17) * 0.00004).toFixed(8),
      intervalHours: 8,
    })),
  };
}

/**
 * The one pair the board's two full columns can make.
 *
 * Derived by hand from `FUNDING_BOARD` so the two screens agree: binance pays
 * 0.0001 every eight hours and bybit pays 0.00042, which is 0.0003 and 0.00126
 * a day. Longs collect where the rate is lower and pay where it is higher, so
 * the hedge is long binance and short bybit, and the daily spread is the
 * difference. Lighter quotes BTC-USDC — a different quote asset, and so not the
 * same underlying — which is why one opportunity comes out of a board with
 * three live venues rather than three.
 */
export const ARBITRAGE_OPPORTUNITIES: ArbitrageOpportunity[] = [
  {
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    long: {
      venue: "binance",
      environment: "live",
      accountId: ACCOUNT.accountId,
      instrumentId: "binance:live:perp:USDT:BTC-USDT",
      rate: "0.0001",
      intervalHours: 8,
      nextFundingTime: "2026-08-17T16:00:00Z",
      markPrice: "114262.0",
      openInterest: "9841.72",
      dailyRate: "0.0003",
      stale: false,
    },
    short: {
      venue: "bybit",
      environment: "live",
      accountId: HEDGE_ACCOUNT.accountId,
      instrumentId: "bybit:live:perp:USDT:BTC-USDT",
      rate: "0.00042",
      intervalHours: 8,
      nextFundingTime: "2026-08-17T16:00:00Z",
      markPrice: "114258.0",
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

/* --- Ledgers -------------------------------------------------------------- */

/**
 * One settled position, so the portfolio screens have something to show before
 * anything is traded. It is the long leg of the hedge above, held on binance.
 */
export const POSITIONS: Position[] = [
  {
    lineageId: "lin-seed-1",
    productId: "arbitrage",
    mode: "live",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    settlementCurrency: "USDT",
    quantity: "0.050",
    averageEntryPrice: "113840.0",
    markPrice: "114262.0",
    realizedPnl: "0",
    unrealizedPnl: "21.10",
    updatedAt: OBSERVED_AT,
  },
];

export const LEDGER: LedgerEntry[] = [
  {
    entryId: "led-1",
    mode: "live",
    lineageId: "lin-seed-1",
    productId: "arbitrage",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    kind: "fee",
    amount: "-2.2768",
    currency: "USDT",
    occurredAt: "2026-08-16T16:00:02Z",
  },
  {
    entryId: "led-2",
    mode: "live",
    lineageId: "lin-seed-1",
    productId: "arbitrage",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    kind: "funding",
    amount: "-0.5692",
    currency: "USDT",
    occurredAt: "2026-08-17T00:00:01Z",
  },
];

export const PNL: PnLSnapshot = {
  pnlSnapshotId: "pnl-1",
  lineageId: "lin-seed-1",
  productId: "arbitrage",
  accountId: ACCOUNT.accountId,
  instrumentId: "binance:live:perp:USDT:BTC-USDT",
  realizedPnl: "0",
  unrealizedPnl: "21.10",
  fees: "-2.2768",
  funding: "-0.5692",
  netPnl: "18.2540",
  asOf: OBSERVED_AT,
};

export const FILLS: Fill[] = [
  {
    fillId: "fill-1",
    commandId: "cmd-seed-1",
    lineageId: "lin-seed-1",
    productId: "arbitrage",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    side: "buy",
    quantity: "0.050",
    price: "113840.0",
    fee: "2.2768",
    feeCurrency: "USDT",
    settlementCurrency: "USDT",
    venueSequence: 88_231_004,
    occurredAt: "2026-08-16T16:00:02Z",
  },
];

/* --- Readiness ------------------------------------------------------------ */

/** `system.RequiredComponents`, all five, all up. */
export const SYSTEM: SystemSnapshot = {
  ready: true,
  components: {
    process: { ready: true, reason: "ready", observedAt: OBSERVED_AT },
    database: { ready: true, reason: "ready", observedAt: OBSERVED_AT },
    gateway: { ready: true, reason: "ready", observedAt: OBSERVED_AT },
    publicData: { ready: true, reason: "ready", observedAt: OBSERVED_AT },
    accounts: { ready: true, reason: "ready", observedAt: OBSERVED_AT },
  },
};
