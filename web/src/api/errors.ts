/* ---------------------------------------------------------------------------
 * Error taxonomy.
 *
 * Every failure the UI can encounter arrives as one of three things, and they
 * need to be told apart because the operator's next move differs:
 *
 *   ApiError       the server answered, and said no. There is a code, and often
 *                  a `retryable` flag the server computed. Show it.
 *   NetworkError   nothing answered. Retrying is reasonable; the request may
 *                  also have been received and executed. Never assume it wasn't.
 *   StaleSession   the request was abandoned because the session it was
 *                  authorized under is gone. Not a failure — say nothing.
 * ------------------------------------------------------------------------- */

// Type-only, so it is erased: `lib/i18n` writes `document.documentElement.lang`
// at module scope, while the API error taxonomy must remain side-effect free.
import type { Translate } from "@/lib/i18n";

/** The `error` object in `internal/httpx.WriteError`'s envelope. */
export interface ApiErrorBody {
  code: string;
  message: string;
  /** Never absent: `httpx` substitutes `{}` for a nil map. */
  details: Record<string, unknown>;
  retryable: boolean;
  requestId: string;
}

export class ApiError extends Error {
  override readonly name = "ApiError";
  readonly status: number;
  readonly code: string;
  readonly details: Record<string, unknown>;
  readonly retryable: boolean;
  readonly requestId: string;

  constructor(status: number, body: ApiErrorBody) {
    super(body.message || body.code || `HTTP ${status}`);
    this.status = status;
    this.code = body.code;
    this.details = body.details;
    this.retryable = body.retryable;
    this.requestId = body.requestId;
  }
}

/**
 * The transport failed: DNS, TCP, TLS, CORS, or the tab going offline.
 *
 * Deliberately NOT retryable-by-default at this layer. A `fetch` that rejects
 * proves only that no response came back — for a GET that is harmless, but for
 * a place-order POST the venue may well have the order. The retry decision
 * belongs to the caller, who knows whether the request carried an idempotency
 * key that makes a repeat safe.
 */
export class NetworkError extends Error {
  override readonly name = "NetworkError";
  override readonly cause: unknown;

  constructor(cause: unknown) {
    super(cause instanceof Error ? cause.message : "Network request failed");
    this.cause = cause;
  }
}

/**
 * Thrown when a request is discarded because the session boundary moved under
 * it — a logout, a login as someone else, or an expiry detected elsewhere.
 *
 * This is not an error condition to report. It means the answer would have been
 * about a principal who is no longer signed in, and showing it would be worse
 * than showing nothing.
 */
export class StaleSessionError extends Error {
  override readonly name = "StaleSessionError";

  constructor() {
    super("Request was abandoned because the session changed");
  }
}

export function isApiError(value: unknown): value is ApiError {
  return value instanceof ApiError;
}

/**
 * A failure rendered for a human, carrying the request id when there is one.
 *
 * The id is the only handle that ties what the operator saw to what the server
 * logged, so it travels with the message rather than being kept for a debug
 * view nobody opens while something is going wrong.
 *
 * Outside React, so it takes `t` rather than calling `useT()`. What gets
 * translated here is only ever this client's own words: a server's sentence is
 * printed as the server wrote it, which is the same rule the login screen
 * follows when it prints a refused password.
 */
export function describeError(error: unknown, t: Translate): string {
  if (isApiError(error)) {
    // `INVALID_RESPONSE` is the one body no server sent — `client.ts` builds it
    // when the JSON will not parse, so its message is ours to say.
    // The gateway intentionally redacts validation causes because this plane
    // accepts credentials. Translate its deliberately generic sentence into
    // something actionable without trying to expose the rejected secret.
    const genericValidation =
      error.code === "VALIDATION_ERROR" &&
      (error.message === "request denied" || error.message === "request is invalid");
    const message =
      error.code === "INVALID_RESPONSE"
        ? t("error.unreadable")
        : genericValidation
          ? t("error.validation")
          : error.message;
    return error.requestId
      ? `${message} (${error.code} · ${error.requestId})`
      : `${message} (${error.code})`;
  }
  // Not the transport's own words: `cause.message` here is "Failed to fetch" or
  // whichever phrase the browser picked, which is English whoever is reading and
  // does not say the thing that matters — that a write may have landed anyway.
  if (error instanceof NetworkError) return t("error.network");
  return error instanceof Error ? error.message : t("error.unknown");
}

export function isStaleSession(value: unknown): value is StaleSessionError {
  return value instanceof StaleSessionError;
}

/**
 * Does this failure mean "you are not signed in"?
 *
 * All three tests are needed, and each catches something the others miss.
 * Trading answers 403 with `AUTH_REQUIRED` for an unauthorized mutation
 * (`trading.go:255`), so status alone under-counts. A 401 from a proxy hop
 * short of the handler would carry no code at all, so code alone under-counts
 * too. And `authedge` rejects a bad CSRF token with its own 403 `CSRF_REQUIRED`
 * (`proxy.go:219`) — a different cause with the same remedy, since the token is
 * minted with the session and the only way to get a good one is to sign in
 * again.
 */
export function isAuthFailure(value: unknown): boolean {
  if (!isApiError(value)) return false;
  return value.status === 401 || value.code === "AUTH_REQUIRED" || value.code === "CSRF_REQUIRED";
}

/**
 * Error codes the UI branches on. Everything else is rendered by message.
 *
 * Kept as a const object rather than an enum so the values stay plain strings
 * at runtime — they are compared against whatever the server sends, and an enum
 * would imply this list is exhaustive when the server is free to add codes.
 */
export const ErrorCode = {
  AuthRequired: "AUTH_REQUIRED",
  /** The session is real but the CSRF token is not. Same remedy: sign in again. */
  CsrfRequired: "CSRF_REQUIRED",
  ValidationError: "VALIDATION_ERROR",
  NotFound: "NOT_FOUND",
  Conflict: "CONFLICT",
  RateLimited: "RATE_LIMITED",
  /** An operator disarmed `BT_LIVE_PRIVATE_WRITES_ENABLED`. Not a transient condition. */
  LiveWriteDisabled: "LIVE_PRIVATE_WRITE_DISABLED",
  PublicDataNotReady: "PUBLIC_DATA_NOT_READY",
  RealtimeUnavailable: "REALTIME_UNAVAILABLE",
	/** The stream cannot prove continuity; discard local deltas and refetch authority. */
  ResnapshotRequired: "RESNAPSHOT_REQUIRED",
  RuntimeProjectionUnavailable: "RUNTIME_PROJECTION_UNAVAILABLE",
  CommandProjectionUnavailable: "COMMAND_PROJECTION_UNAVAILABLE",
  FillProjectionUnavailable: "FILL_PROJECTION_UNAVAILABLE",
  ActivityProjectionUnavailable: "ACTIVITY_PROJECTION_UNAVAILABLE",
  Internal: "INTERNAL",
} as const;

export type ErrorCodeValue = (typeof ErrorCode)[keyof typeof ErrorCode];

/**
 * A projection that is still warming up answers 503 with its own code. They all
 * mean the same thing to the operator — "this table isn't ready yet, it will
 * be" — so the UI shows a waiting state rather than an error for any of them.
 */
export function isProjectionWarmingUp(value: unknown): boolean {
  if (!isApiError(value) || value.status !== 503) return false;
  return value.code.endsWith("_PROJECTION_UNAVAILABLE") || value.code === ErrorCode.PublicDataNotReady;
}
