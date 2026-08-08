import {
  AuthGenerationGuard,
  StaleAuthGenerationError,
  authGeneration,
} from "../state/authGeneration";

const DEFAULT_BASE_PATH = "/api/v1";
const MUTATION_METHODS = new Set(["POST", "PUT", "PATCH", "DELETE"]);

export interface ApiErrorOptions {
  status?: number;
  code?: string;
  retryable?: boolean;
  details?: unknown;
  requestId?: string;
  response?: Response | null;
}

export class ApiError extends Error {
  readonly status: number;
  readonly code: string;
  readonly retryable: boolean;
  readonly details: unknown;
  readonly requestId: string;
  readonly response: Response | null;
  readonly isAuthError: boolean;

  constructor(message = "request failed", options: ApiErrorOptions = {}) {
    super(message);
    this.name = "ApiError";
    this.status = options.status ?? 0;
    this.code = options.code ?? "REQUEST_FAILED";
    this.retryable = options.retryable ?? false;
    this.details = options.details ?? null;
    this.requestId = options.requestId ?? "";
    this.response = options.response ?? null;
    this.isAuthError = this.status === 401 || this.code === "AUTH_REQUIRED";
  }
}

export interface ApiEnvelope<T> {
  meta?: {
    requestId?: string;
    contractVersion?: string;
    apiVersion?: string;
    [key: string]: unknown;
  };
  data: T;
  [key: string]: unknown;
}

export type ApiQueryValue = string | number | boolean | null | undefined;

export interface ApiRequestOptions {
  query?: Record<string, ApiQueryValue | ApiQueryValue[]>;
  headers?: HeadersInit;
  requestId?: string;
  csrfToken?: string;
  idempotencyKey?: string;
  idempotencyScope?: string;
  signal?: AbortSignal;
  allowStatuses?: number[];
}

export interface ApiClientOptions {
  basePath?: string;
  fetchImpl?: typeof fetch;
  requestIdFactory?: () => string;
  idempotencyKeyFactory?: (scope: string) => string;
  authGuard?: AuthGenerationGuard;
}

interface ErrorEnvelope {
  error?: {
    code?: string;
    message?: string;
    retryable?: boolean;
    details?: unknown;
    requestId?: string;
  };
  meta?: { requestId?: string };
}

export class ApiClient {
  private readonly basePath: string;
  private readonly fetchImpl?: typeof fetch;
  private readonly requestIdFactory: () => string;
  private readonly idempotencyKeyFactory: (scope: string) => string;
  private readonly authGuard: AuthGenerationGuard;
  private readonly idempotencyKeys = new Map<string, string>();
  private readonly sessionExpiredListeners = new Set<(error: ApiError) => void>();
  private csrfToken = "";

  constructor(options: ApiClientOptions = {}) {
    this.basePath = normalizeBasePath(options.basePath ?? DEFAULT_BASE_PATH);
    this.fetchImpl = options.fetchImpl ?? globalThis.fetch?.bind(globalThis);
    this.requestIdFactory = options.requestIdFactory ?? generateRequestId;
    this.idempotencyKeyFactory = options.idempotencyKeyFactory ?? generateIdempotencyKey;
    this.authGuard = options.authGuard ?? authGeneration;
    this.authGuard.onBoundary(() => {
      this.csrfToken = "";
      this.idempotencyKeys.clear();
    });
  }

  get<T>(path: string, options: ApiRequestOptions = {}): Promise<ApiEnvelope<T> | null> {
    return this.request<T>("GET", path, undefined, options);
  }

  post<T>(path: string, body?: unknown, options: ApiRequestOptions = {}): Promise<ApiEnvelope<T> | null> {
    return this.request<T>("POST", path, body, options);
  }

  put<T>(path: string, body?: unknown, options: ApiRequestOptions = {}): Promise<ApiEnvelope<T> | null> {
    return this.request<T>("PUT", path, body, options);
  }

  patch<T>(path: string, body?: unknown, options: ApiRequestOptions = {}): Promise<ApiEnvelope<T> | null> {
    return this.request<T>("PATCH", path, body, options);
  }

  delete<T>(path: string, options: ApiRequestOptions = {}): Promise<ApiEnvelope<T> | null> {
    return this.request<T>("DELETE", path, undefined, options);
  }

  setSession(session: { csrfToken?: string | null }): void {
    const token = String(session.csrfToken ?? "").trim();
    if (!token || token === this.csrfToken) return;
    this.authGuard.advance("session-established");
    this.csrfToken = token;
  }

  clearSession(reason = "session-cleared"): void {
    this.authGuard.advance(reason);
  }

  onSessionExpired(listener: (error: ApiError) => void): () => void {
    this.sessionExpiredListeners.add(listener);
    return () => this.sessionExpiredListeners.delete(listener);
  }

  idempotencyKey(scope: string): string {
    const normalizedScope = String(scope).trim();
    if (!normalizedScope) throw new TypeError("idempotency scope is required");
    const existing = this.idempotencyKeys.get(normalizedScope);
    if (existing) return existing;
    const key = this.idempotencyKeyFactory(normalizedScope);
    this.idempotencyKeys.set(normalizedScope, key);
    return key;
  }

  clearIdempotencyKey(scope: string): void {
    this.idempotencyKeys.delete(String(scope).trim());
  }

  async request<T>(
    method: string,
    path: string,
    body?: unknown,
    options: ApiRequestOptions = {},
  ): Promise<ApiEnvelope<T> | null> {
    if (!this.fetchImpl) {
      throw new ApiError("fetch is unavailable", { code: "FETCH_UNAVAILABLE" });
    }

    const normalizedMethod = method.toUpperCase();
    const normalizedPath = validatePath(path);
    const requestId = options.requestId?.trim() || this.requestIdFactory();
    const csrfToken = options.csrfToken?.trim() || this.csrfToken;
    const mutation = MUTATION_METHODS.has(normalizedMethod);
    const login = normalizedMethod === "POST" && normalizedPath === "/auth/login";
    if (mutation && !login && !csrfToken) {
      throw new ApiError("CSRF token is required for mutations", {
        code: "MISSING_CSRF_TOKEN",
        requestId,
      });
    }

    const headers = new Headers(options.headers);
    headers.set("Accept", "application/json");
    headers.set("X-Request-Id", requestId);
    if (mutation && !login) headers.set("X-CSRF-Token", csrfToken);
    const idempotencyKey = options.idempotencyKey?.trim()
      || (options.idempotencyScope ? this.idempotencyKey(options.idempotencyScope) : "");
    if (idempotencyKey) headers.set("Idempotency-Key", idempotencyKey);
    if (body !== undefined) headers.set("Content-Type", "application/json");

    const auth = this.authGuard.capture();
    const signal = combineSignals(auth.signal, options.signal);
    let response: Response;
    try {
      response = await this.fetchImpl(buildUrl(this.basePath, normalizedPath, options.query), {
        method: normalizedMethod,
        credentials: "include",
        headers,
        body: body === undefined ? undefined : JSON.stringify(body),
        signal,
      });
    } catch (error) {
      auth.assertCurrent();
      if (options.signal?.aborted) throw error;
      throw new ApiError(error instanceof Error ? error.message : "network request failed", {
        code: "NETWORK_ERROR",
        retryable: true,
        requestId,
      });
    }

    const envelope = await parseResponse(response);
    auth.assertCurrent();
    if (!response.ok && !options.allowStatuses?.includes(response.status) || isErrorEnvelope(envelope)) {
      const apiError = errorFromResponse(response, envelope, requestId);
      if (apiError.isAuthError) {
        this.clearSession("session-expired");
        for (const listener of this.sessionExpiredListeners) listener(apiError);
      }
      throw apiError;
    }

    if (response.status !== 204 && envelope === null) {
      throw new ApiError("Platform API returned an invalid response", {
        status: response.status,
        code: "INVALID_RESPONSE",
        retryable: true,
        requestId,
        response,
      });
    }

    if (normalizedMethod === "DELETE" && normalizedPath === "/auth/session") {
      this.clearSession("logout");
    } else if (login && envelope) {
      const csrf = extractCsrfToken(envelope);
      if (csrf) this.setSession({ csrfToken: csrf });
    }
    return envelope as ApiEnvelope<T> | null;
  }
}

export function generateIdempotencyKey(scope = "operation"): string {
  return `${sanitizePrefix(scope)}_${Date.now().toString(36)}_${randomToken()}`;
}

function generateRequestId(): string {
  return `req_${Date.now().toString(36)}_${randomToken()}`;
}

function randomToken(): string {
  if (globalThis.crypto?.randomUUID) {
    return globalThis.crypto.randomUUID().replaceAll("-", "").slice(0, 20);
  }
  const bytes = new Uint8Array(12);
  globalThis.crypto?.getRandomValues?.(bytes);
  return Array.from(bytes, (byte) => byte.toString(16).padStart(2, "0")).join("")
    || Math.random().toString(36).slice(2, 14);
}

function sanitizePrefix(value: string): string {
  return value.replace(/[^a-zA-Z0-9_-]/g, "-").slice(0, 32) || "operation";
}

function normalizeBasePath(basePath: string): string {
  const value = basePath.trim();
  if (!value.startsWith("/") || value.startsWith("//") || value.includes("://")) {
    throw new TypeError("basePath must be a same-origin path");
  }
  return value.replace(/\/+$/, "") || DEFAULT_BASE_PATH;
}

function validatePath(path: string): string {
  const value = String(path).trim();
  if (!value.startsWith("/") || value.startsWith("//")) {
    throw new TypeError("API paths must start with /");
  }
  if (value.split(/[?#]/, 1)[0].split("/").includes("..")) {
    throw new TypeError("API path cannot escape /api/v1");
  }
  return value;
}

function buildUrl(
  basePath: string,
  path: string,
  queryValues?: Record<string, ApiQueryValue | ApiQueryValue[]>,
): string {
  const [pathname, existingQuery = ""] = path.split("?", 2);
  const query = new URLSearchParams(existingQuery);
  for (const [name, raw] of Object.entries(queryValues ?? {})) {
    const values = Array.isArray(raw) ? raw : [raw];
    for (const value of values) {
      if (value === null || value === undefined || value === "") continue;
      query.append(name, String(value));
    }
  }
  const encoded = query.toString();
  return `${basePath}${pathname}${encoded ? `?${encoded}` : ""}`;
}

function combineSignals(authSignal: AbortSignal, requestSignal?: AbortSignal): AbortSignal {
  if (!requestSignal) return authSignal;
  if (typeof AbortSignal.any === "function") return AbortSignal.any([authSignal, requestSignal]);
  const controller = new AbortController();
  const abort = (event: Event) => controller.abort((event.target as AbortSignal).reason);
  authSignal.addEventListener("abort", abort, { once: true });
  requestSignal.addEventListener("abort", abort, { once: true });
  if (authSignal.aborted) controller.abort(authSignal.reason);
  if (requestSignal.aborted) controller.abort(requestSignal.reason);
  return controller.signal;
}

async function parseResponse(response: Response): Promise<unknown | null> {
  if (response.status === 204) return null;
  if (!response.headers.get("Content-Type")?.toLowerCase().includes("application/json")) return null;
  try {
    return await response.json();
  } catch {
    return null;
  }
}

function isErrorEnvelope(value: unknown): value is ErrorEnvelope {
  return Boolean(value && typeof value === "object" && "error" in value);
}

function errorFromResponse(response: Response, value: unknown, requestId: string): ApiError {
  const envelope = isErrorEnvelope(value) ? value : {};
  const error = envelope.error ?? {};
  return new ApiError(
    error.message || (response.status === 401 ? "session expired" : `HTTP ${response.status}`),
    {
      status: response.status,
      code: error.code || (response.status === 401 ? "AUTH_REQUIRED" : "HTTP_ERROR"),
      retryable: error.retryable ?? response.status >= 500,
      details: error.details,
      requestId: error.requestId || envelope.meta?.requestId || requestId,
      response,
    },
  );
}

function extractCsrfToken(envelope: unknown): string {
  if (!envelope || typeof envelope !== "object" || !("data" in envelope)) return "";
  const data = envelope.data;
  if (!data || typeof data !== "object" || !("session" in data)) return "";
  const session = data.session;
  if (!session || typeof session !== "object" || !("csrfToken" in session)) return "";
  return typeof session.csrfToken === "string" ? session.csrfToken.trim() : "";
}

export { StaleAuthGenerationError };

export const apiClient = new ApiClient();
