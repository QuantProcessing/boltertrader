import { watchSessionEpoch } from "@/api/client";
import { paths } from "@/api/endpoints";

export type RealtimeTopic =
  | "market.order_book"
  | "market.bbo"
  | "market.candles"
  | "market.mark_price"
  | "market.index_price"
  | "trading.orders"
  | "funding.board"
  | "system.venue_connectivity"
  | "account.events";

export interface RealtimeTopicRequest {
  topic: RealtimeTopic;
  instrumentId?: string;
  depth?: number;
  interval?: string;
}

export interface RealtimeMessage<T = unknown> {
  type:
    | "ack"
    | "snapshot"
    | "delta"
    | "full"
    | "resync_required"
    | "error"
    | "pong";
  requestId?: string;
  subscriptionId?: string;
  topic?: RealtimeTopic;
  generation?: number;
  sequence?: number;
  sourceSequence?: string;
  observedAt?: string;
  data?: T;
  code?: string;
  reason?: string;
}

type Listener = (message: RealtimeMessage) => void;
export type RealtimeConnectionState =
  | "idle"
  | "connecting"
  | "online"
  | "degraded"
  | "reconnecting";
type StateListener = (state: RealtimeConnectionState) => void;

interface TopicEntry {
  request: RealtimeTopicRequest;
  listeners: Set<Listener>;
  requestId?: string;
  subscriptionId?: string;
  generation: number;
  sequence: number;
  degradedResources: Set<string>;
  replay?: RealtimeMessage;
  replayedAt?: number;
  retryTimer?: number;
}

/**
 * One browser-wide multiplexed socket for all service-owned subscriptions. Components own
 * logical subscriptions; duplicate requests still share one server topic.
 */
export class RealtimeTopicManager {
  private socket: WebSocket | null = null;
  private entries = new Map<string, TopicEntry>();
  private pending = new Map<string, string>();
  private active = new Map<string, string>();
  private reconnectTimer: number | undefined;
  private heartbeatTimer: number | undefined;
  private pongTimer: number | undefined;
  private attempts = 0;
  private nextRequest = 0;
  private intentionallyClosed = false;
  private state: RealtimeConnectionState = "idle";
  private stateListeners = new Set<StateListener>();

  constructor(private readonly path: string) {}

  watchState(listener: StateListener): () => void {
    this.stateListeners.add(listener);
    listener(this.state);
    return () => this.stateListeners.delete(listener);
  }

  subscribe(request: RealtimeTopicRequest, listener: Listener): () => void {
    const key = topicKey(request);
    let entry = this.entries.get(key);
    if (!entry) {
      entry = {
        request: { ...request },
        listeners: new Set(),
        generation: 0,
        sequence: 0,
        degradedResources: new Set(),
      };
      this.entries.set(key, entry);
    }
    entry.listeners.add(listener);
    if (entry.replay) {
      const data = entry.replay.data as Record<string, unknown> | undefined;
      listener(
        data && typeof data.ageMs === "number"
          ? {
              ...entry.replay,
              data: {
                ...data,
                ageMs:
                  data.ageMs +
                  Math.max(0, Date.now() - (entry.replayedAt ?? Date.now())),
              },
            }
          : entry.replay,
      );
    }
    this.intentionallyClosed = false;
    this.ensureConnected();
    if (
      this.socket?.readyState === WebSocket.OPEN &&
      !entry.requestId &&
      !entry.subscriptionId
    ) {
      this.sendSubscribe(key, entry);
    }
    return () => this.unsubscribe(key, listener);
  }

  private ensureConnected(): void {
    if (
      typeof WebSocket === "undefined" ||
      this.socket ||
      this.entries.size === 0
    )
      return;
    this.setState(this.attempts === 0 ? "connecting" : "reconnecting");
    const url = new URL(this.path, window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url.toString());
    this.socket = socket;
    socket.addEventListener("open", () => {
      if (this.socket !== socket) return;
      this.attempts = 0;
      this.setState("online");
      this.startHeartbeat(socket);
      this.pending.clear();
      this.active.clear();
      for (const [key, entry] of this.entries) {
        entry.requestId = undefined;
        entry.subscriptionId = undefined;
        entry.generation = 0;
        entry.sequence = 0;
        entry.degradedResources.clear();
        this.sendSubscribe(key, entry);
      }
    });
    socket.addEventListener("message", (event) =>
      this.receive(socket, event.data),
    );
    socket.addEventListener("error", () => socket.close());
    socket.addEventListener("close", () => this.disconnected(socket));
  }

  private sendSubscribe(key: string, entry: TopicEntry): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return;
    const requestId = `web-realtime-${++this.nextRequest}`;
    entry.requestId = requestId;
    this.pending.set(requestId, key);
    this.socket.send(
      JSON.stringify({ op: "subscribe", requestId, topics: [entry.request] }),
    );
  }

  private receive(socket: WebSocket, raw: unknown): void {
    if (this.socket !== socket || typeof raw !== "string") return;
    let message: RealtimeMessage;
    try {
      message = JSON.parse(raw) as RealtimeMessage;
      if (
        !message ||
        typeof message !== "object" ||
        typeof message.type !== "string"
      )
        return;
    } catch {
      return;
    }
    if (message.type === "pong") {
      if (this.pongTimer !== undefined) window.clearTimeout(this.pongTimer);
      this.pongTimer = undefined;
      return;
    }
    if (
      message.type === "ack" &&
      message.requestId?.startsWith("web-unsubscribe-")
    )
      return;
    if (message.type === "ack" && message.requestId && message.subscriptionId) {
      const key = this.pending.get(message.requestId);
      const entry = key ? this.entries.get(key) : undefined;
      this.pending.delete(message.requestId);
      if (!key || !entry || entry.requestId !== message.requestId) {
        socket.send(
          JSON.stringify({
            op: "unsubscribe",
            requestId: `web-unsubscribe-${++this.nextRequest}`,
            topics: [{ subscriptionId: message.subscriptionId }],
          }),
        );
        return;
      }
      entry.requestId = undefined;
      entry.subscriptionId = message.subscriptionId;
      this.active.set(message.subscriptionId, key);
      return;
    }
    if (message.type === "error" && message.requestId) {
      const key = this.pending.get(message.requestId);
      const entry = key ? this.entries.get(key) : undefined;
      this.pending.delete(message.requestId);
      if (entry) {
        entry.requestId = undefined;
        entry.degradedResources.add("*");
        this.notify(entry, message);
        this.refreshOpenState();
        if (message.code === "UPSTREAM_UNAVAILABLE")
          this.retry(key!, entry, socket);
      }
      return;
    }
    const key = message.subscriptionId
      ? this.active.get(message.subscriptionId)
      : undefined;
    const entry = key ? this.entries.get(key) : undefined;
    if (!entry) return;
    const generation = message.generation ?? entry.generation;
    const sequence = message.sequence ?? 0;
    if (
      generation < entry.generation ||
      (generation === entry.generation &&
        sequence > 0 &&
        sequence <= entry.sequence)
    )
      return;
    if (generation > entry.generation) entry.sequence = 0;
    entry.generation = generation;
    if (sequence > 0) entry.sequence = sequence;
    if (
      message.type === "snapshot" ||
      message.type === "delta" ||
      message.type === "full"
    ) {
      entry.replay = replayMessage(entry.replay, message);
      entry.replayedAt = Date.now();
    }
    this.notify(entry, message);
    if (
      message.type === "snapshot" ||
      message.type === "delta" ||
      message.type === "full"
    ) {
      const connection = accountConnection(message);
      if (connection?.state === "disconnected")
        entry.degradedResources.add(connection.key);
      if (connection?.state === "connected") {
        entry.degradedResources.delete("*");
        entry.degradedResources.delete(connection.key);
        entry.degradedResources.delete(`${connection.accountId}\u0000`);
      }
      if (entry.request.topic !== "account.events")
        entry.degradedResources.clear();
      this.refreshOpenState();
    }
    if (message.type === "resync_required" || message.type === "error") {
      entry.degradedResources.add("*");
      this.refreshOpenState();
      if (message.code === "UPSTREAM_GAP") return; // Hub follows with a native snapshot on this topic.
      if (message.code === "CONSUMER_LAGGED") {
        socket.close();
        return;
      }
      if (
        message.type === "resync_required" ||
        message.code === "UPSTREAM_UNAVAILABLE" ||
        message.code === "MOCK_PUBLIC_DATA_UNAVAILABLE"
      )
        this.retry(key!, entry, socket);
    }
  }

  private retry(key: string, entry: TopicEntry, socket: WebSocket): void {
    if (entry.retryTimer !== undefined) return;
    if (entry.subscriptionId) {
      this.active.delete(entry.subscriptionId);
      socket.send(
        JSON.stringify({
          op: "unsubscribe",
          requestId: `web-unsubscribe-${++this.nextRequest}`,
          topics: [{ subscriptionId: entry.subscriptionId }],
        }),
      );
    }
    entry.subscriptionId = undefined;
    entry.generation = 0;
    entry.sequence = 0;
    entry.retryTimer = window.setTimeout(() => {
      entry.retryTimer = undefined;
      if (
        this.socket === socket &&
        socket.readyState === WebSocket.OPEN &&
        this.entries.get(key) === entry &&
        !entry.requestId &&
        !entry.subscriptionId
      )
        this.sendSubscribe(key, entry);
    }, 1_000);
  }

  resetSession(): void {
    this.stopHeartbeat();
    if (this.reconnectTimer !== undefined)
      window.clearTimeout(this.reconnectTimer);
    const oldSocket = this.socket;
    this.socket = null;
    oldSocket?.close();
    this.pending.clear();
    this.active.clear();
    for (const entry of this.entries.values()) {
      entry.replay = undefined;
      entry.requestId = undefined;
      entry.subscriptionId = undefined;
      if (entry.retryTimer !== undefined) window.clearTimeout(entry.retryTimer);
      entry.retryTimer = undefined;
    }
    this.setState("idle");
    // React's session subscribers release private topics before reconnecting.
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = undefined;
      this.ensureConnected();
    }, 0);
  }

  dispose(): void {
    this.intentionallyClosed = true;
    this.stopHeartbeat();
    if (this.reconnectTimer !== undefined)
      window.clearTimeout(this.reconnectTimer);
    for (const entry of this.entries.values())
      if (entry.retryTimer !== undefined) window.clearTimeout(entry.retryTimer);
    this.entries.clear();
    this.pending.clear();
    this.active.clear();
    const socket = this.socket;
    this.socket = null;
    socket?.close();
    this.setState("idle");
    this.stateListeners.clear();
  }

  private notify(entry: TopicEntry, message: RealtimeMessage): void {
    for (const listener of entry.listeners) {
      try {
        listener(message);
      } catch (error) {
        console.error("Realtime subscriber failed", error);
      }
    }
  }

  private unsubscribe(key: string, listener: Listener): void {
    const entry = this.entries.get(key);
    if (!entry) return;
    entry.listeners.delete(listener);
    if (entry.listeners.size > 0) return;
    this.entries.delete(key);
    if (entry.retryTimer !== undefined) window.clearTimeout(entry.retryTimer);
    this.refreshOpenState();
    if (entry.requestId) this.pending.delete(entry.requestId);
    if (entry.subscriptionId) {
      this.active.delete(entry.subscriptionId);
      if (this.socket?.readyState === WebSocket.OPEN) {
        this.socket.send(
          JSON.stringify({
            op: "unsubscribe",
            requestId: `web-unsubscribe-${++this.nextRequest}`,
            topics: [
              {
                topic: entry.request.topic,
                subscriptionId: entry.subscriptionId,
              },
            ],
          }),
        );
      }
    }
    if (this.entries.size === 0) {
      this.intentionallyClosed = true;
      if (this.reconnectTimer !== undefined)
        window.clearTimeout(this.reconnectTimer);
      this.reconnectTimer = undefined;
      this.stopHeartbeat();
      this.socket?.close();
      this.socket = null;
      this.setState("idle");
    }
  }

  private disconnected(socket: WebSocket): void {
    if (this.socket !== socket) return;
    this.socket = null;
    this.stopHeartbeat();
    this.pending.clear();
    this.active.clear();
    for (const entry of this.entries.values()) {
      entry.requestId = undefined;
      entry.subscriptionId = undefined;
    }
    if (this.intentionallyClosed || this.entries.size === 0) {
      this.setState("idle");
      return;
    }
    this.setState("reconnecting");
    const delay = Math.min(10_000, 500 * 2 ** Math.min(this.attempts++, 5));
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = undefined;
      this.ensureConnected();
    }, delay);
  }

  private setState(state: RealtimeConnectionState): void {
    if (this.state === state) return;
    this.state = state;
    for (const listener of this.stateListeners) listener(state);
  }

  private refreshOpenState(): void {
    if (this.socket?.readyState !== WebSocket.OPEN) return;
    this.setState(
      [...this.entries.values()].some(
        (entry) => entry.degradedResources.size > 0,
      )
        ? "degraded"
        : "online",
    );
  }

  private startHeartbeat(socket: WebSocket): void {
    this.stopHeartbeat();
    this.heartbeatTimer = window.setInterval(() => {
      if (this.socket !== socket || socket.readyState !== WebSocket.OPEN)
        return;
      const requestId = `web-realtime-ping-${++this.nextRequest}`;
      socket.send(JSON.stringify({ op: "ping", requestId }));
      if (this.pongTimer !== undefined) window.clearTimeout(this.pongTimer);
      this.pongTimer = window.setTimeout(() => {
        if (this.socket === socket) socket.close();
      }, 10_000);
    }, 20_000);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer !== undefined)
      window.clearInterval(this.heartbeatTimer);
    if (this.pongTimer !== undefined) window.clearTimeout(this.pongTimer);
    this.heartbeatTimer = undefined;
    this.pongTimer = undefined;
  }
}

function topicKey(request: RealtimeTopicRequest): string {
  return JSON.stringify([
    request.topic,
    request.instrumentId ?? "",
    request.depth ?? 0,
    request.interval ?? "",
  ]);
}

function accountConnection(
  message: RealtimeMessage,
):
  | { accountId: string; key: string; state: "connected" | "disconnected" }
  | undefined {
  if (typeof message.data !== "object" || message.data === null)
    return undefined;
  const data = message.data as {
    accountId?: unknown;
    kind?: unknown;
    payload?: unknown;
  };
  if (
    data.kind !== "account.account_connection" ||
    typeof data.accountId !== "string" ||
    data.accountId.length === 0 ||
    typeof data.payload !== "object" ||
    data.payload === null
  )
    return undefined;
  const payload = data.payload as { route_id?: unknown; state?: unknown };
  if (payload.state !== "connected" && payload.state !== "disconnected")
    return undefined;
  const routeId = typeof payload.route_id === "string" ? payload.route_id : "";
  return {
    accountId: data.accountId,
    key: `${data.accountId}\u0000${routeId}`,
    state: payload.state,
  };
}

function replayMessage(
  previous: RealtimeMessage | undefined,
  message: RealtimeMessage,
): RealtimeMessage {
  if (
    message.topic !== "market.order_book" ||
    message.type !== "delta" ||
    !previous
  )
    return message;
  type Level = { price: string; quantity: string };
  const old = previous.data as { bids?: Level[]; asks?: Level[] };
  const next = message.data as { bids?: Level[]; asks?: Level[] };
  if (!old || !next) return message;
  const merge = (a: Level[] = [], b: Level[] = [], descending = false) => {
    const values = new Map(a.map((level) => [level.price, level]));
    for (const level of b) {
      if (Number(level.quantity) === 0) values.delete(level.price);
      else values.set(level.price, level);
    }
    return [...values.values()].sort(
      (x, y) => (Number(x.price) - Number(y.price)) * (descending ? -1 : 1),
    );
  };
  return {
    ...message,
    type: "snapshot",
    data: {
      bids: merge(old.bids, next.bids, true),
      asks: merge(old.asks, next.asks),
    },
  };
}

const sharedTopics = new RealtimeTopicManager(paths.realtimeSocket);
const stopSessionWatch = watchSessionEpoch(() => sharedTopics.resetSession());
if (import.meta.hot)
  import.meta.hot.dispose(() => {
    stopSessionWatch();
    sharedTopics.dispose();
  });
export const realtimeTopics = {
  subscribe: (request: RealtimeTopicRequest, listener: Listener) =>
    sharedTopics.subscribe(request, listener),
  watchConnection: (listener: StateListener) =>
    sharedTopics.watchState(listener),
};
