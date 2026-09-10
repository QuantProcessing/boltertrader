import { afterEach, beforeEach, describe, expect, it, vi } from "vitest";

import { RealtimeTopicManager, type RealtimeConnectionState } from "./topic-manager";

class FakeWebSocket extends EventTarget {
  static readonly CONNECTING = 0;
  static readonly OPEN = 1;
  static readonly CLOSING = 2;
  static readonly CLOSED = 3;
  static instances: FakeWebSocket[] = [];

  readonly sent: string[] = [];
  readyState = FakeWebSocket.CONNECTING;
  closeCount = 0;

  constructor(readonly url: string) {
    super();
    FakeWebSocket.instances.push(this);
  }

  open(): void {
    this.readyState = FakeWebSocket.OPEN;
    this.dispatchEvent(new Event("open"));
  }

  receive(message: unknown): void {
    this.dispatchEvent(new MessageEvent("message", { data: JSON.stringify(message) }));
  }

  send(data: string): void {
    this.sent.push(data);
  }

  close(): void {
    if (this.readyState === FakeWebSocket.CLOSED) return;
    this.closeCount++;
    this.readyState = FakeWebSocket.CLOSED;
    this.dispatchEvent(new Event("close"));
  }
}

function currentSocket(): FakeWebSocket {
  const socket = FakeWebSocket.instances.at(-1);
  if (!socket) throw new Error("expected a WebSocket instance");
  return socket;
}

function sentMessage(socket: FakeWebSocket, index: number): unknown {
  const value = socket.sent[index];
  if (!value) throw new Error(`expected WebSocket message ${index}`);
  return JSON.parse(value) as unknown;
}

describe("RealtimeTopicManager", () => {
  const originalWebSocket = globalThis.WebSocket;

  beforeEach(() => {
    FakeWebSocket.instances = [];
    Object.defineProperty(globalThis, "WebSocket", {
      configurable: true,
      value: FakeWebSocket as unknown as typeof WebSocket,
    });
  });

  afterEach(() => {
    vi.useRealTimers();
    Object.defineProperty(globalThis, "WebSocket", {
      configurable: true,
      value: originalWebSocket,
    });
  });

  it("shares one connection across market, private events and persisted orders", () => {
    const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const stops = [manager.subscribe({ topic: "market.index_price", instrumentId: "btc" }, () => {}), manager.subscribe({ topic: "account.events" }, () => {}), manager.subscribe({ topic: "trading.orders" }, () => {})];
    expect(FakeWebSocket.instances).toHaveLength(1); currentSocket().open();
    expect(currentSocket().sent).toHaveLength(3); stops.forEach(stop => stop());
    expect(currentSocket().closeCount).toBe(1);
  });

  it("cancels a late subscribe acknowledgement without looping on cancel acknowledgement", () => {
    const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const keep = manager.subscribe({ topic: "funding.board" }, () => {});
    const release = manager.subscribe({ topic: "account.events" }, () => {});
    const socket = currentSocket(); socket.open();
    const request = sentMessage(socket, 1) as { requestId: string };
    release(); socket.receive({ type: "ack", requestId: request.requestId, subscriptionId: "late" });
    const cancel = sentMessage(socket, 2) as { op: string; requestId: string; topics: {subscriptionId: string}[] };
    expect(cancel.op).toBe("unsubscribe"); expect(cancel.topics[0]?.subscriptionId).toBe("late");
    socket.receive({ type: "ack", requestId: cancel.requestId, subscriptionId: "late" });
    expect(socket.sent).toHaveLength(3); keep();
  });

  it("replays a complete book rather than an isolated delta to a later consumer", () => {
    const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const request = { topic: "market.order_book" as const, instrumentId: "btc", depth: 100 };
    const stop = manager.subscribe(request, () => {}); const socket = currentSocket(); socket.open();
    socket.receive({ type: "ack", requestId: (sentMessage(socket, 0) as {requestId:string}).requestId, subscriptionId: "book" });
    socket.receive({ type: "snapshot", subscriptionId: "book", topic: request.topic, generation: 1, sequence: 1, data: { bids: [{price:"99",quantity:"2"}], asks: [{price:"101",quantity:"3"}] } });
    socket.receive({ type: "delta", subscriptionId: "book", topic: request.topic, generation: 1, sequence: 2, data: { bids: [{price:"99",quantity:"0"},{price:"98",quantity:"4"}], asks: [] } });
    const listener = vi.fn(); const stop2 = manager.subscribe(request, listener);
    expect(listener).toHaveBeenCalledWith(expect.objectContaining({ type:"snapshot", data:{bids:[{price:"98",quantity:"4"}],asks:[{price:"101",quantity:"3"}]} }));
    expect(socket.sent).toHaveLength(1); stop2(); stop();
  });

  it("does not turn an aged reference replay into a fresh quote", () => {
    vi.useFakeTimers(); const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const request = { topic: "market.index_price" as const, instrumentId:"btc" };
    const stop = manager.subscribe(request, () => {}); const socket=currentSocket(); socket.open();
    socket.receive({type:"ack",requestId:(sentMessage(socket,0) as {requestId:string}).requestId,subscriptionId:"index"});
    socket.receive({type:"snapshot",subscriptionId:"index",topic:request.topic,data:{price:"100",ageMs:1000,staleAfterMs:5000}});
    vi.advanceTimersByTime(5000);const listener=vi.fn();const stop2=manager.subscribe(request,listener);
    expect(listener.mock.calls[0]?.[0].data.ageMs).toBe(6000);stop2();stop();
  });

  it("drops cached private snapshots at a session boundary", () => {
    vi.useFakeTimers();const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const stop=manager.subscribe({topic:"trading.orders"},()=>{});const socket=currentSocket();socket.open();
    socket.receive({type:"ack",requestId:(sentMessage(socket,0) as {requestId:string}).requestId,subscriptionId:"orders"});
    socket.receive({type:"snapshot",subscriptionId:"orders",topic:"trading.orders",data:{orders:[{commandId:"private"}]}});
    manager.resetSession();const listener=vi.fn();const stop2=manager.subscribe({topic:"trading.orders"},listener);
    expect(listener).not.toHaveBeenCalled();expect(socket.closeCount).toBe(1);stop2();stop();vi.runOnlyPendingTimers();
  });

	it("tracks exchange account connections independently from the browser socket", () => {
    const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const states: RealtimeConnectionState[] = [];
    const stopState = manager.watchState((state) => states.push(state));
    const unsubscribe = manager.subscribe({ topic: "account.events" }, () => undefined);
    const socket = currentSocket();
    socket.open();
    const subscribe = sentMessage(socket, 0) as { requestId: string };
    socket.receive({ type: "ack", requestId: subscribe.requestId, subscriptionId: "account-events", topic: "account.events" });

    socket.receive({
	  type: "delta", subscriptionId: "account-events", topic: "account.events",
	  data: { kind: "account.account_connection", accountId: "acct-a", payload: { route_id: "perp-usdt", state: "disconnected" } },
    });
    expect(states.at(-1)).toBe("degraded");

    socket.receive({
      type: "delta", subscriptionId: "account-events", topic: "account.events",
	  data: { kind: "account.account_connection", accountId: "acct-b", payload: { route_id: "perp-usdt", state: "connected" } },
    });
    expect(states.at(-1)).toBe("degraded");

    socket.receive({
      type: "delta", subscriptionId: "account-events", topic: "account.events",
	  data: { kind: "account.account_connection", accountId: "acct-a", payload: { route_id: "perp-usdt", state: "connected" } },
    });
    expect(states.at(-1)).toBe("online");

    unsubscribe();
    stopState();
  });

  it("closes a browser socket that stops answering application pings", () => {
    vi.useFakeTimers();
    const manager = new RealtimeTopicManager("/api/v1/realtime/ws");
    const states: RealtimeConnectionState[] = [];
    const stopState = manager.watchState((state) => states.push(state));
    const unsubscribe = manager.subscribe({ topic: "funding.board" }, () => undefined);
    const socket = currentSocket();
    socket.open();

    vi.advanceTimersByTime(20_000);
    expect(JSON.parse(socket.sent.at(-1) ?? "{}")).toMatchObject({ op: "ping" });
    vi.advanceTimersByTime(10_000);

    expect(socket.closeCount).toBe(1);
    expect(states.at(-1)).toBe("reconnecting");

    unsubscribe();
    stopState();
  });
});
