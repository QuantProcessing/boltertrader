import { describe, expect, it, vi } from "vitest";

import {
  AuthGenerationGuard,
  StaleAuthGenerationError,
} from "../state/authGeneration";

describe("AuthGenerationGuard", () => {
  it("exposes an auth generation guard", async () => {
    const module = await import("../state/authGeneration").catch(() => null);

    expect(module?.AuthGenerationGuard).toBeTypeOf("function");
  });

  it("aborts captured work and advances exactly once at an auth boundary", () => {
    const guard = new AuthGenerationGuard();
    const before = guard.capture();

    const nextGeneration = guard.advance("logout");

    expect(nextGeneration).toBe(before.generation + 1);
    expect(before.signal.aborted).toBe(true);
    expect(guard.capture().signal.aborted).toBe(false);
  });

  it("rejects async results that ignored abort after logout and relogin", async () => {
    const guard = new AuthGenerationGuard();
    const before = guard.capture();

    guard.advance("logout");
    guard.advance("login");

    expect(() => before.assertCurrent()).toThrow(StaleAuthGenerationError);
    await expect(
      Promise.resolve("stale").then((value) => {
        before.assertCurrent();
        return value;
      }),
    ).rejects.toMatchObject({
      name: "StaleAuthGenerationError",
      capturedGeneration: before.generation,
      currentGeneration: before.generation + 2,
    });
  });

  it("notifies registered runtime reset hooks and supports unsubscribe", () => {
    const guard = new AuthGenerationGuard();
    const reset = vi.fn();
    const unsubscribe = guard.onBoundary(reset);

    guard.advance("session-expired");
    unsubscribe();
    guard.advance("login");

    expect(reset).toHaveBeenCalledOnce();
    expect(reset).toHaveBeenCalledWith({
      generation: 1,
      reason: "session-expired",
    });
  });
});
