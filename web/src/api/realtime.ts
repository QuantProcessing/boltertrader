// One WebSocket to /api/ws, multiplexing every subscription. Subscriptions are
// reference-counted per topic; the socket reconnects with backoff and
// re-subscribes. Retained channels (book, candle, ticker, account.*) deliver
// their latest state on (re)subscribe, so consumers simply replace state.

export interface Topic {
  ch: "book" | "trades" | "candle" | "ticker" | "account";
  inst?: string;
  interval?: string;
  account?: string;
}

export interface Message {
  ch: string;
  inst?: string;
  interval?: string;
  account?: string;
  data: unknown;
}

type Listener = (message: Message) => void;
export type ConnectionState = "connecting" | "online" | "offline";

const topicKey = (t: Topic) => [t.ch, t.inst ?? "", t.interval ?? "", t.account ?? ""].join("|");

function messageKeys(m: Message): string[] {
  // An account topic fans out as account.positions, account.orders, ...
  if (m.ch.startsWith("account")) return [["account", "", "", m.account ?? ""].join("|")];
  const ch = m.ch.endsWith(".status") ? m.ch.slice(0, -7) : m.ch;
  return [[ch, m.inst ?? "", m.interval ?? "", m.account ?? ""].join("|")];
}

class Realtime {
  private socket: WebSocket | null = null;
  private topics = new Map<string, { topic: Topic; listeners: Set<Listener> }>();
  private stateListeners = new Set<(state: ConnectionState) => void>();
  private state: ConnectionState = "offline";
  private backoff = 500;
  private timer: ReturnType<typeof setTimeout> | null = null;
  private enabled = false;

  start() {
    if (this.enabled) return;
    this.enabled = true;
    this.connect();
  }

  stop() {
    this.enabled = false;
    if (this.timer) clearTimeout(this.timer);
    this.socket?.close();
    this.socket = null;
    this.setState("offline");
  }

  subscribe(topic: Topic, listener: Listener): () => void {
    const key = topicKey(topic);
    let entry = this.topics.get(key);
    if (!entry) {
      entry = { topic, listeners: new Set() };
      this.topics.set(key, entry);
      this.send({ op: "sub", ...topic });
    }
    entry.listeners.add(listener);
    return () => {
      const current = this.topics.get(key);
      if (!current) return;
      current.listeners.delete(listener);
      if (current.listeners.size === 0) {
        this.topics.delete(key);
        this.send({ op: "unsub", ...topic });
      }
    };
  }

  onState(listener: (state: ConnectionState) => void): () => void {
    this.stateListeners.add(listener);
    listener(this.state);
    return () => this.stateListeners.delete(listener);
  }

  private setState(state: ConnectionState) {
    this.state = state;
    for (const listener of this.stateListeners) listener(state);
  }

  private send(payload: object) {
    if (this.socket?.readyState === WebSocket.OPEN) this.socket.send(JSON.stringify(payload));
  }

  private connect() {
    if (!this.enabled) return;
    this.setState("connecting");
    const url = `${location.protocol === "https:" ? "wss" : "ws"}://${location.host}/api/ws`;
    const socket = new WebSocket(url);
    this.socket = socket;
    socket.onopen = () => {
      this.backoff = 500;
      this.setState("online");
      for (const { topic } of this.topics.values()) this.send({ op: "sub", ...topic });
    };
    socket.onmessage = (event) => {
      let message: Message;
      try {
        message = JSON.parse(event.data as string);
      } catch {
        return;
      }
      if (message.ch === "error") {
        console.warn("realtime:", message.data);
        return;
      }
      for (const key of messageKeys(message)) {
        const entry = this.topics.get(key);
        if (entry) for (const listener of entry.listeners) listener(message);
      }
    };
    socket.onclose = () => {
      if (this.socket !== socket) return;
      this.socket = null;
      this.setState("offline");
      if (!this.enabled) return;
      this.timer = setTimeout(() => this.connect(), this.backoff);
      this.backoff = Math.min(this.backoff * 2, 15_000);
    };
  }
}

export const realtime = new Realtime();
