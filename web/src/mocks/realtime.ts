import { paths } from "@/api/endpoints";
import type { CandleInterval } from "@/api/types";

import { instrumentOf } from "./fixtures";
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

let nextSocketId = 0;
let nextSubscriptionId = 0;

/**
 * Browser-only implementation of the one multiplexed realtime socket.
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

  constructor(url: string | URL) {
    super();
    this.url = String(url);
    window.setTimeout(() => {
      if (this.readyState !== MockRealtimeSocket.CONNECTING) return;
      this.readyState = MockRealtimeSocket.OPEN;
      const event = new Event("open");
      this.dispatchEvent(event);
      this.onopen?.(event);
    }, 0);
  }

  send(raw: string | ArrayBufferLike | Blob | ArrayBufferView): void {
    if (this.readyState !== MockRealtimeSocket.OPEN || typeof raw !== "string") return;
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
        const subscriptionId = (topic as SubscribeTopic & { subscriptionId?: string }).subscriptionId;
        if (subscriptionId) this.#subscriptions.delete(subscriptionId);
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
    const event = new CloseEvent("close", { code, reason, wasClean: code === 1000 });
    this.dispatchEvent(event);
    this.onclose?.(event);
  }

  async #snapshot(subscriptionId: string, topic: SubscribeTopic): Promise<void> {
    try {
      if (topic.topic === "account.events") {
        this.#data(subscriptionId, topic.topic, "checkpoint", { cursor: "mock-account-1" });
        return;
      }
      const instrument = topic.instrumentId ? instrumentOf(topic.instrumentId) : undefined;
      if (topic.topic.startsWith("market.") && !instrument) throw new Error("unknown mock instrument");

      if (topic.topic === "market.order_book") {
        const fact = await fetchMarketFact(instrument!);
        this.#data(subscriptionId, topic.topic, "snapshot", { bids: fact.bids, asks: fact.asks }, fact.observedAt);
        return;
      }
      if (topic.topic === "market.bbo") {
        const fact = await fetchMarketFact(instrument!);
        this.#data(subscriptionId, topic.topic, "snapshot", {
          bestBid: fact.bestBid,
          bestBidSize: fact.bestBidSize,
          bestAsk: fact.bestAsk,
          bestAskSize: fact.bestAskSize,
        }, fact.observedAt);
        return;
      }
      if (topic.topic === "market.candles") {
        const candles = await fetchCandles(instrument!, topic.interval ?? "15m", 200);
        this.#data(subscriptionId, topic.topic, "snapshot", { candles });
        return;
      }
      if (topic.topic === "market.mark_price") {
        const fact = await fetchMarketFact(instrument!);
        const price = fact.markPrice || midpoint(fact.bestBid, fact.bestAsk);
        if (!price) throw new Error("mark price is unavailable");
        this.#data(subscriptionId, topic.topic, "snapshot", { price }, fact.observedAt);
        return;
      }
      if (topic.topic === "funding.board") {
        const board = await fetchFundingBoard();
        this.#data(subscriptionId, topic.topic, "snapshot", board, board.observedAt);
        return;
      }
      if (topic.topic === "arbitrage.opportunities") {
        const response = await fetch(paths.arbitrageOpportunities);
        if (!response.ok) throw new Error(`opportunities returned ${response.status}`);
        const body = await response.json() as { data: unknown };
        this.#data(subscriptionId, topic.topic, "snapshot", body.data);
        return;
      }
      if (topic.topic === "system.venue_connectivity") {
        const response = await fetch(paths.venueConnectivity);
        if (!response.ok) throw new Error(`connectivity returned ${response.status}`);
        const body = await response.json() as { data: unknown };
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
    }
  }

  #data(
    subscriptionId: string,
    topic: string,
    type: "snapshot" | "checkpoint",
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
      const event = new MessageEvent<string>("message", { data: JSON.stringify(message) });
      this.dispatchEvent(event);
      this.onmessage?.(event);
    }, 0);
  }
}

function midpoint(bid: string, ask: string): string | undefined {
  const left = Number(bid);
  const right = Number(ask);
  return Number.isFinite(left) && Number.isFinite(right) && left > 0 && right > 0
    ? String((left + right) / 2)
    : undefined;
}

let installed = false;

export function installMockRealtime(): void {
  if (installed) return;
  installed = true;
  window.WebSocket = MockRealtimeSocket as unknown as typeof WebSocket;
}
