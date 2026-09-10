import type {
  Account,
  AccountBalance,
  AccountPositionSnapshot,
  AccountRuntime,
  CatalogProjectionMeta,
  CredentialSchema,
  Fill,
  Instrument,
  LedgerEntry,
  PnLSnapshot,
  Position,
  RateLimitStatus,
  SessionState,
  SystemSnapshot,
  Venue,
  VenueRouteProfile,
} from "@/api/types";

/* ---------------------------------------------------------------------------
 * The part of the world that has no public feed.
 *
 * Everything an exchange publishes without a key — books, candles, funding, the
 * board and the spreads derived from it — is fetched live in `venues.ts`. What
 * is left here is what a venue will only tell an authenticated caller, or will
 * not tell anyone: the operator's session, their accounts and routes, their
 * positions, fills, ledger and PnL. Those are the mock's actual subject. A
 * price was never anyone's to invent.
 *
 * The instrument and venue catalogs stay here too, one step behind that line:
 * the venues do publish them, but as thousands of rows, and the terminal's
 * first screen is a picker over them. Seven instruments chosen to exercise the
 * picker is a better fixture than binance's nine hundred, so these are curated
 * rather than fetched — and curated from the real thing:
 *
 *   - Instrument rules use the four keys `catalog.Instrument` actually emits
 *     (`priceIncrement`, `quantityIncrement`, `minQuantity`, `minNotional`) and
 *     the increments the venues actually publish. `TradeRules` is an index
 *     signature, so a wrong key would typecheck and then silently fail to
 *     format a price — which is exactly why these are copied, not guessed.
 *   - Route profile ids are built the way `routeprofile.profileID` builds them:
 *     dot-joined, settlement lowercased, then `v1`. There is no environment
 *     segment — `binance.spot.v1`, not `binance.spot.live.v1`.
 *   - Credential schemas are the entries of `catalog.browserCredentialVariants`,
 *     field for field, including which fields are metadata rather than secret.
 *   - Venues carry `products: ["manual"]` and `nativeEnvironment === environment`
 *     because `enrichBrowserVenues` overwrites both on the way out.
 *   - The readiness components are `system.RequiredComponents`.
 *
 * Everything is live: there is one environment now, and a fixture that still
 * said `testnet` would be describing a deployment that no longer exists.
 * ------------------------------------------------------------------------- */

/**
 * The instant the account-side fixtures were "observed". Fixed, so runs compare.
 *
 * It is deliberately not used for anything the venues answer. A position opened
 * at this timestamp beside a mark price fetched a moment ago is the honest
 * picture of a mock account against a live market; back-dating the price to
 * match would be the dishonest one.
 */
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
  principal: { id: "principal-1", username: CREDENTIALS.username },
  session: { csrfToken: "csrf-mock", expiresAt: "2099-01-01T00:00:00Z" },
};

/* --- Instruments ---------------------------------------------------------- */

/**
 * Seven instruments across four venues and both products.
 *
 * The spot pair leads because the terminal falls back to the first ready
 * instrument in catalog order, and spot is the one the binance account holds a
 * route for. The BTC perpetuals exercise the same underlying across venues.
 *
 * Binance leads and lists most of them on purpose. The picker's defect was that
 * a venue further down the catalog could not be found without scrolling past
 * everything binance lists, so a fixture where binance is not in the majority
 * cannot reproduce it. The halted okx listing is here for the same reason: an
 * instrument that is not tradable has to be findable.
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
    marketFamily: "spot",
    assetClass: "crypto",
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
    marketFamily: "spot",
    assetClass: "crypto",
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
    marketFamily: "venue_perpetual",
    assetClass: "crypto",
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
    marketFamily: "venue_perpetual",
    assetClass: "crypto",
    // Bybit linear BTCUSDT: tick 0.10, qty step 0.001, min order value 5.
    rules: { priceIncrement: "0.1", quantityIncrement: "0.001", minQuantity: "0.001", minNotional: "5" },
    ready: true,
  },
  {
    // Deliberately last, and on a venue nothing else lists: this is the row the
    // old flat picker buried, and finding it is what the offline spec asserts.
    instrumentId: "okx:live:perp:USDT:BTC-USDT",
    venue: "okx",
    environment: "live",
    product: "perp",
    settlement: "USDT",
    symbol: "BTC-USDT",
    baseAsset: "BTC",
    quoteAsset: "USDT",
    marketFamily: "venue_perpetual",
    assetClass: "crypto",
    // OKX BTC-USDT-SWAP: tick 0.1, lot 0.01 contracts, min 0.01.
    rules: { priceIncrement: "0.1", quantityIncrement: "0.01", minQuantity: "0.01", minNotional: "1" },
    ready: true,
  },
  {
    // Halted, and still listed: design.md §10 requires it be shown and marked.
    instrumentId: "okx:live:spot:BTC-USDC",
    venue: "okx",
    environment: "live",
    product: "spot",
    symbol: "BTC-USDC",
    baseAsset: "BTC",
    quoteAsset: "USDC",
    marketFamily: "spot",
    assetClass: "crypto",
    rules: { priceIncrement: "0.1", quantityIncrement: "0.00001", minQuantity: "0.00001", minNotional: "1" },
    ready: false,
  },
  {
    instrumentId: "lighter:live:perp:USDC:ETH-USDC",
    venue: "lighter",
    environment: "live",
    product: "perp",
    settlement: "USDC",
    symbol: "ETH-USDC",
    baseAsset: "ETH",
    quoteAsset: "USDC",
    marketFamily: "venue_perpetual",
    assetClass: "crypto",
    rules: { priceIncrement: "0.01", quantityIncrement: "0.0001", minQuantity: "0.0001", minNotional: "10" },
    ready: true,
  },
];

export function instrumentOf(instrumentId: string): Instrument | undefined {
  return INSTRUMENTS.find((candidate) => candidate.instrumentId === instrumentId);
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
      routeProfileId: [venue, product, settlement?.toLowerCase(), "v1"].filter(Boolean).join("."),
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
  nativeEnvironment: "live",
  displayName: "Binance",
  routes: [
    { routeId: "spot", product: "spot", routeProfileId: "binance.spot.v1" },
    {
      routeId: "perp-usdt",
      product: "perp",
      settlementAsset: "USDT",
      routeProfileId: "binance.perp.usdt.v1",
    },
  ],
  status: "active",
  lastValidatedAt: OBSERVED_AT,
  updatedAt: OBSERVED_AT,
};

/** A second account exercises cross-venue selection and account isolation. */
export const SECOND_ACCOUNT: Account = {
  accountId: "acct-bybit-live",
  venue: "bybit",
  nativeEnvironment: "live",
  displayName: "Bybit",
  routes: [
    {
      routeId: "perp-usdt",
      product: "perp",
      settlementAsset: "USDT",
      routeProfileId: "bybit.perp.usdt.v1",
    },
  ],
  status: "active",
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
 return {accountId:account.accountId, connected:true, observedAt:OBSERVED_AT};
}

/* --- Ledgers -------------------------------------------------------------- */

/**
 * One settled position, so the portfolio screens have something to show before
 * anything is traded. It is held on binance.
 */
export const POSITIONS: Position[] = [
  {
    lineageId: "lin-seed-1",
    productId: "manual",
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

export const BALANCES: AccountBalance[] = [
  {
    accountId: ACCOUNT.accountId,
    product: "perp",
    settlementAsset: "USDT",
    asset: "USDT",
    available: "346.28",
    availableMargin: "346.28",
    locked: "0",
    total: "350.00",
  },
  {
    accountId: SECOND_ACCOUNT.accountId,
    product: "perp",
    settlementAsset: "USDT",
    asset: "USDT",
    available: "218.75",
    availableMargin: "218.75",
    locked: "0",
    total: "225.00",
  },
];

export const ACCOUNT_POSITIONS: AccountPositionSnapshot = {
  projectionRevision: 1,
  verifiedAt: OBSERVED_AT,
  summary: {
    positionCount: 2,
    accountCount: 2,
    staleAccountCount: 0,
    bySettlementAsset: [
      {
        settlementAsset: "USDT",
        positionCount: 2,
        liveCount: 2,
        staleCount: 0,
        excludedPnlCount: 0,
      },
    ],
  },
  accounts: [
    {
      accountId: ACCOUNT.accountId,
      label: ACCOUNT.displayName,
      venue: ACCOUNT.venue,
      environment: "live",
      status: "connected",
      routeHealth: "live",
      freshness: "live",
      writeReady: true,
      verifiedAt: OBSERVED_AT,
      receivedAt: OBSERVED_AT,
    },
    {
      accountId: SECOND_ACCOUNT.accountId,
      label: SECOND_ACCOUNT.displayName,
      venue: SECOND_ACCOUNT.venue,
      environment: "live",
      status: "connected",
      routeHealth: "live",
      freshness: "live",
      writeReady: true,
      verifiedAt: OBSERVED_AT,
      receivedAt: OBSERVED_AT,
    },
  ],
  items: [
    {
      accountId: ACCOUNT.accountId,
      routeId: "perp-usdt",
      instrumentId: "binance:live:perp:USDT:BTC-USDT",
      accountLabel: ACCOUNT.displayName,
      venue: "binance",
      environment: "live",
      product: "perp",
      settlementAsset: "USDT",
      symbol: "BTC-USDT",
      side: "buy",
      quantity: "0.050",
      entryPrice: "113840.0",
      markPrice: "",
      markPriceSource: "unavailable",
      unrealizedPnl: "",
      unrealizedPnlCurrency: "USDT",
      unrealizedPnlSource: "unavailable",
      liquidationPrice: "",
      leverage: "2",
      marginMode: "cross",
      routeHealth: "live",
      freshness: "live",
      writeReady: true,
      resourceVersion: 1,
      observedAt: OBSERVED_AT,
      verifiedAt: OBSERVED_AT,
      receivedAt: OBSERVED_AT,
    },
    {
      accountId: SECOND_ACCOUNT.accountId,
      routeId: "perp-usdt",
      instrumentId: "bybit:live:perp:USDT:BTC-USDT",
      accountLabel: SECOND_ACCOUNT.displayName,
      venue: "bybit",
      environment: "live",
      product: "perp",
      settlementAsset: "USDT",
      symbol: "BTC-USDT",
      side: "sell",
      quantity: "-0.030",
      entryPrice: "114120.0",
      markPrice: "",
      markPriceSource: "unavailable",
      unrealizedPnl: "",
      unrealizedPnlCurrency: "USDT",
      unrealizedPnlSource: "unavailable",
      liquidationPrice: "",
      leverage: "2",
      marginMode: "cross",
      routeHealth: "live",
      freshness: "live",
      writeReady: true,
      resourceVersion: 1,
      observedAt: OBSERVED_AT,
      verifiedAt: OBSERVED_AT,
      receivedAt: OBSERVED_AT,
    },
  ],
};

export const LEDGER: LedgerEntry[] = [
  {
    entryId: "led-1",
    lineageId: "lin-seed-1",
    productId: "manual",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    kind: "fee",
    amount: "2.2768",
    currency: "USDT",
    occurredAt: "2026-08-16T16:00:02Z",
  },
  {
    entryId: "led-2",
    lineageId: "lin-seed-1",
    productId: "manual",
    accountId: ACCOUNT.accountId,
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    kind: "funding",
    amount: "-0.5692",
    currency: "USDT",
    occurredAt: "2026-08-17T00:00:01Z",
  },
];

export const PNL: PnLSnapshot = {
  lineageId: "lin-seed-1",
  productId: "manual",
  accountId: ACCOUNT.accountId,
  instrumentId: "binance:live:perp:USDT:BTC-USDT",
  byCurrency: [{ currency: "USDT", realizedPnl: "0", unrealizedPnl: "21.10", fees: "2.2768", funding: "-0.5692", bookedNetPnl: "-2.846", netPnl: "18.254" }],
  asOf: OBSERVED_AT,
  valuationAsOf: OBSERVED_AT,
};

export const FILLS: Fill[] = [
  {
    fillId: "fill-1",
    commandId: "cmd-seed-1",
    lineageId: "lin-seed-1",
    productId: "manual",
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
