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
  cursor?: string;
}

export interface RealtimeMessage<T = unknown> {
  type: "ack" | "snapshot" | "delta" | "checkpoint" | "resync_required" | "error" | "pong";
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

interface TopicEntry {
  request: RealtimeTopicRequest;
  listeners: Set<Listener>;
  requestId?: string;
  subscriptionId?: string;
  generation: number;
  sequence: number;
}

/**
 * One browser-wide multiplexed socket. Components own logical subscriptions;
 * duplicate requests share one server topic and one upstream subscription.
 */
class RealtimeTopicManager {
  private socket: WebSocket | null = null;
  private entries = new Map<string, TopicEntry>();
  private pending = new Map<string, string>();
  private active = new Map<string, string>();
  private reconnectTimer: number | undefined;
  private attempts = 0;
  private nextRequest = 0;
  private intentionallyClosed = false;

  subscribe(request: RealtimeTopicRequest, listener: Listener): () => void {
    const key = topicKey(request);
    let entry = this.entries.get(key);
    if (!entry) {
      entry = { request: { ...request }, listeners: new Set(), generation: 0, sequence: 0 };
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
    const url = new URL(paths.realtimeSocket, window.location.href);
    url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
    const socket = new WebSocket(url.toString());
    this.socket = socket;
    socket.addEventListener("open", () => {
      if (this.socket !== socket) return;
      this.attempts = 0;
      this.pending.clear();
      this.active.clear();
      for (const [key, entry] of this.entries) {
        entry.requestId = undefined;
        entry.subscriptionId = undefined;
        entry.generation = 0;
        entry.sequence = 0;
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
    if (entry.request.topic === "account.events" && typeof message.data === "object" && message.data !== null) {
      const cursor = (message.data as { cursor?: unknown }).cursor;
      if (typeof cursor === "string" && cursor !== "") entry.request.cursor = cursor;
    }
    this.notify(entry, message);
    if (message.type === "resync_required") {
      // A book gap stays on the same logical subscription: Gateway follows
      // the resync marker with a new-generation snapshot. Cursor expiry and a
      // topic-level upstream failure instead require only that topic to be
      // recreated; do not tear down every healthy topic on the browser socket.
      if (message.code === "UPSTREAM_GAP") return;
      if (message.code === "CONSUMER_LAGGED") return; // server closes with 1013
      if (entry.request.topic === "account.events") entry.request.cursor = undefined;
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
      this.socket?.close();
      this.socket = null;
    }
  }

  private disconnected(socket: WebSocket): void {
    if (this.socket !== socket) return;
    this.socket = null;
    this.pending.clear();
    this.active.clear();
    for (const entry of this.entries.values()) {
      entry.requestId = undefined;
      entry.subscriptionId = undefined;
    }
    if (this.intentionallyClosed || this.entries.size === 0) return;
    const delay = Math.min(10_000, 500 * 2 ** Math.min(this.attempts++, 5));
    this.reconnectTimer = window.setTimeout(() => {
      this.reconnectTimer = undefined;
      this.ensureConnected();
    }, delay);
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

export const realtimeTopics = new RealtimeTopicManager();
