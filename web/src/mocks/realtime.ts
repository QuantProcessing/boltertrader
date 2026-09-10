import { paths } from "@/api/endpoints";
import type { CandleInterval } from "@/api/types";

import { orders, activity, session } from "./db";
import { LEDGER, instrumentOf } from "./fixtures";
import { fetchCandles, fetchFundingBoard, fetchMarketFact } from "./venues";

interface SubscribeTopic {
  topic: string;
  instrumentId?: string;
  depth?: number;
  interval?: CandleInterval;
}

interface ControlMessage {
  op: "subscribe" | "unsubscribe" | "ping";
  requestId?: string;
  topics?: SubscribeTopic[];
}

const sockets = new Set<MockRealtimeSocket>();
let nextSocketId = 0;
let nextSubscriptionId = 0;

/**
 * Browser-only implementation of the single multiplexed realtime connection.
 *
 * MSW deliberately owns HTTP only. This class mirrors the control protocol so
 * mock mode exercises the production topic manager while all public values are
 * still read from venue REST and all private values remain local fixtures.
 */
class MockRealtimeSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;

  readonly CONNECTING = MockRealtimeSocket.CONNECTING;
  readonly OPEN = MockRealtimeSocket.OPEN;
  readonly CLOSING = MockRealtimeSocket.CLOSING;
  readonly CLOSED = MockRealtimeSocket.CLOSED;
  readonly url: string;
  readonly protocol = "";
  readonly extensions = "";
  readonly binaryType = "blob";
  bufferedAmount = 0;
  readyState = MockRealtimeSocket.CONNECTING;
  onopen: ((event: Event) => void) | null = null;
  onmessage: ((event: MessageEvent<string>) => void) | null = null;
  onerror: ((event: Event) => void) | null = null;
  onclose: ((event: CloseEvent) => void) | null = null;

  #socketId = ++nextSocketId;
  #sequence = 0;
  #subscriptions = new Map<string, SubscribeTopic>();
  #timers = new Map<string, ReturnType<typeof setTimeout>>();
  #accountChanged = (event: Event) => {
    if (!session.open) return;
    for (const [id, topic] of this.#subscriptions)
      if (topic.topic === "account.events")
        this.#data(id, topic.topic, "delta", (event as CustomEvent).detail);
  };
  #privateChanged = () => {
    for (const [id, topic] of this.#subscriptions)
      if (topic.topic === "trading.orders") void this.#snapshot(id, topic);
  };

  constructor(url: string | URL) {
    super();
    this.url = String(url);
    sockets.add(this);
    window.addEventListener("bolter-mock-account", this.#accountChanged);
    window.addEventListener("bolter-mock-orders", this.#privateChanged);
    window.setTimeout(() => {
      if (this.readyState !== MockRealtimeSocket.CONNECTING) return;
      this.readyState = MockRealtimeSocket.OPEN;
      const event = new Event("open");
      this.dispatchEvent(event);
      this.onopen?.(event);
    }, 0);
  }

  diagnostics() {
    return {
      url: this.url,
      state: this.readyState,
      topics: [...this.#subscriptions.values()].map((topic) => ({ ...topic })),
    };
  }

  send(raw: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (this.readyState !== MockRealtimeSocket.OPEN || typeof raw !== "string")
      return;
    let control: ControlMessage;
    try {
      control = JSON.parse(raw) as ControlMessage;
    } catch {
      return;
    }
    if (control.op === "ping") {
      this.#emit({ type: "pong", requestId: control.requestId });
      return;
    }
    if (control.op === "unsubscribe") {
      for (const topic of control.topics ?? []) {
        const subscriptionId = (
          topic as SubscribeTopic & { subscriptionId?: string }
        ).subscriptionId;
        if (subscriptionId) {
          this.#subscriptions.delete(subscriptionId);
          clearTimeout(this.#timers.get(subscriptionId));
          this.#timers.delete(subscriptionId);
        }
      }
      return;
    }
    for (const topic of control.topics ?? []) {
      const subscriptionId = `mock-${this.#socketId}-${++nextSubscriptionId}`;
      this.#subscriptions.set(subscriptionId, topic);
      this.#emit({ type: "ack", requestId: control.requestId, subscriptionId });
      void this.#snapshot(subscriptionId, topic);
    }
  }

  close(code = 1000, reason = ""): void {
    if (this.readyState === MockRealtimeSocket.CLOSED) return;
    this.readyState = MockRealtimeSocket.CLOSED;
    this.#subscriptions.clear();
    sockets.delete(this);
    window.removeEventListener("bolter-mock-account", this.#accountChanged);
    this.#timers.forEach(clearTimeout);
    this.#timers.clear();
    window.removeEventListener("bolter-mock-orders", this.#privateChanged);
    const event = new CloseEvent("close", {
      code,
      reason,
      wasClean: code === 1000,
    });
    this.dispatchEvent(event);
    this.onclose?.(event);
  }

  async #snapshot(
    subscriptionId: string,
    topic: SubscribeTopic,
  ): Promise<void> {
    try {
      if (topic.topic === "trading.orders") {
        if (!session.open) throw new Error("sign in required");
        this.#data(subscriptionId, topic.topic, "snapshot", {
          orders: orders.list(),
          fills: orders.fills(),
          activity: activity.list(),
          ledger: LEDGER,
        });
        return;
      }
      if (topic.topic === "account.events") {
        return;
      }
      const instrument = topic.instrumentId
        ? instrumentOf(topic.instrumentId)
        : undefined;
      if (topic.topic.startsWith("market.") && !instrument)
        throw new Error("unknown mock instrument");

      if (topic.topic === "market.order_book") {
        const fact = await fetchMarketFact(instrument!);
        this.#data(
          subscriptionId,
          topic.topic,
          "snapshot",
          { bids: fact.bids, asks: fact.asks },
          fact.observedAt,
        );
        return;
      }
      if (topic.topic === "market.bbo") {
        const fact = await fetchMarketFact(instrument!);
        this.#data(
          subscriptionId,
          topic.topic,
          "snapshot",
          {
            bestBid: fact.bestBid,
            bestBidSize: fact.bestBidSize,
            bestAsk: fact.bestAsk,
            bestAskSize: fact.bestAskSize,
          },
          fact.observedAt,
        );
        return;
      }
      if (topic.topic === "market.candles") {
        const candles = await fetchCandles(
          instrument!,
          topic.interval ?? "15m",
          1,
        );
        this.#data(subscriptionId, topic.topic, "snapshot", { candles });
        return;
      }
      if (
        topic.topic === "market.mark_price" ||
        topic.topic === "market.index_price"
      ) {
        const fact = await fetchMarketFact(instrument!);
        const price =
          topic.topic === "market.index_price"
            ? fact.indexPrice
            : fact.markPrice;
        if (!price) throw new Error("mark price is unavailable");
        this.#data(subscriptionId, topic.topic, "snapshot", {
          price,
          fundingRate: fact.fundingRate,
          openInterest: fact.openInterest,
          ageMs: 0,
          staleAfterMs: 15_000,
        });
        return;
      }
      if (topic.topic === "funding.board") {
        const board = await fetchFundingBoard();
        this.#data(
          subscriptionId,
          topic.topic,
          "snapshot",
          board,
          board.observedAt,
        );
        return;
      }
      if (topic.topic === "system.venue_connectivity") {
        const response = await fetch(paths.venueConnectivity);
        if (!response.ok)
          throw new Error(`connectivity returned ${response.status}`);
        const body = (await response.json()) as { data: unknown };
        this.#data(subscriptionId, topic.topic, "snapshot", body.data);
      }
    } catch (error) {
      this.#emit({
        type: "error",
        subscriptionId,
        topic: topic.topic,
        code: "MOCK_PUBLIC_DATA_UNAVAILABLE",
        reason: error instanceof Error ? error.message : String(error),
      });
    } finally {
      if (
        this.#subscriptions.has(subscriptionId) &&
        topic.topic !== "account.events" &&
        topic.topic !== "trading.orders"
      ) {
        clearTimeout(this.#timers.get(subscriptionId));
        this.#timers.set(
          subscriptionId,
          setTimeout(
            () => void this.#snapshot(subscriptionId, topic),
            topic.topic === "funding.board" ? 30_000 : 5_000,
          ),
        );
      }
    }
  }

  #data(
    subscriptionId: string,
    topic: string,
    type: "snapshot" | "delta",
    data: unknown,
    observedAt = new Date().toISOString(),
  ): void {
    if (!this.#subscriptions.has(subscriptionId)) return;
    this.#emit({
      type,
      subscriptionId,
      topic,
      generation: 1,
      sequence: ++this.#sequence,
      observedAt,
      data,
    });
  }

  #emit(message: Record<string, unknown>): void {
    if (this.readyState !== MockRealtimeSocket.OPEN) return;
    window.setTimeout(() => {
      if (this.readyState !== MockRealtimeSocket.OPEN) return;
      const event = new MessageEvent<string>("message", {
        data: JSON.stringify(message),
      });
      this.dispatchEvent(event);
      this.onmessage?.(event);
    }, 0);
  }
}

let installed = false;

export function installMockRealtime(): void {
  if (installed) return;
  installed = true;
  window.WebSocket = MockRealtimeSocket as unknown as typeof WebSocket;
  Object.assign(window, {
    __BOLTER_MOCK_REALTIME__: {
      snapshot: () => [...sockets].map((socket) => socket.diagnostics()),
      disconnect: () =>
        [...sockets].forEach((socket) =>
          socket.close(1013, "preview_disconnect"),
        ),
    },
  });
}
