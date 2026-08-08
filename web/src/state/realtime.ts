import {
  AuthGenerationGuard,
  type AuthBoundary,
  authGeneration,
} from "./authGeneration";

const DEFAULT_URL = "/api/v1/realtime/stream";

export const REALTIME_EVENT_KINDS = Object.freeze([
  "system.ready",
  "system.degraded",
  "auth.session.created",
  "auth.session.expired",
  "account.desired.created",
  "account.desired.updated",
  "account.desired.revision_changed",
  "account.rotation.fenced",
  "account.runtime.updated",
  "account.runtime.stale_write_rejected",
  "reservation.created",
  "reservation.released",
  "reservation.blocked",
  "command.accepted",
  "command.duplicate",
  "command.blocked",
  "command.uncertain",
  "command.finalized",
  "live.private_write_denied",
  "funding.worker.registered",
  "funding.worker.draining",
  "funding.worker.stale",
  "funding.work.claimed",
  "funding.work.retry_scheduled",
  "funding.work.stale_lease_rejected",
  "funding.work.manual_intervention",
  "audit.recorded",
  "migration.one_writer_fence_raised",
  "order.accepted",
  "order.submitted",
  "order.partially_filled",
  "order.filled",
  "order.canceled",
  "fill.received",
  "position.updated",
  "ledger.updated",
  "funding.payment_posted",
  "pnl.updated",
  "catalog.updated",
  "market_data.updated",
  "realtime.resnapshot_required",
  "realtime.sequence_gap",
  "resnapshot.required",
] as const);

const RESNAPSHOT_EVENTS = new Set([
  "realtime.resnapshot_required",
  "realtime.sequence_gap",
  "resnapshot.required",
]);

export interface RealtimeEnvelope {
  eventId: string;
  sequence: number;
  cursor: string;
  observedAt: string;
  payload?: unknown;
  kind?: string;
  [key: string]: unknown;
}

export type RealtimeStatus =
  | "connecting"
  | "connected"
  | "reconnecting"
  | "disconnected"
  | "resnapshot-required"
  | "auth-boundary";

export interface RealtimeState {
  status: RealtimeStatus;
  cursor: string;
  reconnectAttempt: number;
  lastSequence: number;
  lastEventAt: string;
  lastError: unknown;
  requiresResnapshot: boolean;
}

export interface RealtimeMessage {
  type: "event" | "status" | "transport" | "malformed" | "resnapshot-required";
  event?: string;
  data?: unknown;
  envelope?: RealtimeEnvelope;
  error?: unknown;
  reason?: string;
  delayMs?: number;
  status: RealtimeStatus;
  state: RealtimeState;
}

export interface RealtimeSourceEvent {
  data?: string;
  error?: unknown;
}

export interface EventSourceLike {
  onopen: ((event: RealtimeSourceEvent) => void) | null;
  onerror: ((event: RealtimeSourceEvent) => void) | null;
  onmessage: ((event: RealtimeSourceEvent) => void) | null;
  addEventListener(type: string, listener: (event: RealtimeSourceEvent) => void): void;
  close(): void;
}

export type EventSourceConstructor = new (
  url: string,
  options: { withCredentials?: boolean },
) => EventSourceLike;

export interface RealtimeBackoff {
  initialMs: number;
  maxMs: number;
  factor: number;
}

export interface RealtimeOptions {
  url?: string;
  EventSourceImpl?: EventSourceConstructor;
  authGuard?: AuthGenerationGuard;
  backoff?: Partial<RealtimeBackoff>;
  eventKinds?: readonly string[];
  onEvent?: (message: RealtimeMessage) => void;
  setTimeoutImpl?: (callback: () => void, delay: number) => unknown;
  clearTimeoutImpl?: (handle: unknown) => void;
}

const DEFAULT_BACKOFF: RealtimeBackoff = {
  initialMs: 500,
  maxMs: 8_000,
  factor: 2,
};

export class RealtimeController {
  private readonly url: string;
  private readonly EventSourceImpl: EventSourceConstructor;
  private readonly backoff: RealtimeBackoff;
  private readonly eventKinds: readonly string[];
  private readonly authGuard: AuthGenerationGuard;
  private readonly listeners = new Set<(message: RealtimeMessage) => void>();
  private readonly setTimeoutImpl: (callback: () => void, delay: number) => unknown;
  private readonly clearTimeoutImpl: (handle: unknown) => void;
  private source: EventSourceLike | null = null;
  private reconnectTimer: unknown | null = null;
  private detachAuthBoundary: (() => void) | null = null;
  private closed = true;
  private state: RealtimeState = {
    status: "disconnected",
    cursor: "",
    reconnectAttempt: 0,
    lastSequence: 0,
    lastEventAt: "",
    lastError: null,
    requiresResnapshot: false,
  };

  constructor(options: RealtimeOptions = {}) {
    this.url = validateUrl(options.url ?? DEFAULT_URL);
    const source = options.EventSourceImpl
      ?? (globalThis.EventSource as unknown as EventSourceConstructor | undefined);
    if (!source) throw new TypeError("RealtimeController requires EventSource");
    this.EventSourceImpl = source;
    this.backoff = { ...DEFAULT_BACKOFF, ...options.backoff };
    validateBackoff(this.backoff);
    this.eventKinds = [...new Set(options.eventKinds ?? REALTIME_EVENT_KINDS)];
    this.setTimeoutImpl = options.setTimeoutImpl
      ?? ((callback, delay) => globalThis.setTimeout(callback, delay));
    this.clearTimeoutImpl = options.clearTimeoutImpl
      ?? ((handle) => globalThis.clearTimeout(handle as ReturnType<typeof setTimeout>));
    if (options.onEvent) this.listeners.add(options.onEvent);
    this.authGuard = options.authGuard ?? authGeneration;
    this.attachAuthBoundary();
  }

  connect(): this {
    this.attachAuthBoundary();
    this.closed = false;
    this.state = {
      ...this.state,
      status: "connecting",
      lastError: null,
      requiresResnapshot: false,
    };
    this.emit({ type: "status" });
    this.open();
    return this;
  }

  subscribe(listener: (message: RealtimeMessage) => void): () => void {
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  getState(): RealtimeState {
    return { ...this.state };
  }

  close(): void {
    this.closed = true;
    this.clearReconnectTimer();
    this.closeSource();
    this.detachAuthBoundary?.();
    this.detachAuthBoundary = null;
    this.state = { ...this.state, status: "disconnected" };
    this.emit({ type: "status" });
  }

  resumeAfterSnapshot(): void {
    if (!this.state.requiresResnapshot) return;
    this.clearReconnectTimer();
    this.closeSource();
    this.closed = false;
    this.state = {
      ...this.state,
      status: "connecting",
      cursor: "",
      lastSequence: 0,
      reconnectAttempt: 0,
      lastError: null,
      requiresResnapshot: false,
    };
    this.emit({ type: "status" });
    this.open();
  }

  private open(): void {
    if (this.closed || this.state.requiresResnapshot) return;
    const source = new this.EventSourceImpl(this.buildUrl(), { withCredentials: true });
    this.source = source;
    source.onopen = () => {
      if (source !== this.source) return;
      this.state = {
        ...this.state,
        status: "connected",
        reconnectAttempt: 0,
        lastError: null,
      };
      this.emit({ type: "status" });
    };
    source.onerror = (event) => {
      if (source !== this.source || this.closed || this.state.requiresResnapshot) return;
      this.state = { ...this.state, status: "disconnected", lastError: event.error ?? event };
      this.emit({ type: "transport", error: event.error ?? event });
      this.scheduleReconnect();
    };
    for (const kind of this.eventKinds) {
      source.addEventListener(kind, (event) => this.handleEvent(kind, event));
    }
    source.onmessage = (event) => this.handleEvent("message", event);
  }

  private handleEvent(kind: string, sourceEvent: RealtimeSourceEvent): void {
    const parsed = parseEnvelope(sourceEvent.data);
    const eventKind = kind === "message" && parsed.ok && parsed.value.kind
      ? parsed.value.kind
      : kind;
    if (!parsed.ok) {
      this.emit({ type: "malformed", event: eventKind, error: parsed.error });
      return;
    }
    const envelope = parsed.value;
    if (RESNAPSHOT_EVENTS.has(eventKind)) {
      this.requireResnapshot(eventKind, envelope);
      return;
    }
    if (this.state.lastSequence !== 0 && envelope.sequence !== this.state.lastSequence + 1) {
      this.requireResnapshot("sequence-gap", envelope);
      return;
    }
    this.state = {
      ...this.state,
      cursor: envelope.cursor,
      lastSequence: envelope.sequence,
      lastEventAt: envelope.observedAt,
    };
    this.emit({
      type: "event",
      event: eventKind,
      data: envelope.payload ?? envelope,
      envelope,
    });
  }

  private requireResnapshot(reason: string, envelope: RealtimeEnvelope): void {
    this.clearReconnectTimer();
    this.closeSource();
    this.state = {
      ...this.state,
      status: "resnapshot-required",
      cursor: "",
      requiresResnapshot: true,
      lastError: reason,
    };
    this.emit({ type: "resnapshot-required", event: reason, reason, envelope });
  }

  private scheduleReconnect(): void {
    if (this.reconnectTimer !== null || this.closed || this.state.requiresResnapshot) return;
    this.closeSource();
    const attempt = this.state.reconnectAttempt + 1;
    const delayMs = Math.min(
      this.backoff.maxMs,
      Math.round(this.backoff.initialMs * this.backoff.factor ** (attempt - 1)),
    );
    this.state = { ...this.state, status: "reconnecting", reconnectAttempt: attempt };
    this.emit({ type: "status", delayMs });
    this.reconnectTimer = this.setTimeoutImpl(() => {
      this.reconnectTimer = null;
      this.open();
    }, delayMs);
  }

  private handleAuthBoundary(boundary: AuthBoundary): void {
    this.closed = true;
    this.clearReconnectTimer();
    this.closeSource();
    this.detachAuthBoundary?.();
    this.detachAuthBoundary = null;
    this.state = {
      status: "auth-boundary",
      cursor: "",
      reconnectAttempt: 0,
      lastSequence: 0,
      lastEventAt: "",
      lastError: boundary.reason,
      requiresResnapshot: false,
    };
    this.emit({ type: "status", reason: boundary.reason });
  }

  private attachAuthBoundary(): void {
    if (this.detachAuthBoundary) return;
    this.detachAuthBoundary = this.authGuard.onBoundary(
      (boundary) => this.handleAuthBoundary(boundary),
    );
  }

  private buildUrl(): string {
    const [pathname, existingQuery = ""] = this.url.split("?", 2);
    const query = new URLSearchParams(existingQuery);
    if (this.state.cursor) query.set("cursor", this.state.cursor);
    else query.delete("cursor");
    const encoded = query.toString();
    return `${pathname}${encoded ? `?${encoded}` : ""}`;
  }

  private clearReconnectTimer(): void {
    if (this.reconnectTimer === null) return;
    this.clearTimeoutImpl(this.reconnectTimer);
    this.reconnectTimer = null;
  }

  private closeSource(): void {
    if (!this.source) return;
    this.source.close();
    this.source = null;
  }

  private emit(message: Omit<RealtimeMessage, "state" | "status">): void {
    const complete = {
      ...message,
      status: this.state.status,
      state: this.getState(),
    } as RealtimeMessage;
    for (const listener of this.listeners) listener(complete);
  }
}

export function connectRealtime(options: RealtimeOptions = {}): RealtimeController {
  return new RealtimeController(options).connect();
}

function validateUrl(url: string): string {
  const value = url.trim();
  if (!value.startsWith("/") || value.startsWith("//") || value.includes("://")) {
    throw new TypeError("realtime URL must be a same-origin path");
  }
  return value;
}

function validateBackoff(backoff: RealtimeBackoff): void {
  if (
    backoff.initialMs <= 0
    || backoff.maxMs < backoff.initialMs
    || backoff.factor < 1
    || !Number.isFinite(backoff.initialMs + backoff.maxMs + backoff.factor)
  ) {
    throw new TypeError("realtime backoff is invalid");
  }
}

function parseEnvelope(data: string | undefined):
  | { ok: true; value: RealtimeEnvelope }
  | { ok: false; error: Error } {
  let value: unknown;
  try {
    value = JSON.parse(data ?? "");
  } catch {
    return { ok: false, error: new Error("realtime event is not valid JSON") };
  }
  if (!value || typeof value !== "object") {
    return { ok: false, error: new Error("realtime event must be an object") };
  }
  const candidate = value as Partial<RealtimeEnvelope>;
  if (
    typeof candidate.eventId !== "string"
    || !candidate.eventId
    || !Number.isSafeInteger(candidate.sequence)
    || Number(candidate.sequence) <= 0
    || typeof candidate.cursor !== "string"
    || !candidate.cursor
    || typeof candidate.observedAt !== "string"
    || !candidate.observedAt
    || Number.isNaN(Date.parse(candidate.observedAt))
  ) {
    return { ok: false, error: new Error("realtime event metadata is invalid") };
  }
  return { ok: true, value: candidate as RealtimeEnvelope };
}
