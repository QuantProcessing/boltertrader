import { paths } from "@/api/endpoints";

export type RealtimeTopic =
  | "market.order_book"
  | "market.bbo"
  | "market.candles"
  | "market.mark_price"
  | "funding.board"
  | "arbitrage.opportunities"
  | "system.venue_connectivity"
  | "account.events";

export interface RealtimeTopicRequest {
  topic: RealtimeTopic;
  instrumentId?: string;
  depth?: number;
  interval?: string;
}

export interface RealtimeMessage<T = unknown> {
  type: "ack" | "snapshot" | "delta" | "full" | "checkpoint" | "resync_required" | "error" | "pong";
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
export type RealtimeConnectionState = "idle" | "connecting" | "online" | "degraded" | "reconnecting";
type StateListener = (state: RealtimeConnectionState) => void;

interface TopicEntry {
  request: RealtimeTopicRequest;
  listeners: Set<Listener>;
  requestId?: string;
  subscriptionId?: string;
  generation: number;
  sequence: number;
  degradedResources: Set<string>;
}

/**
 * One browser-wide multiplexed socket per ownership boundary. Components own
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
      entry = { request: { ...request }, listeners: new Set(), generation: 0, sequence: 0, degradedResources: new Set() };
      this.entries.set(key, entry);
    }
    entry.listeners.add(listener);
    this.intentionallyClosed = false;
    this.ensureConnected();
    if (this.socket?.readyState === WebSocket.OPEN && !entry.requestId && !entry.subscriptionId) {
      this.sendSubscribe(key, entry);
    }
    return () => this.unsubscribe(key, listener);
  }

  private ensureConnected(): void {
    if (typeof WebSocket === "undefined" || this.socket || this.entries.size === 0) return;
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
    socket.addEventListener("message", (event) => this.receive(socket, event.data));
    socket.addEventListener("error", () => socket.close());
    socket.addEventListener("close", () => this.disconnected(socket));
  }

  private sendSubscribe(key: string, entry: TopicEntry): void {
    if (!this.socket || this.socket.readyState !== WebSocket.OPEN) return;
    const requestId = `web-realtime-${++this.nextRequest}`;
    entry.requestId = requestId;
    this.pending.set(requestId, key);
    this.socket.send(JSON.stringify({ op: "subscribe", requestId, topics: [entry.request] }));
  }

  private receive(socket: WebSocket, raw: unknown): void {
    if (this.socket !== socket || typeof raw !== "string") return;
    let message: RealtimeMessage;
    try {
      message = JSON.parse(raw) as RealtimeMessage;
    } catch {
      return;
    }
    if (message.type === "pong") {
      if (this.pongTimer !== undefined) window.clearTimeout(this.pongTimer);
      this.pongTimer = undefined;
      return;
    }
    if (message.type === "ack" && message.requestId && message.subscriptionId) {
      const key = this.pending.get(message.requestId);
      const entry = key ? this.entries.get(key) : undefined;
      this.pending.delete(message.requestId);
      if (!key || !entry) return;
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
        this.notify(entry, message);
      }
      return;
    }
    const key = message.subscriptionId ? this.active.get(message.subscriptionId) : undefined;
    const entry = key ? this.entries.get(key) : undefined;
    if (!entry) return;
    const generation = message.generation ?? entry.generation;
    const sequence = message.sequence ?? 0;
    if (generation < entry.generation || (generation === entry.generation && sequence > 0 && sequence <= entry.sequence)) return;
    if (generation > entry.generation) entry.sequence = 0;
    entry.generation = generation;
    if (sequence > 0) entry.sequence = sequence;
    this.notify(entry, message);
    if (message.type === "snapshot" || message.type === "delta" || message.type === "full" || message.type === "checkpoint") {
      const recoveredAccount = accountHealthIdentity(message);
      if (entry.request.topic !== "account.events") entry.degradedResources.clear();
      else if (recoveredAccount) entry.degradedResources.delete(recoveredAccount);
      else if (message.type === "checkpoint") entry.degradedResources.delete("*");
      this.refreshOpenState();
    }
    if (message.type === "resync_required") {
      // A market gap stays on the same logical subscription. Gateway reopens
      // the native WS feed and follows the resync marker with a new snapshot;
      // reconnecting this socket would interrupt unrelated healthy topics.
      if (message.code === "UPSTREAM_GAP") {
        entry.degradedResources.add(accountIdentity(message) ?? "*");
        this.refreshOpenState();
        return;
      }
      if (message.code === "UPSTREAM_UNAVAILABLE") {
        entry.degradedResources.add("*");
        this.refreshOpenState();
      }
      if (message.code === "CONSUMER_LAGGED") return; // server closes with 1013
      if (entry.subscriptionId) this.active.delete(entry.subscriptionId);
      entry.subscriptionId = undefined;
      entry.generation = 0;
      entry.sequence = 0;
      window.setTimeout(() => {
        if (this.socket === socket && socket.readyState === WebSocket.OPEN && this.entries.get(key!) === entry && !entry.requestId && !entry.subscriptionId) {
          this.sendSubscribe(key!, entry);
        }
      }, 250);
    }
  }

  private notify(entry: TopicEntry, message: RealtimeMessage): void {
    for (const listener of entry.listeners) listener(message);
  }

  private unsubscribe(key: string, listener: Listener): void {
    const entry = this.entries.get(key);
    if (!entry) return;
    entry.listeners.delete(listener);
    if (entry.listeners.size > 0) return;
    this.entries.delete(key);
    this.refreshOpenState();
    if (entry.requestId) this.pending.delete(entry.requestId);
    if (entry.subscriptionId) {
      this.active.delete(entry.subscriptionId);
      if (this.socket?.readyState === WebSocket.OPEN) {
        this.socket.send(JSON.stringify({
          op: "unsubscribe",
          requestId: `web-realtime-${++this.nextRequest}`,
          topics: [{ topic: entry.request.topic, subscriptionId: entry.subscriptionId }],
        }));
      }
    }
    if (this.entries.size === 0) {
      this.intentionallyClosed = true;
      if (this.reconnectTimer !== undefined) window.clearTimeout(this.reconnectTimer);
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
    this.setState([...this.entries.values()].some((entry) => entry.degradedResources.size > 0) ? "degraded" : "online");
  }

  private startHeartbeat(socket: WebSocket): void {
    this.stopHeartbeat();
    this.heartbeatTimer = window.setInterval(() => {
      if (this.socket !== socket || socket.readyState !== WebSocket.OPEN) return;
      const requestId = `web-realtime-ping-${++this.nextRequest}`;
      socket.send(JSON.stringify({ op: "ping", requestId }));
      if (this.pongTimer !== undefined) window.clearTimeout(this.pongTimer);
      this.pongTimer = window.setTimeout(() => {
        if (this.socket === socket) socket.close();
      }, 10_000);
    }, 20_000);
  }

  private stopHeartbeat(): void {
    if (this.heartbeatTimer !== undefined) window.clearInterval(this.heartbeatTimer);
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

function accountIdentity(message: RealtimeMessage): string | undefined {
  if (typeof message.data !== "object" || message.data === null) return undefined;
  const accountId = (message.data as { accountId?: unknown }).accountId;
  return typeof accountId === "string" && accountId.length > 0 ? accountId : undefined;
}

function accountHealthIdentity(message: RealtimeMessage): string | undefined {
  if (typeof message.data !== "object" || message.data === null) return undefined;
  const data = message.data as { accountId?: unknown; kind?: unknown };
  return data.kind === "account.account_health" && typeof data.accountId === "string" && data.accountId.length > 0
    ? data.accountId
    : undefined;
}

const marketTopics = new RealtimeTopicManager(paths.marketRealtimeSocket);
const accountTopics = new RealtimeTopicManager(paths.accountRealtimeSocket);
const businessTopics = new RealtimeTopicManager(paths.realtimeSocket);

/**
 * Route subscriptions to the service that owns the fact. Public venue data and
 * private exchange events no longer make an unnecessary hop through Trading.
 */
export const realtimeTopics = {
  subscribe(request: RealtimeTopicRequest, listener: Listener): () => void {
    switch (request.topic) {
      case "account.events":
        return accountTopics.subscribe(request, listener);
      case "arbitrage.opportunities":
        return businessTopics.subscribe(request, listener);
      default:
        return marketTopics.subscribe(request, listener);
    }
  },
  watchConnection(owner: "market" | "account" | "business", listener: StateListener): () => void {
    return { market: marketTopics, account: accountTopics, business: businessTopics }[owner].watchState(listener);
  },
};
