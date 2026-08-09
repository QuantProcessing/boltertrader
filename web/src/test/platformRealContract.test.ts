import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";
// @ts-expect-error -- Playwright's installed runtime bundles pngjs without public declarations.
import { PNG } from "playwright-core/lib/utilsBundle";

import { assertCanaryPixelIsolation, assertNoAuthoritativeAccountWrites, installEventSourceCapture } from "../../e2e/real-acceptance-helpers";

const specPath = resolve(process.cwd(), "e2e/platform-real.spec.ts");
const helperPath = resolve(process.cwd(), "e2e/real-acceptance-helpers.ts");

async function acceptanceSource(): Promise<string> {
  const [spec, helpers] = await Promise.all([
    readFile(specPath, "utf8"),
    readFile(helperPath, "utf8").catch(() => ""),
  ]);
  return `${spec}\n${helpers}`;
}

describe("real browser acceptance contract", () => {
  it("never submits an authoritative account mutation", async () => {
    const source = await acceptanceSource();

    expect(source).not.toMatch(/name:\s*"(?:Add account|Rotate |Drain |Disable |Remove |Confirm removal)"[^\n]*\.click\(\)/);
    expect(source).toContain('page.on("request"');
    expect(source).toContain("assertNoAuthoritativeAccountWrites");
  });

  it("allows login and non-writing validation but rejects account mutation requests", () => {
    expect(() => assertNoAuthoritativeAccountWrites([
      { method: "POST", path: "/api/v1/auth/login" },
      { method: "POST", path: "/api/v1/accounts/account-1/validate" },
      { method: "GET", path: "/api/v1/accounts" },
    ])).not.toThrow();
    expect(() => assertNoAuthoritativeAccountWrites([
      { method: "POST", path: "/api/v1/accounts" },
    ])).toThrow("authoritative private or account mutation");
  });

  it("records listener and generic onmessage delivery without replacing handlers", () => {
    class FakeEventSource {
      private readonly listeners = new Map<string, EventListener[]>();
      private messageHandler: ((event: MessageEvent) => unknown) | null = null;

      get onmessage() { return this.messageHandler; }
      set onmessage(handler: ((event: MessageEvent) => unknown) | null) { this.messageHandler = handler; }
      addEventListener(type: string, listener: EventListenerOrEventListenerObject) {
        const callable = typeof listener === "function" ? listener : listener.handleEvent.bind(listener);
        this.listeners.set(type, [...(this.listeners.get(type) ?? []), callable]);
      }
      emitListener(type: string, data: string) {
        const event = new MessageEvent(type, { data });
        for (const listener of this.listeners.get(type) ?? []) listener(event);
      }
      emitMessage(data: string) { this.messageHandler?.(new MessageEvent("message", { data })); }
    }
    Object.defineProperty(window, "EventSource", { configurable: true, value: FakeEventSource });
    Object.defineProperty(globalThis, "EventSource", { configurable: true, value: FakeEventSource });
    installEventSourceCapture();
    const source = new FakeEventSource();
    let listenerCalls = 0;
    let messageCalls = 0;
    source.addEventListener("custom", () => { listenerCalls += 1; });
    source.onmessage = () => { messageCalls += 1; };
    source.emitListener("custom", "listener-probe");
    source.emitMessage("message-probe");

    const captures = (window as typeof window & { __btRealtimePayloads?: Array<{ path: string; data: string }> }).__btRealtimePayloads;
    expect(captures).toEqual([
      { path: "listener", data: "listener-probe" },
      { path: "onmessage", data: "message-probe" },
    ]);
    expect(listenerCalls).toBe(1);
    expect(messageCalls).toBe(1);
  });

  it("accepts decoded changes only inside controlled rectangles and requires full restoration", () => {
    const image = (change?: { x: number; y: number }) => {
      const png = new PNG({ width: 4, height: 4 });
      for (let offset = 0; offset < png.data.length; offset += 4) png.data.set([20, 20, 20, 255], offset);
      png.data.set([80, 80, 80, 255], 0);
      if (change) png.data.set([240, 240, 240, 255], (change.y * 4 + change.x) * 4);
      return PNG.sync.write(png);
    };
    const before = image();
    const inside = image({ x: 1, y: 1 });
    const outside = image({ x: 3, y: 3 });
    const allowed = [{ x: 1, y: 1, width: 1, height: 1 }];

    expect(() => assertCanaryPixelIsolation(before, inside, before, allowed)).not.toThrow();
    expect(() => assertCanaryPixelIsolation(before, outside, before, allowed)).toThrow("outside controlled inputs");
    expect(() => assertCanaryPixelIsolation(before, inside, inside, allowed)).toThrow("did not restore");
  });

  it("captures both EventSource listener and generic onmessage payloads", async () => {
    const source = await acceptanceSource();

    expect(source).toContain("installEventSourceCapture");
    expect(source).toContain('path: "listener"');
    expect(source).toContain('record("listener"');
    expect(source).toContain('record("onmessage"');
  });

  it("decodes stable full-page before, with, and after screenshots", async () => {
    const source = await acceptanceSource();

    expect(source.match(/fullPage:\s*true/g)?.length ?? 0).toBeGreaterThanOrEqual(3);
    expect(source).toContain("assertCanaryPixelIsolation");
    expect(source).toContain("PNG.sync.read");
  });
});
