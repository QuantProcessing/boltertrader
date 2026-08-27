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

  it("keeps a principal account subscription degraded until that account recovers", () => {
    const manager = new RealtimeTopicManager("/api/v1/account/realtime/ws");
    const states: RealtimeConnectionState[] = [];
    const stopState = manager.watchState((state) => states.push(state));
    const unsubscribe = manager.subscribe({ topic: "account.events" }, () => undefined);
    const socket = currentSocket();
    socket.open();
    const subscribe = sentMessage(socket, 0) as { requestId: string };
    socket.receive({ type: "ack", requestId: subscribe.requestId, subscriptionId: "account-events", topic: "account.events" });

    socket.receive({
      type: "resync_required", subscriptionId: "account-events", topic: "account.events",
      code: "UPSTREAM_GAP", data: { accountId: "acct-a" },
    });
    expect(states.at(-1)).toBe("degraded");

    socket.receive({
      type: "delta", subscriptionId: "account-events", topic: "account.events",
      data: { kind: "account.account_health", accountId: "acct-b" },
    });
    expect(states.at(-1)).toBe("degraded");

    socket.receive({
      type: "delta", subscriptionId: "account-events", topic: "account.events",
      data: { kind: "account.account_health", accountId: "acct-a" },
    });
    expect(states.at(-1)).toBe("online");

    unsubscribe();
    stopState();
  });

  it("closes a browser socket that stops answering application pings", () => {
    vi.useFakeTimers();
    const manager = new RealtimeTopicManager("/api/v1/market/realtime/ws");
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
