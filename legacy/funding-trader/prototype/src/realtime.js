export const EVENT_KINDS = Object.freeze([
  "snapshot",
  "system.status",
  "opportunity.upsert",
  "alert.upsert",
  "execution.upsert",
  "position.upsert",
  "activity.append",
  "audit.append",
  "ledger.funding_payment.append",
  "ledger.position.verification_changed",
  "ledger.portfolio.updated",
  "verification.status",
  "resnapshot.required",
  "heartbeat",
]);

export const SERVER_FILTERABLE_EVENT_KINDS = Object.freeze([
  "system.status",
  "opportunity.upsert",
  "alert.upsert",
  "execution.upsert",
  "position.upsert",
  "activity.append",
  "audit.append",
  "ledger.funding_payment.append",
  "ledger.position.verification_changed",
  "ledger.portfolio.updated",
  "verification.status",
  "resnapshot.required",
]);

export const PRIVATE_V1_CONTRACT_VERSION = "private-v1-mode-1";

const DATA_EVENTS = new Set([
  "system.status",
  "opportunity.upsert",
  "alert.upsert",
  "execution.upsert",
  "position.upsert",
  "activity.append",
  "audit.append",
  "ledger.funding_payment.append",
  "ledger.position.verification_changed",
  "ledger.portfolio.updated",
  "verification.status",
]);

const DEFAULT_BACKOFF = Object.freeze({
  initialMs: 500,
  maxMs: 8000,
  factor: 2,
});
const SERVER_CONTROLLED_QUERY_KEYS = Object.freeze(["environment", "mode"]);

export class FundingTraderRealtime {
  constructor({
    url = "/api/v1/stream",
    EventSourceImpl = globalThis.EventSource,
    setTimeoutImpl = globalThis.setTimeout?.bind(globalThis),
    clearTimeoutImpl = globalThis.clearTimeout?.bind(globalThis),
    backoff = DEFAULT_BACKOFF,
    kinds = null,
  } = {}) {
    if (typeof EventSourceImpl !== "function") {
      throw new TypeError("FundingTraderRealtime requires an EventSource implementation");
    }
    this.url = url;
    this.EventSourceImpl = EventSourceImpl;
    this.setTimeoutImpl = setTimeoutImpl;
    this.clearTimeoutImpl = clearTimeoutImpl;
    this.backoff = { ...DEFAULT_BACKOFF, ...backoff };
    this.kinds = validateFilterKinds(kinds);
    this.listeners = new Set();
    this.source = null;
    this.reconnectTimer = null;
    this.closed = true;
    this.state = {
      status: "disconnected",
      cursor: "",
      reconnectAttempt: 0,
      lastEventAt: "",
      lastError: null,
    };
  }

  subscribe(listener) {
    if (typeof listener !== "function") {
      throw new TypeError("listener must be a function");
    }
    this.listeners.add(listener);
    return () => this.listeners.delete(listener);
  }

  getState() {
    return { ...this.state };
  }

  connect({ cursor = this.state.cursor, kinds = this.kinds } = {}) {
    this.close({ permanent: false });
    this.closed = false;
    this.kinds = validateFilterKinds(kinds);
    this.state = {
      ...this.state,
      cursor: String(cursor || ""),
      status: "degraded",
      lastError: null,
    };
    this.emit({ type: "status", status: this.state.status });
    this.open();
    return this;
  }

  close({ permanent = true } = {}) {
    if (permanent) this.closed = true;
    if (this.reconnectTimer) {
      this.clearTimeoutImpl(this.reconnectTimer);
      this.reconnectTimer = null;
    }
    if (this.source) {
      this.source.close();
      this.source = null;
    }
    if (permanent) {
      this.state = { ...this.state, status: "disconnected" };
      this.emit({ type: "status", status: this.state.status });
    }
  }

  open() {
    const source = new this.EventSourceImpl(this.buildUrl(), { withCredentials: true });
    this.source = source;

    source.onopen = () => {
      this.state = {
        ...this.state,
        status: "connected",
        reconnectAttempt: 0,
        lastError: null,
      };
      this.emit({ type: "status", status: this.state.status });
    };

    source.onerror = (event) => {
      this.emitTransport(event);
      if (!this.closed) this.scheduleReconnect();
    };

    for (const kind of EVENT_KINDS) {
      source.addEventListener(kind, (event) => this.handleEvent(kind, event));
    }
    source.onmessage = (event) => this.handleEvent("message", event);
  }

  buildUrl() {
    const [path, existingQuery = ""] = String(this.url).split("?");
    const query = new URLSearchParams(existingQuery);
    for (const key of SERVER_CONTROLLED_QUERY_KEYS) query.delete(key);
    if (this.state.cursor) query.set("cursor", this.state.cursor);
    for (const kind of this.kinds) query.append("kind", kind);
    const serialized = query.toString();
    return serialized ? `${path}?${serialized}` : path;
  }

  handleEvent(kind, event) {
    const parsed = parseSSEData(event?.data);
    if (!parsed.ok) {
      this.emit({
        type: "malformed",
        event: kind,
        error: parsed.error,
        raw: event?.data ?? "",
      });
      return;
    }
    const envelope = parsed.value ?? {};
    const resnapshotRequired = kind === "resnapshot.required";
    if (resnapshotRequired) {
      this.state = { ...this.state, cursor: "" };
    } else if (typeof envelope.cursor === "string" && envelope.cursor) {
      this.state = { ...this.state, cursor: envelope.cursor };
    }
    this.state = {
      ...this.state,
      lastEventAt: envelope.occurredAt || new Date().toISOString(),
    };

    const type = eventTypeFor(kind);
    const message = {
      type,
      event: kind,
      data: envelope.data ?? envelope,
      cursor: this.state.cursor,
      envelope,
    };
    if (resnapshotRequired) {
      message.scope = Array.isArray(envelope.scope) ? envelope.scope : ["all"];
      if (!this.closed) this.connect({ cursor: "", kinds: this.kinds });
    }
    this.emit(message);
  }

  emitTransport(event) {
    const error = event?.error || event;
    this.state = {
      ...this.state,
      status: "disconnected",
      lastError: error || "transport_error",
    };
    this.emit({ type: "transport", status: this.state.status, error });
    this.emit({ type: "status", status: this.state.status });
  }

  scheduleReconnect() {
    if (this.reconnectTimer) return;
    if (this.source) {
      this.source.close();
      this.source = null;
    }
    const attempt = this.state.reconnectAttempt + 1;
    const delay = Math.min(
      this.backoff.maxMs,
      Math.round(this.backoff.initialMs * (this.backoff.factor ** (attempt - 1))),
    );
    this.state = { ...this.state, reconnectAttempt: attempt, status: "degraded" };
    this.emit({ type: "status", status: this.state.status, delayMs: delay });
    this.reconnectTimer = this.setTimeoutImpl(() => {
      this.reconnectTimer = null;
      if (!this.closed) this.open();
    }, delay);
  }

  emit(message) {
    for (const listener of this.listeners) {
      listener({ ...message, state: this.getState() });
    }
  }
}

export function createRealtimeController(options) {
  return new FundingTraderRealtime(options);
}

export function validateRealtimeEnvelope(envelope, expected) {
  if (String(envelope?.principalId ?? "") !== String(expected?.principalId ?? "")) {
    return { ok: false, reason: "principal-mismatch" };
  }
  if (String(envelope?.mode ?? "") !== String(expected?.mode ?? "")) {
    return { ok: false, reason: "mode-mismatch" };
  }
  if (String(envelope?.contractVersion ?? "") !== String(expected?.contractVersion ?? "")) {
    return { ok: false, reason: "contract-version-mismatch" };
  }
  return { ok: true };
}

export function realtimeDisplayStatus(status) {
  if (status === "connected") return "stream connected";
  if (status === "disconnected" || status === "closed" || status === "idle") {
    return "stream disconnected";
  }
  return "stream degraded";
}

function parseSSEData(data) {
  try {
    return { ok: true, value: data ? JSON.parse(data) : {} };
  } catch (error) {
    return { ok: false, error };
  }
}

function eventTypeFor(kind) {
  if (kind === "snapshot") return "snapshot";
  if (kind === "resnapshot.required") return "resnapshot";
  if (kind === "heartbeat") return "heartbeat";
  if (DATA_EVENTS.has(kind)) return "data";
  return "data";
}

function validateFilterKinds(kinds) {
  if (kinds === null || kinds === undefined) return [];
  const values = Array.isArray(kinds) ? kinds : [kinds];
  return values.filter((kind, index) => {
    if (!EVENT_KINDS.includes(kind)) {
      throw new TypeError(`unsupported stream event kind: ${kind}`);
    }
    return SERVER_FILTERABLE_EVENT_KINDS.includes(kind) && values.indexOf(kind) === index;
  });
}
