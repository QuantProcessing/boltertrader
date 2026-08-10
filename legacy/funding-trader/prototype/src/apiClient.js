const DEFAULT_BASE_PATH = "/api/v1";
const WRITE_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);
const SERVER_CONTROLLED_QUERY_KEYS = new Set(["environment", "mode"]);

export class ApiError extends Error {
  constructor(message, options = {}) {
    super(message || "request failed");
    this.name = "ApiError";
    this.status = options.status ?? 0;
    this.code = options.code ?? "request_failed";
    this.retryable = Boolean(options.retryable);
    this.details = options.details ?? null;
    this.requestId = options.requestId ?? "";
    this.envelope = options.envelope ?? null;
    this.response = options.response ?? null;
    this.isAuthError = this.status === 401 || this.code === "unauthenticated";
  }
}

export function generateIdempotencyKey(prefix = "idem") {
  const safePrefix = String(prefix || "idem").replace(/[^a-zA-Z0-9_-]/g, "-").slice(0, 32) || "idem";
  const random = randomToken();
  return `${safePrefix}_${Date.now().toString(36)}_${random}`;
}

function defaultRequestIdFactory() {
  return `req_${Date.now().toString(36)}_${randomToken()}`;
}

function randomToken() {
  const cryptoObject = globalThis.crypto;
  if (cryptoObject?.randomUUID) {
    return cryptoObject.randomUUID().replaceAll("-", "").slice(0, 20);
  }
  if (cryptoObject?.getRandomValues) {
    const bytes = new Uint8Array(12);
    cryptoObject.getRandomValues(bytes);
    return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("");
  }
  return Math.random().toString(36).slice(2, 14);
}

export class FundingTraderApiClient {
  constructor({
    basePath = DEFAULT_BASE_PATH,
    fetchImpl = globalThis.fetch?.bind(globalThis),
    requestIdFactory = defaultRequestIdFactory,
    idempotencyKeyFactory = generateIdempotencyKey,
  } = {}) {
    if (typeof fetchImpl !== "function") {
      throw new TypeError("FundingTraderApiClient requires a fetch implementation");
    }
    this.basePath = normalizeBasePath(basePath);
    this.fetchImpl = fetchImpl;
    this.requestIdFactory = requestIdFactory;
    this.idempotencyKeyFactory = idempotencyKeyFactory;
  }

  login(credentials, options = {}) {
    return this.request("POST", "/auth/login", { body: credentials, ...options });
  }

  getSession(options = {}) {
    return this.request("GET", "/auth/session", options);
  }

  logout({ csrfToken, ...options } = {}) {
    return this.request("DELETE", "/auth/session", { csrfToken, ...options });
  }

  getMe(options = {}) {
    return this.request("GET", "/me", options);
  }

  getSystemStatus(options = {}) {
    return this.request("GET", "/system/status", options);
  }

  getReadiness(options = {}) {
    return this.request("GET", "/system/readiness", { allowStatuses: [503], ...options });
  }

  getVerification(params = {}, options = {}) {
    return this.request("GET", "/system/verification", { params, ...options });
  }

  getPortfolioLedger(params = {}, options = {}) {
    return this.request("GET", "/ledger/portfolio", { params, ...options });
  }

  listFundingPayments(params = {}, options = {}) {
    return this.request("GET", "/ledger/funding-payments", { params, ...options });
  }

  listOpportunities(params = {}, options = {}) {
    return this.request("GET", "/opportunities", { params, ...options });
  }

  getOpportunity(opportunityId, options = {}) {
    return this.request("GET", `/opportunities/${encodePathSegment(opportunityId)}`, options);
  }

  listAlerts(params = {}, options = {}) {
    return this.request("GET", "/funding-rate-alerts", { params, ...options });
  }

  createExecutionPreview(payload, { csrfToken, ...options } = {}) {
    return this.request("POST", "/execution-previews", { body: payload, csrfToken, ...options });
  }

  submitOrder(payload, { csrfToken, idempotencyKey, ...options } = {}) {
    return this.request("POST", "/orders", {
      body: payload,
      csrfToken,
      idempotencyKey: requiredIdempotencyKey(idempotencyKey),
      ...options,
    });
  }

  getExecution(executionId, options = {}) {
    return this.request("GET", `/executions/${encodePathSegment(executionId)}`, options);
  }

  listPositions(params = {}, options = {}) {
    return this.request("GET", "/positions", { params, ...options });
  }

  listClosedPositions(params = {}, options = {}) {
    return this.request("GET", "/positions", { params: { ...params, state: "closed" }, ...options });
  }

  getPositionLedger(positionId, options = {}) {
    return this.request("GET", `/positions/${encodePathSegment(positionId)}`, options);
  }

  closePosition(positionId, { csrfToken, idempotencyKey, ...options } = {}) {
    return this.request("POST", `/positions/${encodePathSegment(positionId)}/close`, {
      body: null,
      csrfToken,
      idempotencyKey: requiredIdempotencyKey(idempotencyKey),
      ...options,
    });
  }

  listActivities(params = {}, options = {}) {
    return this.request("GET", "/activities", { params, ...options });
  }

  listAuditEvents(params = {}, options = {}) {
    return this.request("GET", "/audit-events", { params, ...options });
  }

  async request(method, path, {
    body,
    csrfToken,
    idempotencyKey,
    params,
    requestId,
    allowStatuses = [],
    headers = {},
    signal,
  } = {}) {
    const normalizedMethod = String(method || "GET").toUpperCase();
    const url = this.url(path, params);
    const resolvedRequestId = requestId || this.requestIdFactory();
    const requestHeaders = new Headers(headers);
    requestHeaders.set("Accept", "application/json");
    requestHeaders.set("X-Request-Id", resolvedRequestId);

    const hasBody = body !== undefined && body !== null;
    if (hasBody) {
      requestHeaders.set("Content-Type", "application/json");
    }
    if (csrfToken) {
      requestHeaders.set("X-CSRF-Token", csrfToken);
    }
    if (idempotencyKey) {
      requestHeaders.set("Idempotency-Key", idempotencyKey);
    }
    if (WRITE_METHODS.has(normalizedMethod) && (normalizedMethod === "POST") && path !== "/auth/login" && !idempotencyKey && (path === "/orders" || path.endsWith("/close"))) {
      throw new ApiError("idempotency key is required for execution writes", {
        status: 0,
        code: "missing_idempotency_key",
        retryable: false,
        requestId: resolvedRequestId,
      });
    }

    let response;
    try {
      response = await this.fetchImpl(url, {
        method: normalizedMethod,
        credentials: "include",
        headers: requestHeaders,
        body: hasBody ? JSON.stringify(body) : undefined,
        signal,
      });
    } catch (error) {
      throw new ApiError(error?.message || "network request failed", {
        status: 0,
        code: "network_error",
        retryable: true,
        requestId: resolvedRequestId,
      });
    }

    const envelope = await parseResponseBody(response);
    const allowed = response.ok || allowStatuses.includes(response.status);
    if (!allowed || envelope?.error) {
      throw errorFromEnvelope(response, envelope, resolvedRequestId);
    }
    if (envelope === null) {
      throw new ApiError("backend returned an invalid response", {
        status: response.status,
        code: "invalid_response",
        retryable: true,
        requestId: resolvedRequestId,
        response,
      });
    }
    return envelope;
  }

  url(path, params) {
    if (!String(path || "").startsWith("/")) {
      throw new TypeError("API paths must start with /");
    }
    const query = encodeQuery(params);
    return `${this.basePath}${path}${query}`;
  }
}

function normalizeBasePath(basePath) {
  const value = String(basePath || DEFAULT_BASE_PATH).trim();
  if (!value.startsWith("/")) {
    throw new TypeError("basePath must be same-origin and start with /");
  }
  return value.replace(/\/+$/, "") || DEFAULT_BASE_PATH;
}

function encodePathSegment(value) {
  const text = String(value ?? "").trim();
  if (!text) {
    throw new TypeError("path segment is required");
  }
  return encodeURIComponent(text);
}

function encodeQuery(params) {
  if (!params) return "";
  const query = new URLSearchParams();
  for (const [key, rawValue] of Object.entries(params)) {
    if (SERVER_CONTROLLED_QUERY_KEYS.has(key)) continue;
    if (rawValue === undefined || rawValue === null || rawValue === "") continue;
    const values = Array.isArray(rawValue) ? rawValue : [rawValue];
    for (const value of values) {
      if (value === undefined || value === null || value === "") continue;
      query.append(key, String(value));
    }
  }
  const serialized = query.toString();
  return serialized ? `?${serialized}` : "";
}

function requiredIdempotencyKey(value) {
  const key = String(value ?? "").trim();
  if (!key) return "";
  return key;
}

async function parseResponseBody(response) {
  const contentType = response.headers?.get?.("content-type") || "";
  if (!contentType.includes("application/json")) {
    return null;
  }
  try {
    return await response.json();
  } catch {
    return null;
  }
}

function errorFromEnvelope(response, envelope, fallbackRequestId) {
  const error = envelope?.error ?? {};
  const code = error.code || (response.status === 401 ? "unauthenticated" : "http_error");
  const message = error.message || (response.status === 401 ? "session expired" : `HTTP ${response.status}`);
  return new ApiError(message, {
    status: response.status,
    code,
    retryable: error.retryable,
    details: error.details ?? null,
    requestId: error.requestId || envelope?.meta?.requestId || fallbackRequestId,
    envelope,
    response,
  });
}

export const apiClient = new FundingTraderApiClient();
