/* ---------------------------------------------------------------------------
 * The transport.
 *
 * One `fetch` wrapper, and everything the browser needs to talk to the auth
 * edge correctly: the session cookie, the CSRF token, the error envelope, and
 * the epoch guard that stops one principal's answers from painting another
 * principal's screen.
 *
 * This file knows nothing about routes or response shapes. It returns the
 * parsed body and lets `endpoints.ts` say what that body is — the services use
 * four different envelopes, and which one a route speaks is a fact about the
 * route, not about the transport.
 *
 * Every path is same-origin and relative. In development the Vite server
 * proxies `/api` to the auth edge (see vite.config.ts); in production the same
 * origin serves both. There is deliberately no configurable base URL: a
 * cross-origin base would mean the session cookie stops being sent, and the
 * failure would look like "logged out" rather than "misconfigured".
 * ------------------------------------------------------------------------- */

import { ApiError, NetworkError, StaleSessionError, type ApiErrorBody } from "./errors";

/** `internal/httpx.Meta` — present on every response except the gateway plane's. */
export interface ResponseMeta {
  requestId: string;
  contractVersion: string;
  apiVersion: string;
}

/* --- Session epoch ------------------------------------------------------- */

/**
 * The session epoch. Bumped whenever the identity behind the cookie changes:
 * login, logout, or an expiry detected by any request.
 *
 * A request captures the epoch when it starts and compares it when it lands. If
 * they differ, the answer is about a principal who is no longer signed in and
 * is discarded as a StaleSessionError.
 *
 * This discards the ANSWER, never the EFFECT. A mutation that completed during
 * a logout still happened at the venue; the guard only prevents its result from
 * being rendered for whoever is signed in now. Anything that needs to know
 * whether a write landed must ask again after signing in, not infer it from
 * this error.
 */
let sessionEpoch = 0;

export function currentSessionEpoch(): number {
  return sessionEpoch;
}

/** Invalidates every in-flight request. Call on login, logout and expiry. */
export function advanceSessionEpoch(): void {
  sessionEpoch += 1;
}

/* --- CSRF ---------------------------------------------------------------- */

/**
 * The CSRF token, held in memory only.
 *
 * The session cookie is HttpOnly, so this token is the one half of the pair
 * JavaScript can see — which is exactly what makes it work. It arrives in the
 * body of login and `/me`, and the auth edge requires it on every unsafe method
 * (`proxy.go:216`). Deliberately not persisted: a token in localStorage
 * outlives the session it was minted for, and would be replayed on the next
 * page load against a cookie that no longer matches it.
 */
let csrfToken: string | null = null;

export function setCsrfToken(token: string | null): void {
  csrfToken = token;
}

export function currentCsrfToken(): string | null {
  return csrfToken;
}

/* --- Requests ------------------------------------------------------------ */

const SAFE_METHODS = new Set(["GET", "HEAD", "OPTIONS"]);

/**
 * How long a request may take before it is abandoned.
 *
 * Generous, because the slow paths are real work rather than a stall: an
 * execution preview and an order placement both cross the auth edge, trading
 * and the gateway to reach a venue. A timeout tight enough to feel responsive
 * would abort orders that were about to succeed, and an aborted order is the
 * one outcome the operator cannot resolve by looking at the screen.
 */
const DEFAULT_TIMEOUT_MS = 20_000;

export interface RequestOptions {
  method?: string;
  /** Serialised as JSON. Omit for a body-less request. */
  body?: unknown;
  query?: QueryParams;
  /**
   * Sent as `Idempotency-Key`. Trading compares it byte-for-byte against the
   * `idempotencyKey` in the body and rejects a mismatch with 400, so the two
   * are never derived separately — `endpoints.ts` sets both from one value.
   */
  idempotencyKey?: string;
  /**
   * Sent as `If-Match`. The gateway's account lifecycle answers 428
   * `IF_MATCH_REQUIRED` without it on every mutation but create: an account's
   * desired state may only be changed by someone who has seen the revision
   * they are changing.
   */
  ifMatch?: string;
  signal?: AbortSignal;
  timeoutMs?: number;
}

export type QueryParams = Record<string, string | number | boolean | null | undefined>;

/**
 * Performs one request and returns its parsed body.
 *
 * Throws ApiError when the server answered and said no, NetworkError when
 * nothing answered, and StaleSessionError when the session moved underneath the
 * request. It never returns a failure as a value — every caller here is a query
 * or a mutation whose framework expects a rejected promise.
 */
export async function apiRequest<T>(path: string, options: RequestOptions = {}): Promise<T> {
  const { method = "GET", body, query, idempotencyKey, ifMatch, signal, timeoutMs = DEFAULT_TIMEOUT_MS } = options;
  const epoch = sessionEpoch;

  const headers: Record<string, string> = { Accept: "application/json" };
  if (body !== undefined) {
    headers["Content-Type"] = "application/json";
  }
  if (!SAFE_METHODS.has(method) && csrfToken) {
    headers["X-CSRF-Token"] = csrfToken;
  }
  if (idempotencyKey) {
    headers["Idempotency-Key"] = idempotencyKey;
  }
  if (ifMatch) {
    headers["If-Match"] = ifMatch;
  }

  let response: Response;
  try {
    response = await fetch(buildUrl(path, query), {
      method,
      headers,
      body: body === undefined ? undefined : JSON.stringify(body),
      // Same-origin rather than "include": the cookie is same-origin by
      // construction, and "include" would quietly keep working if someone
      // later pointed this at another host — hiding the moment the deployment
      // stopped matching what the auth edge's cookie policy assumes.
      credentials: "same-origin",
      signal: deadline(signal, timeoutMs),
    });
  } catch (cause) {
    if (sessionEpoch !== epoch) throw new StaleSessionError();
    // A caller-cancelled request is not a transport failure. Rethrowing the
    // AbortError as-is lets the query layer recognise its own cancellation
    // instead of reporting a network problem that did not happen.
    if (signal?.aborted) throw cause;
    throw new NetworkError(cause);
  }

  if (sessionEpoch !== epoch) throw new StaleSessionError();

  if (!response.ok) {
    throw new ApiError(response.status, await errorBody(response));
  }
  // 204 is the whole answer for `DELETE /api/v1/auth/session`. Reading a body
  // that was never sent would throw inside the JSON parse and report a logout
  // that worked as a failure.
  if (response.status === 204 || response.headers.get("Content-Length") === "0") {
    return undefined as T;
  }
  try {
    return (await response.json()) as T;
  } catch (cause) {
    throw new ApiError(response.status, {
      code: "INVALID_RESPONSE",
      message: "The server sent a response this client could not read",
      details: { path },
      retryable: false,
      requestId: response.headers.get("X-Request-Id") ?? "",
    });
  }
}

function buildUrl(path: string, query?: QueryParams): string {
  if (!query) return path;
  const params = new URLSearchParams();
  for (const [key, value] of Object.entries(query)) {
    // An absent optional means "let the server decide", which is not the same
    // as sending it empty — trading rejects a named-but-blank query field.
    if (value === undefined || value === null || value === "") continue;
    params.set(key, String(value));
  }
  const encoded = params.toString();
  return encoded === "" ? path : `${path}?${encoded}`;
}

/**
 * Combines the caller's cancellation with a deadline of our own, so a request
 * nobody is waiting for cannot hold a connection open indefinitely.
 */
function deadline(signal: AbortSignal | undefined, timeoutMs: number): AbortSignal {
  const timeout = AbortSignal.timeout(timeoutMs);
  return signal ? AbortSignal.any([signal, timeout]) : timeout;
}

/**
 * Reads the error envelope, and invents one when the failure came from
 * somewhere that does not speak it.
 *
 * Not every non-2xx passes through `internal/httpx`: a dev proxy with no
 * upstream, a gateway timeout, or a static host answering the SPA fallback all
 * produce a body that is not our envelope. Those must still become an ApiError
 * with a code, because the alternative is `undefined.code` at the point where
 * the UI decides what to show.
 */
async function errorBody(response: Response): Promise<ApiErrorBody> {
  const requestId = response.headers.get("X-Request-Id") ?? "";
  let parsed: unknown;
  try {
    parsed = await response.json();
  } catch {
    return {
      code: fallbackCode(response.status),
      message: response.statusText || `HTTP ${response.status}`,
      details: {},
      retryable: response.status >= 500,
      requestId,
    };
  }
  const envelope = (parsed as { error?: Partial<ApiErrorBody> } | null)?.error;
  if (!envelope || typeof envelope.code !== "string") {
    return {
      code: fallbackCode(response.status),
      message: response.statusText || `HTTP ${response.status}`,
      details: {},
      retryable: response.status >= 500,
      requestId,
    };
  }
  return {
    code: envelope.code,
    message: envelope.message ?? "",
    details: envelope.details ?? {},
    retryable: envelope.retryable ?? false,
    requestId: envelope.requestId || requestId,
  };
}

/**
 * The 401 case is the one that matters: `isAuthFailure` branches on status as
 * well as code, so an unenveloped 401 from a proxy hop still signs the operator
 * out rather than showing an unexplained error.
 */
function fallbackCode(status: number): string {
  if (status === 401) return "AUTH_REQUIRED";
  if (status === 403) return "AUTH_REQUIRED";
  if (status === 404) return "NOT_FOUND";
  if (status === 409) return "CONFLICT";
  if (status === 429) return "RATE_LIMITED";
  return status >= 500 ? "UPSTREAM_UNAVAILABLE" : "REQUEST_FAILED";
}
