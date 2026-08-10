import http from "node:http";
import { randomUUID } from "node:crypto";

const DEFAULT_PORT = 8081;
const CONTRACT_VERSION = "private-v1-mode-1";
const SESSION_COOKIE = "ft_fixture_session";
const JSON_HEADERS = Object.freeze({
  "content-type": "application/json; charset=utf-8",
  "cache-control": "no-store",
});

export function createBrowserFixtureState({ now = () => new Date("2026-08-01T12:00:00.000Z") } = {}) {
  const sessions = new Map();
  const state = {
    now,
    principal: {
      principalId: "fixture-principal",
      email: "fixture@example.test",
      mode: "test",
      role: "owner",
    },
    sessions,
    sseClients: new Set(),
    executions: [],
    activities: [
      activity("fixture-activity-open", "position.opened", "open", "accepted", "BTC-USDT fixture position already open", now().toISOString()),
    ],
    auditEvents: [],
    positionOpen: true,
    cursor: 0,
    recordAudit(kind, payload = {}) {
      const event = {
        auditId: `fixture-audit-${this.auditEvents.length + 1}`,
        occurredAt: this.now().toISOString(),
        actor: "fixture-principal",
        action: kind,
        outcome: "fixture",
        details: redact(payload),
      };
      this.auditEvents.unshift(event);
      return event;
    },
    nextCursor() {
      this.cursor += 1;
      return `fixture-cursor-${this.cursor}`;
    },
    enqueueSse(kind, data, scope = []) {
      const envelope = eventEnvelope(this, kind, data, scope);
      const frame = sseFrame(kind, envelope);
      for (const response of this.sseClients) response.write(frame);
      return envelope;
    },
    closePosition(idempotencyKey = "") {
      if (!this.positionOpen) {
        return execution("fixture-exec-close-duplicate", "close", "completed", "position already closed");
      }
      this.positionOpen = false;
      const exec = execution("fixture-exec-close-1", "close", "completed", "fixture close filled both legs");
      this.executions.unshift(exec);
      this.activities.unshift(activity("fixture-activity-close", "position.closed", "close", "closed", "Fixture position closed; ledger updated", this.now().toISOString()));
      this.recordAudit("position.close.fixture", { positionId: "fixture-position-open", idempotencyKey });
      this.enqueueSse("execution.upsert", exec);
      this.enqueueSse("position.upsert", { positionId: "fixture-position-open", state: "closed" });
      this.enqueueSse("ledger.portfolio.updated", portfolioLedger(this));
      this.enqueueSse("verification.status", verification(this));
      this.enqueueSse("resnapshot.required", null, ["positions", "closedPositions", "portfolio", "verification", "activities", "auditEvents", "executions"]);
      return exec;
    },
  };
  state.recordAudit("fixture.started", { fixtureOnly: true });
  return state;
}

export async function startBrowserFixtureServer({ host = "127.0.0.1", port = DEFAULT_PORT, state = createBrowserFixtureState() } = {}) {
  const server = http.createServer((request, response) => {
    void handleRequest(state, request, response);
  });
  await new Promise((resolve, reject) => {
    server.once("error", reject);
    server.listen(port, host, () => {
      server.off("error", reject);
      resolve();
    });
  });
  const address = server.address();
  const origin = `http://${address.address}:${address.port}`;
  return {
    state,
    server,
    origin,
    port: address.port,
    async close() {
      for (const client of state.sseClients) client.end();
      state.sseClients.clear();
      await new Promise((resolve, reject) => server.close((error) => error ? reject(error) : resolve()));
    },
  };
}

async function handleRequest(state, request, response) {
  const url = new URL(request.url ?? "/", "http://fixture.local");
  if (!url.pathname.startsWith("/api/v1/")) {
    return sendError(response, 404, "not_found", "fixture only serves /api/v1/*");
  }
  const path = url.pathname.slice("/api/v1".length) || "/";
  if (request.method === "OPTIONS") return send(response, 204, null);

  try {
    if (request.method === "POST" && path === "/auth/login") return login(state, request, response);
    if (request.method === "GET" && path === "/auth/session") return session(state, request, response);

    const auth = authenticate(state, request);
    if (!auth.ok) return sendError(response, 401, "unauthenticated", "fixture session required");
    if (isWrite(request) && !validCsrf(request, auth.session)) {
      return sendError(response, 403, "csrf_invalid", "fixture CSRF token required");
    }

    if (request.method === "DELETE" && path === "/auth/session") return logout(state, auth, response);
    if (request.method === "GET" && path === "/me") return sendData(response, { principal: state.principal });
    if (request.method === "GET" && path === "/system/status") return sendData(response, systemStatus());
    if (request.method === "GET" && path === "/system/readiness") return sendData(response, readiness());
    if (request.method === "GET" && path === "/system/verification") return sendData(response, verification(state));
    if (request.method === "GET" && path === "/ledger/portfolio") return sendData(response, portfolioLedger(state));
    if (request.method === "GET" && path === "/ledger/funding-payments") return sendData(response, fundingPayments());
    if (request.method === "GET" && path === "/opportunities") return sendData(response, { items: [opportunity()] });
    if (request.method === "GET" && path === "/opportunities/fixture-opp-btc") return sendData(response, opportunity());
    if (request.method === "GET" && path === "/funding-rate-alerts") return sendData(response, { items: [alert()] });
    if (request.method === "POST" && path === "/execution-previews") return preview(state, request, response);
    if (request.method === "POST" && path === "/orders") return sendError(response, 409, "fixture_private_write_blocked", "fixture never submits open orders");
    if (request.method === "GET" && path === "/positions") return sendData(response, positions(state, url.searchParams));
    if (request.method === "GET" && path === "/activities") return sendData(response, { items: state.activities });
    if (request.method === "GET" && path === "/audit-events") return sendData(response, { items: state.auditEvents });
    if (request.method === "GET" && path === "/stream") return stream(state, response);

    const closeMatch = path.match(/^\/positions\/([^/]+)\/close$/);
    if (request.method === "POST" && closeMatch) return closePosition(state, request, response, decodeURIComponent(closeMatch[1]));
    const closePreviewMatch = path.match(/^\/positions\/([^/]+)\/close-preview$/);
    if (request.method === "POST" && closePreviewMatch) return closePreview(state, response, decodeURIComponent(closePreviewMatch[1]));
    const positionMatch = path.match(/^\/positions\/([^/]+)$/);
    if (request.method === "GET" && positionMatch) return sendData(response, positionLedger(state, decodeURIComponent(positionMatch[1])));

    return sendError(response, 404, "not_found", `fixture route not implemented: ${request.method} ${path}`);
  } catch (error) {
    return sendError(response, 500, "fixture_error", error?.message || "fixture error");
  }
}

async function login(state, request, response) {
  const body = await readJson(request);
  const sessionId = `sess_${randomUUID().replaceAll("-", "")}`;
  const csrfToken = `csrf_${randomUUID().replaceAll("-", "")}`;
  state.sessions.set(sessionId, { sessionId, csrfToken, principalId: state.principal.principalId });
  state.recordAudit("auth.login", body);
  return sendData(response, sessionPayload(state, csrfToken), 200, {
    "set-cookie": `${SESSION_COOKIE}=${sessionId}; Path=/; HttpOnly; SameSite=Lax`,
  });
}

function session(state, request, response) {
  const auth = authenticate(state, request);
  if (!auth.ok) return sendError(response, 401, "unauthenticated", "fixture session required");
  return sendData(response, sessionPayload(state, auth.session.csrfToken));
}

function logout(state, auth, response) {
  state.sessions.delete(auth.session.sessionId);
  state.recordAudit("auth.logout", { sessionId: auth.session.sessionId });
  return sendData(response, { ok: true }, 200, {
    "set-cookie": `${SESSION_COOKIE}=; Path=/; HttpOnly; SameSite=Lax; Max-Age=0`,
  });
}

async function preview(state, request, response) {
  const body = await readJson(request);
  state.recordAudit("execution.preview.fixture", body);
  return sendData(response, {
    previewId: "fixture-preview-open-blocked",
    opportunityId: body?.opportunityId ?? "fixture-opp-btc",
    snapshotId: "fixture-snapshot-test-1",
    canSubmit: false,
    fixtureOnly: true,
    blockers: [{ code: "fixture_open_writes_blocked", message: "Fixture intentionally blocks private open writes; preview is safe and local only." }],
    warnings: [{ code: "fixture_not_real_test_execution", message: "This is isolated UI fixture data, not real Gateway Test execution." }],
    plannedLegs: [
      { role: "long", venue: "binance", instrumentId: "binance:demo:perp:USDT:BTC-USDT", notional: "5000.00000000", side: "buy" },
      { role: "short", venue: "bybit", instrumentId: "bybit:testnet:perp:USDT:BTC-USDT", notional: "5000.00000000", side: "sell" },
    ],
    estimatedFeesUsd: "3.20000000",
    estimatedSlippageUsd: "1.10000000",
    expectedNetAprPct: "18.42000000",
    expectedNetPnL7dUsd: "17.65000000",
  });
}

function closePreview(state, response, positionId) {
  if (!state.positionOpen || positionId !== "fixture-position-open") {
    return sendError(response, 404, "position_not_open", "fixture position is not open");
  }
  return sendData(response, {
    previewId: "fixture-preview-close-ready",
    positionId,
    canSubmit: true,
    fixtureOnly: true,
    expectedResidual: { legQuantity: "0.00000000", reportingAssetNotional: "0.00000000", openOrderCount: 0, pendingCompensation: 0 },
    estimatedFeesUsd: "1.25000000",
  });
}

function closePosition(state, request, response, positionId) {
  if (positionId !== "fixture-position-open") {
    return sendError(response, 404, "position_not_found", "fixture position not found");
  }
  const exec = state.closePosition(request.headers["idempotency-key"] ?? "");
  return sendData(response, exec);
}

function stream(state, response) {
  response.writeHead(200, {
    "content-type": "text/event-stream; charset=utf-8",
    "cache-control": "no-store, no-transform",
    connection: "keep-alive",
  });
  response.flushHeaders?.();
  state.sseClients.add(response);
  response.write(sseFrame("heartbeat", eventEnvelope(state, "heartbeat", { fixtureOnly: true })));
  response.on("close", () => state.sseClients.delete(response));
}

function authenticate(state, request) {
  const cookie = request.headers.cookie ?? "";
  const sessionId = cookie.split(";").map((part) => part.trim()).find((part) => part.startsWith(`${SESSION_COOKIE}=`))?.slice(`${SESSION_COOKIE}=`.length);
  const session = sessionId ? state.sessions.get(sessionId) : null;
  return session ? { ok: true, session } : { ok: false };
}

function validCsrf(request, session) {
  return request.headers["x-csrf-token"] === session.csrfToken;
}

function isWrite(request) {
  return ["POST", "PUT", "PATCH", "DELETE"].includes(request.method ?? "");
}

async function readJson(request) {
  let raw = "";
  for await (const chunk of request) raw += chunk;
  if (!raw) return {};
  return JSON.parse(raw);
}

function sendData(response, data, status = 200, headers = {}) {
  return send(response, status, { data, meta: { fixtureOnly: true, requestId: `fixture-${Date.now()}` } }, headers);
}

function sendError(response, status, code, message) {
  return send(response, status, { error: { code, message, retryable: false }, meta: { fixtureOnly: true } });
}

function send(response, status, body, headers = {}) {
  response.writeHead(status, { ...JSON_HEADERS, ...headers });
  response.end(body === null ? "" : JSON.stringify(body));
}

function sseFrame(kind, envelope) {
  return `event: ${kind}\ndata: ${JSON.stringify(envelope)}\n\n`;
}

function eventEnvelope(state, kind, data, scope = []) {
  const envelope = {
    eventId: `fixture-event-${state.cursor + 1}`,
    kind,
    cursor: state.nextCursor(),
    occurredAt: state.now().toISOString(),
    principalId: state.principal.principalId,
    mode: "test",
    contractVersion: CONTRACT_VERSION,
  };
  if (scope.length > 0) envelope.scope = scope;
  if (data !== null && data !== undefined) envelope.data = data;
  return envelope;
}

function sessionPayload(state, csrfToken) {
  return {
    principal: state.principal,
    session: {
      authenticated: true,
      csrfToken,
      expiresAt: "2026-08-01T18:00:00.000Z",
    },
  };
}

function systemStatus() {
  return {
    mode: "test",
    ready: true,
    build: "fixture-local",
    version: "fixture-browser-v1",
    maintenance: false,
    maintenanceReason: "",
  };
}

function readiness() {
  return {
    ready: false,
    openReady: false,
    closeReady: true,
    recoveryReady: true,
    readinessReason: "fixture_open_blocked_close_ready",
    components: {
      gateway: { status: "fixture", details: { fixtureOnly: true } },
      execution: { status: "blocked", details: { twoLegWriteReady: false, twoLegWriteReason: "fixture_open_blocked_close_ready" } },
      ledger: { status: "verified", details: { watermarkVerified: true } },
    },
  };
}

function opportunity() {
  return {
    opportunityId: "fixture-opp-btc",
    symbol: "BTC-USDT",
    longVenue: "Binance",
    shortVenue: "Bybit",
    longVenueId: "binance-demo",
    shortVenueId: "bybit-testnet",
    settlement: "USDT",
    netAprPct: "18.42000000",
    grossAprPct: "23.10000000",
    capacityUsd: "10000.00000000",
    stabilityPct: "91.50000000",
    roundTripFeeBps: "6.40000000",
    estimatedSlippageBps: "2.20000000",
    basisPct: "0.08000000",
    hedgeDeltaPct: "0.12000000",
    sevenDayFundingPct: "0.35300000",
    dataAgeSeconds: "1.00000000",
    liquidationBufferPct: "63.00000000",
    positiveSettlements: 5,
    validSettlements: 6,
    nextFundingAt: "2026-08-01T16:00:00.000Z",
    observedAt: "2026-08-01T12:00:00.000Z",
    formulaVersion: "fixture-opportunity-v1",
    twoLegWriteReady: false,
    twoLegWriteReason: "fixture_open_blocked_close_ready",
    blockers: [{ code: "fixture_open_blocked", message: "Open write is blocked so browser can verify preview-only behavior." }],
    warnings: [{ code: "fixture_only", message: "Fixture data is isolated and not real Test execution." }],
    fundingSeries: [{ spreadPct: "0.04100000" }, { spreadPct: "0.04700000" }, { spreadPct: "0.05200000" }],
    calculationInputs: {
      liquidationBufferPct: "63.00000000",
      longLeg: {
        accountId: "fixture-long-account",
        venue: "binance",
        venueEnvironment: "demo",
        instrumentId: "binance:demo:perp:USDT:BTC-USDT",
        currentFunding: { rate: "-0.00011000" },
      },
      shortLeg: {
        accountId: "fixture-short-account",
        venue: "bybit",
        venueEnvironment: "testnet",
        instrumentId: "bybit:testnet:perp:USDT:BTC-USDT",
        currentFunding: { rate: "0.00042000" },
      },
    },
  };
}

function alert() {
  return {
    alertId: "fixture-alert-btc",
    token: "BTC",
    exchange: "Bybit",
    exchangeId: "bybit-testnet",
    currentRatePct: "0.04200000",
    thresholdLabel: "> 0.0300%",
    changePct: "0.00600000",
    severity: "warning",
    relatedOpportunityId: "fixture-opp-btc",
    nextFundingAt: "2026-08-01T16:00:00.000Z",
  };
}

function positions(state, params) {
  const closedOnly = params.get("state") === "closed";
  if (closedOnly) return { items: state.positionOpen ? [] : [closedLedger()] };
  return { items: state.positionOpen ? [openPosition()] : [] };
}

function openPosition() {
  return {
    positionId: "fixture-position-open",
    lineageId: "fixture-lineage-1",
    symbol: "BTC-USDT",
    longVenue: "Binance",
    shortVenue: "Bybit",
    longLeg: { venue: "binance", venueEnvironment: "demo", instrumentId: "binance:demo:perp:USDT:BTC-USDT" },
    shortLeg: { venue: "bybit", venueEnvironment: "testnet", instrumentId: "bybit:testnet:perp:USDT:BTC-USDT" },
    settlement: "USDT",
    state: "open",
    status: "open",
    capitalUsdt: "5000.00000000",
    singleLegNotional: "5000.00000000",
    expectedNetAprPct: "18.42000000",
    unrealizedPnlUsd: "6.00000000",
    hedgeDeltaPct: "0.09000000",
    liquidationBufferPct: "64.50000000",
    nextFundingAt: "2026-08-01T16:00:00.000Z",
  };
}

function positionLedger(state, positionId) {
  if (positionId === "fixture-position-open" && state.positionOpen) return openPosition();
  if (positionId === "fixture-position-open" && !state.positionOpen) return closedLedger();
  return {};
}

function closedLedger() {
  return {
    positionId: "fixture-position-open",
    lineageId: "fixture-lineage-1",
    principalId: "fixture-principal",
    mode: "test",
    symbol: "BTC-USDT",
    longLeg: { accountId: "fixture-long-account", venue: "binance", symbol: "BTC-USDT", instrumentId: "binance:demo:perp:USDT:BTC-USDT", venueEnvironment: "demo" },
    shortLeg: { accountId: "fixture-short-account", venue: "bybit", symbol: "BTC-USDT", instrumentId: "bybit:testnet:perp:USDT:BTC-USDT", venueEnvironment: "testnet" },
    openedAt: "2026-08-01T10:00:00.000Z",
    closedAt: "2026-08-01T12:00:00.000Z",
    reportingAsset: "USDT",
    realizedTradingPnl: "6.00000000",
    unrealizedTradingPnl: "0.00000000",
    actualFundingCashFlow: "2.74000000",
    fees: "-1.25000000",
    netPnl: "7.49000000",
    closedTradingUpnl: "0.00000000",
    verificationStatus: "verified",
    residual: { legQuantity: "0.00000000", reportingAssetNotional: "0.00000000", openOrderCount: 0, pendingCompensation: 0, blockerClass: "" },
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T12:00:00.000Z" },
    conversionSource: "fixture-static-usdt-usd",
    conversionAsOf: "2026-08-01T12:00:00.000Z",
    roundingMode: "HALF_EVEN",
    scale: 8,
    formulaVersion: "fixture-ledger-v1",
  };
}

function portfolioLedger(state) {
  return {
    principalId: "fixture-principal",
    mode: "test",
    asOf: "2026-08-01T12:00:00.000Z",
    reportingAsset: "USDT",
    cumulativeRealizedNetPnl: state.positionOpen ? "0.00000000" : "7.49000000",
    currentOpenUnrealizedPnl: state.positionOpen ? "6.00000000" : "0.00000000",
    totalNetPnl: state.positionOpen ? "6.00000000" : "7.49000000",
    actualFundingCashFlow: state.positionOpen ? "0.00000000" : "2.74000000",
    fees: state.positionOpen ? "0.00000000" : "-1.25000000",
    openPositionCount: state.positionOpen ? 1 : 0,
    closedPositionCount: state.positionOpen ? 0 : 1,
    verificationStatus: "verified",
    residual: { legQuantity: "0.00000000", reportingAssetNotional: "0.00000000", openOrderCount: 0, pendingCompensation: 0, blockerClass: "" },
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T12:00:00.000Z" },
    conversionSource: "fixture-static-usdt-usd",
    conversionAsOf: "2026-08-01T12:00:00.000Z",
    roundingMode: "HALF_EVEN",
    scale: 8,
    formulaVersion: "fixture-ledger-v1",
  };
}

function fundingPayments() {
  return {
    items: [
      {
        paymentId: "fixture-funding-long",
        accountId: "fixture-long-account",
        instrumentId: "binance:demo:perp:USDT:BTC-USDT",
        venue: "binance",
        venueEnvironment: "demo",
        product: "perp",
        settlementAsset: "USDT",
        amount: "-0.76000000",
        effectiveAt: "2026-08-01T12:00:00.000Z",
        observedAt: "2026-08-01T12:00:00.000Z",
        revision: "fixture-rev-1",
        version: "fixture-v1",
        sourceRef: "fixture-only",
        formulaVersion: "fixture-funding-v1",
      },
      {
        paymentId: "fixture-funding-short",
        accountId: "fixture-short-account",
        instrumentId: "bybit:testnet:perp:USDT:BTC-USDT",
        venue: "bybit",
        venueEnvironment: "testnet",
        product: "perp",
        settlementAsset: "USDT",
        amount: "3.50000000",
        effectiveAt: "2026-08-01T12:00:00.000Z",
        observedAt: "2026-08-01T12:00:00.000Z",
        revision: "fixture-rev-1",
        version: "fixture-v1",
        sourceRef: "fixture-only",
        formulaVersion: "fixture-funding-v1",
      },
    ],
    status: "complete",
    asOf: "2026-08-01T12:00:00.000Z",
    completeThrough: "2026-08-01T12:00:00.000Z",
    revision: "fixture-rev-1",
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T12:00:00.000Z" },
  };
}

function verification() {
  return {
    status: "verified",
    reason: "fixture_verified_closed_form_ledger",
    details: "Fixture ledger is deterministic and isolated; it is not real Gateway Test execution.",
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T12:00:00.000Z" },
    openReady: false,
    closeReady: true,
    recoveryReady: true,
  };
}

function execution(executionId, operationKind, state, message) {
  return {
    executionId,
    operationKind,
    state,
    status: state,
    latestEventAt: "2026-08-01T12:00:00.000Z",
    blockingReason: state === "completed" ? "" : message,
    manualInterventionRequired: false,
    legs: [
      { legId: `${executionId}-long`, state, venue: "binance", filledQuantity: "0.10000000" },
      { legId: `${executionId}-short`, state, venue: "bybit", filledQuantity: "0.10000000" },
    ],
    fills: state === "completed" ? [
      { fillId: `${executionId}-fill-long`, fee: { amount: "0.62500000", currency: "USDT" } },
      { fillId: `${executionId}-fill-short`, fee: { amount: "0.62500000", currency: "USDT" } },
    ] : [],
    message,
    fixtureOnly: true,
  };
}

function activity(activityId, kind, operation, status, message, occurredAt) {
  return {
    activityId,
    positionId: "fixture-position-open",
    executionId: operation === "close" ? "fixture-exec-close-1" : "fixture-exec-open-existing",
    occurredAt,
    kind,
    operation,
    status,
    state: status,
    symbol: "BTC-USDT",
    value: operation === "close" ? "7.49000000" : "5000.00000000",
    message,
  };
}

function redact(value) {
  if (Array.isArray(value)) return value.map(redact);
  if (!value || typeof value !== "object") return value;
  const output = {};
  for (const [key, raw] of Object.entries(value)) {
    if (/password|secret|token|cookie|authorization|session/i.test(key)) {
      output[key] = "[redacted]";
    } else {
      output[key] = redact(raw);
    }
  }
  return output;
}

if (import.meta.url === `file://${process.argv[1]}`) {
  const portArgIndex = process.argv.findIndex((arg) => arg === "--port");
  const envPort = process.env.FT_BROWSER_FIXTURE_PORT;
  const port = Number(portArgIndex >= 0 ? process.argv[portArgIndex + 1] : envPort || DEFAULT_PORT);
  const fixture = await startBrowserFixtureServer({ port: Number.isFinite(port) ? port : DEFAULT_PORT });
  process.stdout.write(`browser fixture listening on ${fixture.origin}\n`);
}
