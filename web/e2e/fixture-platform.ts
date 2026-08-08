import type { Page, Request, Route } from "@playwright/test";

interface FixtureOptions {
  authenticated?: boolean;
  degraded?: boolean;
  liveDenied?: boolean;
  mode?: "test" | "live";
}

export interface FixtureWrite {
  method: string;
  path: string;
  body: unknown;
  headers: Record<string, string>;
}

export interface FixturePlatform {
  writes: FixtureWrite[];
  readPaths: () => string[];
  snapshotReads: () => number;
}

const now = "2026-08-08T10:00:00Z";

function envelope(data: unknown) {
  return { meta: { requestId: "fixture-request", contractVersion: "boltertrader-product-platform-upgrade/v4", apiVersion: "v1" }, data };
}

function error(code: string, message: string, retryable = false) {
  return { error: { code, message, details: {}, retryable, requestId: "fixture-request" } };
}

async function requestBody(request: Request): Promise<unknown> {
  if (!request.postData()) return null;
  try {
    return request.postDataJSON();
  } catch {
    return request.postData();
  }
}

export async function installFixturePlatform(page: Page, options: FixtureOptions = {}): Promise<FixturePlatform> {
  let authenticated = options.authenticated ?? true;
  let reads = 0;
  const readPaths: string[] = [];
  const mode = options.mode ?? "test";
  const environment = mode === "live" ? "live" : "demo";
  const writes: FixtureWrite[] = [];
  const session = {
    principal: { id: "operator-1", mode, username: "operator@example.test" },
    session: { csrfToken: "csrf-fixture", expiresAt: "2026-08-09T00:00:00Z" },
  };
  const accounts = [
    { accountId: "binance-primary", venue: "binance", venueEnvironment: environment, displayName: "Binance Primary", productIds: ["funding", "manual"], status: "active", desiredStateRevision: 3, runtimeGeneration: 2, credentialVersion: 1 },
    { accountId: "bybit-hedge", venue: "bybit", venueEnvironment: environment, displayName: "Bybit Hedge", productIds: ["funding", "manual"], status: "active", desiredStateRevision: 2, runtimeGeneration: 1, credentialVersion: 1 },
  ];
  const positions = [
    { lineageId: "funding-lineage", productId: "funding", mode, accountId: "binance-primary", instrumentId: "BTC-USDT-PERP", settlementCurrency: "USDT", quantity: "0.001", averageEntryPrice: "60000", markPrice: "60125", realizedPnl: "1.25", unrealizedPnl: "0.125", updatedAt: now },
    { lineageId: "manual-lineage", productId: "manual", mode, accountId: "binance-primary", instrumentId: "ETH-USDT-PERP", settlementCurrency: "USDT", quantity: "0.01", averageEntryPrice: "3000", markPrice: "3020", realizedPnl: "0", unrealizedPnl: "0.2", updatedAt: now },
  ];
  const fundingInstrument = (accountId: string) => accountId === "binance-primary" ? "BTC-BINANCE-PERP" : "BTC-BYBIT-PERP";
  const fundingRequirements = (reservationKind: "open" | "close") => accounts.map((account) => ({
    accountId: account.accountId,
    productId: "funding",
    reservationKind,
    nettingScope: { accountId: account.accountId, venue: account.venue, instrumentId: fundingInstrument(account.accountId), positionMode: null, settlementScope: null, venueEnvironment: environment },
    quantity: "0.001",
    notional: "60",
    currency: "USDT",
    mode,
  }));
  const openRequirements = fundingRequirements("open");
  const closeRequirements = fundingRequirements("close");
  const opportunityRequirements = openRequirements.map(({ quantity: _quantity, notional: _notional, currency: _currency, ...requirement }) => requirement);
  const operations = {
    place: true,
    amend: true,
    cancel: true,
    openOrders: true,
    orderHistory: true,
    fills: true,
    balances: true,
    account: true,
    positions: true,
    funding: true,
    accountFundingPayments: true,
    leverage: true,
    privateStream: true,
    marginMode: true,
    positionMode: true,
    fundsTransfer: false,
  };

  const respond = (route: Route, data: unknown, status = 200) => route.fulfill({ status, contentType: "application/json", body: JSON.stringify(data) });

  await page.route("**/api/v1/**", async (route) => {
    const request = route.request();
    const url = new URL(request.url());
    const path = url.pathname.replace(/^\/api\/v1/, "") || "/";
    const method = request.method();

    if (method !== "GET") {
      writes.push({ method, path, body: await requestBody(request), headers: request.headers() });
    }
    if (path === "/auth/session" && method === "GET") {
      return authenticated ? respond(route, envelope(session)) : respond(route, error("AUTH_REQUIRED", "authentication required"), 401);
    }
    if (path === "/auth/login" && method === "POST") {
      authenticated = true;
      return respond(route, envelope(session));
    }
    if (path === "/auth/session" && method === "DELETE") {
      authenticated = false;
      return route.fulfill({ status: 204, body: "" });
    }
    if (!authenticated) return respond(route, error("AUTH_REQUIRED", "authentication required"), 401);

    if (method === "GET" && path !== "/realtime/stream") {
      reads += 1;
      readPaths.push(path);
    }
    if (path === "/system/status") return respond(route, envelope({ ready: !options.degraded, components: { gateway: { ready: !options.degraded, reason: options.degraded ? "runtime unavailable" : "" } } }));
    if (path === "/system/readiness") return respond(route, envelope({ ready: !options.degraded, components: { gateway: { ready: !options.degraded, reason: options.degraded ? "runtime unavailable" : "" } } }), options.degraded ? 503 : 200);
    if (path === "/accounts" && method === "GET") return respond(route, envelope({ accounts }));
    if (/^\/accounts\/[^/]+\/runtimes$/.test(path)) {
      const accountId = path.split("/")[2];
      const account = accounts.find((item) => item.accountId === accountId)!;
      return respond(route, envelope({ items: [{
        accountId,
        desiredStateRevision: account.desiredStateRevision,
        runtimeGeneration: account.runtimeGeneration,
        credentialVersion: account.credentialVersion,
        runtimeVersion: 4,
        readiness: { ready: true },
        writeReadiness: { ready: true },
        lastAppliedDesiredStateRevision: account.desiredStateRevision,
        lastAppliedRuntimeGeneration: account.runtimeGeneration,
        blockedReason: null,
        capabilities: [{ rowId: `${accountId}-perp`, venue: account.venue, product: "perp", settlement: "USDT", writeReady: true, operations }],
        observedAt: now,
      }] }));
    }
    if (path === "/catalog/venues") return respond(route, envelope({ items: [{ venue: "binance", environment, ready: true, products: ["funding", "manual"] }, { venue: "bybit", environment, ready: true, products: ["funding", "manual"] }] }));
    if (path === "/catalog/instruments") return respond(route, envelope({ items: [{ instrumentId: "BTC-BINANCE-PERP", venue: "binance", environment, product: "perp", symbol: "BTCUSDT", ready: true }, { instrumentId: "BTC-BYBIT-PERP", venue: "bybit", environment, product: "perp", symbol: "BTCUSDT", ready: true }, { instrumentId: "ETH-USDT-PERP", venue: "binance", environment, product: "perp", symbol: "ETHUSDT", ready: true }] }));
    if (path === "/products/funding/opportunities") return respond(route, envelope({ items: [{ opportunityId: "funding-opportunity", canonicalMarketId: "BTC-USDT-PERP", longAccountId: "binance-primary", shortAccountId: "bybit-hedge", longInstrumentId: "BTC-BINANCE-PERP", shortInstrumentId: "BTC-BYBIT-PERP", longVenue: "binance", shortVenue: "bybit", longFundingRate: "0.0001", shortFundingRate: "0.0003", reservationRequirements: opportunityRequirements }] }));
    if (path === "/commands") return respond(route, envelope({ items: [{ commandId: "funding-command", lineageId: "funding-lineage", productId: "funding", state: "filled", updatedAt: now }] }));
    if (path === "/fills") return respond(route, envelope({ items: [{ fillId: "fill-1", commandId: "funding-command", productId: "funding", accountId: "binance-primary", instrumentId: "BTC-USDT-PERP", side: "buy", quantity: "0.001", price: "60000", fee: "0.01", feeCurrency: "USDT", occurredAt: now }] }));
    if (path === "/positions") return respond(route, envelope({ items: positions }));
    if (path === "/ledger") return respond(route, envelope({ items: [{ entryId: "ledger-1", lineageId: "funding-lineage", productId: "funding", accountId: "binance-primary", instrumentId: "BTC-USDT-PERP", kind: "funding", amount: "1.25", currency: "USDT", occurredAt: now }] }));
    if (path === "/activity") return respond(route, envelope({ items: [{ eventId: "activity-1", sequence: 1, cursor: "cursor-1", kind: "command.finalized", productId: "funding", accountId: "binance-primary", resourceId: "funding-command", payload: { summary: "Funding command finalized", status: "filled" }, observedAt: now }] }));
    if (path === "/audit") return respond(route, envelope({ audit: [{ auditId: "audit-1", action: "command.submit", resourceType: "command", resourceId: "funding-command", outcome: "accepted", requestId: "request-1", occurredAt: now }] }));
    if (path === "/products/manual/orders") return respond(route, envelope({ items: [{ commandId: "manual-command", lineageId: "manual-lineage", productId: "manual", accountId: "binance-primary", commandKind: "manual.place", state: "submitted", updatedAt: now }] }));
    if (path === "/products/manual/execution-previews" && method === "POST") {
      if (options.liveDenied) return respond(route, error("LIVE_PRIVATE_WRITE_DENIED", "live private writes are disabled"), 403);
      return respond(route, envelope({ previewId: "manual-preview", blockers: [], warnings: [], expiresAt: "2026-08-08T10:05:00Z" }));
    }
    if (path === "/products/funding/execution-previews" && method === "POST") return respond(route, envelope({
      previewId: "funding-preview",
      opportunityId: "funding-opportunity",
      canSubmit: true,
      blockers: [],
      warnings: [],
      mode,
      expiresAt: "2026-08-08T10:05:00Z",
      catalogRevision: "catalog-1",
      factsRevision: "facts-1",
      plan: {
        previewId: "funding-preview",
        opportunityId: "funding-opportunity",
        productId: "funding",
        mode,
        routeId: "binance-bybit",
        formulaVersion: "funding-v1",
        observedAt: now,
        expiresAt: "2026-08-08T10:05:00Z",
        grossApr: "0.10",
        netApr: "0.08",
        roundTripFees: "0.001",
        slippage: "0.0005",
        capacityNotional: "1000",
        stability: "1",
        nextFunding: "2026-08-08T16:00:00Z",
        legs: [
          { accountId: "binance-primary", runtimeGeneration: 2, venue: "binance", venueEnvironment: environment, instrumentId: "BTC-BINANCE-PERP", symbol: "BTCUSDT", role: "long", side: "buy", quantity: "0.001", notional: "60", reduceOnly: false },
          { accountId: "bybit-hedge", runtimeGeneration: 1, venue: "bybit", venueEnvironment: environment, instrumentId: "BTC-BYBIT-PERP", symbol: "BTCUSDT", role: "short", side: "sell", quantity: "0.001", notional: "60", reduceOnly: false },
        ],
        blockers: [],
        warnings: [],
        canSubmit: true,
        normalizedHash: "plan-hash",
      },
      reservationRequirements: openRequirements,
    }));
    if (path === "/products/manual/commands" && method === "POST") return respond(route, envelope({ commandId: "manual-new", state: "accepted" }), 202);
    if (path === "/products/funding/commands" && method === "POST") return respond(route, envelope({ workItemId: "funding-work", status: "pending" }), 202);
    if (path === "/products/funding/lineage/funding-lineage" && method === "GET") return respond(route, envelope({ lineageId: "funding-lineage", closeReservationRequirements: closeRequirements }));
    if (path === "/reservations" && method === "POST") return respond(route, envelope({ reservationId: `reservation-${writes.length}`, status: "held" }), 201);
    if (path === "/accounts" && method === "POST") return respond(route, envelope({ account: accounts[0] }), 201);
    if (/^\/accounts\/[^/]+$/.test(path) && (method === "PATCH" || method === "DELETE")) return respond(route, envelope({ account: accounts[0] }), method === "DELETE" ? 202 : 200);
    if (/^\/accounts\/[^/]+\/(validate|rotate|drain|disable)$/.test(path) && method === "POST") return respond(route, envelope({ account: accounts[0] }));

    return respond(route, error("NOT_FOUND", "fixture route not found"), 404);
  });

  return { writes, readPaths: () => [...readPaths], snapshotReads: () => reads };
}

export async function installFixtureRealtime(page: Page): Promise<void> {
  await page.addInitScript(() => {
    class FixtureEventSource {
      static instances: FixtureEventSource[] = [];
      readonly listeners = new Map<string, Array<(event: { data: string }) => void>>();
      onopen: ((event: object) => void) | null = null;
      onerror: ((event: object) => void) | null = null;
      onmessage: ((event: { data: string }) => void) | null = null;

      constructor(readonly url: string, readonly options: { withCredentials?: boolean }) {
        FixtureEventSource.instances.push(this);
        queueMicrotask(() => this.onopen?.({}));
      }

      addEventListener(type: string, listener: (event: { data: string }) => void) {
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), listener]);
      }

      close() {}

      emit(type: string, payload: unknown) {
        const event = { data: JSON.stringify(payload) };
        for (const listener of this.listeners.get(type) ?? []) listener(event);
        if (type === "message") this.onmessage?.(event);
      }
    }

    Object.defineProperty(window, "EventSource", { configurable: true, value: FixtureEventSource });
    Object.defineProperty(window, "__emitRealtime", {
      configurable: true,
      value: (type: string, payload: unknown) => FixtureEventSource.instances.at(-1)?.emit(type, payload),
    });
  });
}
