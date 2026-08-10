import { readFile } from "node:fs/promises";
import { resolve } from "node:path";

import { describe, expect, it } from "vitest";
// @ts-expect-error -- Playwright's installed runtime bundles pngjs without public declarations.
import { PNG } from "playwright-core/lib/utilsBundle";

import { assertCanaryPixelIsolation, assertNoAuthoritativeAccountWrites, assertSafeRealPlatformURL, assertTestSessionMode, installEventSourceCapture } from "../../e2e/real-acceptance-helpers";

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
  it("accepts only explicit loopback HTTP targets without deceptive URL components", () => {
    expect(assertSafeRealPlatformURL("http://127.0.0.1:13001").origin).toBe("http://127.0.0.1:13001");
    expect(assertSafeRealPlatformURL("http://localhost:13001").origin).toBe("http://localhost:13001");
    expect(assertSafeRealPlatformURL("http://[::1]:13001").origin).toBe("http://[::1]:13001");
    for (const target of ["https://platform.example.com:443", "http://127.0.0.1", "ftp://127.0.0.1:13001", "http://platform.example.com@127.0.0.1:13001", "http://127.0.0.1:13001/redirect"]) {
      expect(() => assertSafeRealPlatformURL(target)).toThrow("loopback");
    }
  });

  it("requires an authoritative Test session before non-login posts", () => {
    expect(() => assertTestSessionMode({ principal: { mode: "test" } })).not.toThrow();
    expect(() => assertTestSessionMode({ principal: { mode: "live" } })).toThrow("exact Test mode");
    expect(() => assertTestSessionMode({ principal: {} })).toThrow("exact Test mode");
  });

  it("never submits an authoritative account mutation", async () => {
    const source = await acceptanceSource();

    expect(source).not.toMatch(/name:\s*"(?:Add account|Rotate |Drain |Disable |Remove |Confirm removal)"[^\n]*\.click\(\)/);
    expect(source).toContain('page.on("request"');
    expect(source).toContain("assertNoAuthoritativeAccountWrites");
  });

  it("allows only authentication-session writes and rejects account validation", () => {
    expect(() => assertNoAuthoritativeAccountWrites([
      { method: "POST", path: "/api/v1/auth/login" },
      { method: "DELETE", path: "/api/v1/auth/session" },
      { method: "GET", path: "/api/v1/accounts" },
    ])).not.toThrow();
    expect(() => assertNoAuthoritativeAccountWrites([
      { method: "POST", path: "/api/v1/accounts/account-1/validate" },
    ])).toThrow("authoritative private or account mutation");
  });

  it("visits every authenticated product and system surface without invoking account validation", async () => {
    const source = await acceptanceSource();

    for (const navigation of ["Overview", "Funding", "Manual", "Accounts", "Activity", "Settings"]) {
      expect(source).toContain(`name: "${navigation}"`);
    }
    for (const heading of ["Trading overview", "Funding Trading", "Manual Trading", "Accounts & Security", "Activity & Audit", "Settings"]) {
      expect(source).toContain(`name: "${heading}"`);
    }
    expect(source).not.toContain("Retry validation for");
    expect(source).not.toContain("/validate$");
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

  it("does not swallow response capture failures and expects named production SSE only", async () => {
    const source = await acceptanceSource();

    expect(source).not.toContain("catch(() => undefined)");
    expect(source).toContain('capture.path === "listener"');
    expect(source).not.toContain('capture.path === "onmessage"');
  });

  it("arms authoritative snapshot waits before invoking the resnapshot harness", async () => {
    const source = await acceptanceSource();
    const waits = source.indexOf("postHarnessResponses");
    const harness = source.indexOf("request.post(harnessURL");

    expect(waits).toBeGreaterThan(0);
    expect(harness).toBeGreaterThan(waits);
    expect(source).toContain('"/api/v1/accounts"');
    expect(source).toContain('"/api/v1/catalog/venues"');
    expect(source).toContain("triggerCount");
    expect(source).toContain("canonicalMutationCount");
    expect(source).toContain("activeHeadMutationCount");
    expect(source).toContain('"Idempotency-Key"');
  });

  it("requires one successful recovery and zero authoritative harness effects", async () => {
    const source = await acceptanceSource();

    expect(source).toContain("markers.triggerCount");
    expect(source).toContain("markers.canonicalMutationCount");
    expect(source).toContain("markers.activeHeadMutationCount");
    expect(source).toContain("resnapshot trigger must execute exactly once");
    expect(source).toContain("resnapshot harness must not mutate canonical state");
    expect(source).toContain("resnapshot harness must not promote Gateway heads");
  });

  it("decodes stable full-page before, with, and after screenshots", async () => {
    const source = await acceptanceSource();

    expect(source.match(/fullPage:\s*true/g)?.length ?? 0).toBeGreaterThanOrEqual(3);
    expect(source).toContain("assertCanaryPixelIsolation");
    expect(source).toContain("PNG.sync.read");
  });
});
