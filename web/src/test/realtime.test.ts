import { beforeEach, describe, expect, it, vi } from "vitest";

import { AuthGenerationGuard } from "../state/authGeneration";
import {
  RealtimeController,
  connectRealtime,
  type RealtimeMessage,
} from "../state/realtime";

type SourceEvent = { data?: string; error?: unknown };
type SourceListener = (event: SourceEvent) => void;

class FakeEventSource {
  static instances: FakeEventSource[] = [];

  readonly listeners = new Map<string, SourceListener[]>();
  readonly close = vi.fn();
  onopen: SourceListener | null = null;
  onerror: SourceListener | null = null;
  onmessage: SourceListener | null = null;

  constructor(
    readonly url: string,
    readonly options: { withCredentials?: boolean },
  ) {
    FakeEventSource.instances.push(this);
  }

  addEventListener(type: string, listener: SourceListener): void {
    const listeners = this.listeners.get(type) ?? [];
    listeners.push(listener);
    this.listeners.set(type, listeners);
  }

  emit(type: string, data: unknown): void {
    const event = { data: typeof data === "string" ? data : JSON.stringify(data) };
    for (const listener of this.listeners.get(type) ?? []) listener(event);
    if (type === "message") this.onmessage?.(event);
  }
}

function event(sequence: number, cursor = `opaque-${sequence}`) {
  return {
    eventId: `event-${sequence}`,
    sequence,
    cursor,
    observedAt: `2026-08-08T10:00:0${sequence}Z`,
    payload: { commandId: `command-${sequence}` },
  };
}

beforeEach(() => {
  FakeEventSource.instances = [];
});

describe("connectRealtime", () => {
  it("exports the shared realtime connection contract", async () => {
    const module = await import("../state/realtime").catch(() => null);

    expect(module?.connectRealtime).toBeTypeOf("function");
    expect(module?.RealtimeController).toBeTypeOf("function");
  });

  it("connects immediately to the same-origin stream with credentials", () => {
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
    });

    expect(controller).toBeInstanceOf(RealtimeController);
    expect(FakeEventSource.instances).toHaveLength(1);
    expect(FakeEventSource.instances[0].url).toBe("/api/v1/realtime/stream");
    expect(FakeEventSource.instances[0].options).toEqual({ withCredentials: true });
  });

  it("advances an opaque cursor only after a valid event is observed", () => {
    const messages: RealtimeMessage[] = [];
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
      onEvent: (message) => messages.push(message),
    });

    FakeEventSource.instances[0].emit("command.accepted", event(1, "opaque/+=cursor"));

    expect(controller.getState()).toMatchObject({
      cursor: "opaque/+=cursor",
      lastSequence: 1,
      lastEventAt: "2026-08-08T10:00:01Z",
      requiresResnapshot: false,
    });
    expect(messages.at(-1)).toMatchObject({
      type: "event",
      event: "command.accepted",
      data: { commandId: "command-1" },
      envelope: event(1, "opaque/+=cursor"),
    });
  });

  it("does not advance state for malformed events missing required metadata", () => {
    const messages: RealtimeMessage[] = [];
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
      onEvent: (message) => messages.push(message),
    });

    FakeEventSource.instances[0].emit("command.accepted", {
      sequence: 1,
      cursor: "opaque-1",
      observedAt: "2026-08-08T10:00:01Z",
    });

    expect(controller.getState()).toMatchObject({ cursor: "", lastSequence: 0 });
    expect(messages.at(-1)).toMatchObject({ type: "malformed", event: "command.accepted" });
  });

  it("reconnects with the cursor and bounded exponential backoff", () => {
    const timers: Array<{ callback: () => void; delay: number }> = [];
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
      backoff: { initialMs: 100, maxMs: 250, factor: 2 },
      setTimeoutImpl: (callback, delay) => {
        timers.push({ callback, delay });
        return timers.length;
      },
      clearTimeoutImpl: vi.fn(),
    });
    FakeEventSource.instances[0].emit("command.accepted", event(1));

    FakeEventSource.instances[0].onerror?.({ error: new Error("offline") });
    expect(timers[0].delay).toBe(100);
    timers[0].callback();
    expect(FakeEventSource.instances[1].url).toBe(
      "/api/v1/realtime/stream?cursor=opaque-1",
    );

    FakeEventSource.instances[1].onerror?.({ error: new Error("offline") });
    expect(timers[1].delay).toBe(200);
    timers[1].callback();
    FakeEventSource.instances[2].onerror?.({ error: new Error("offline") });
    expect(timers[2].delay).toBe(250);
    expect(controller.getState().reconnectAttempt).toBe(3);
  });

  it("clears the cursor and pauses on a resnapshot-required event", () => {
    const messages: RealtimeMessage[] = [];
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
      onEvent: (message) => messages.push(message),
    });
    const source = FakeEventSource.instances[0];
    source.emit("command.accepted", event(1));

    source.emit("realtime.resnapshot_required", event(2));

    expect(source.close).toHaveBeenCalledOnce();
    expect(controller.getState()).toMatchObject({
      status: "resnapshot-required",
      cursor: "",
      requiresResnapshot: true,
    });
    expect(messages.at(-1)).toMatchObject({ type: "resnapshot-required" });

    controller.resumeAfterSnapshot();
    expect(FakeEventSource.instances).toHaveLength(2);
    expect(FakeEventSource.instances[1].url).toBe("/api/v1/realtime/stream");
  });

  it("fails closed when the observed sequence is not contiguous", () => {
    const controller = connectRealtime({
      EventSourceImpl: FakeEventSource,
      authGuard: new AuthGenerationGuard(),
    });
    const source = FakeEventSource.instances[0];
    source.emit("command.accepted", event(2));

    source.emit("command.finalized", event(4));

    expect(controller.getState()).toMatchObject({
      status: "resnapshot-required",
      cursor: "",
      lastSequence: 2,
      requiresResnapshot: true,
    });
  });

  it("clears cursor runtime state and closes at an auth boundary", () => {
    const guard = new AuthGenerationGuard();
    const controller = connectRealtime({ EventSourceImpl: FakeEventSource, authGuard: guard });
    const source = FakeEventSource.instances[0];
    source.emit("command.accepted", event(1));

    guard.advance("logout");

    expect(source.close).toHaveBeenCalledOnce();
    expect(controller.getState()).toMatchObject({
      status: "auth-boundary",
      cursor: "",
      lastSequence: 0,
      requiresResnapshot: false,
    });
  });

  it("detaches auth callbacks after a terminal close", () => {
    const guard = new AuthGenerationGuard();
    const controller = connectRealtime({ EventSourceImpl: FakeEventSource, authGuard: guard });

    controller.close();
    guard.advance("later-login");

    expect(controller.getState().status).toBe("disconnected");
  });
});
