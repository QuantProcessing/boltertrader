import { describe, expect, it, vi } from "vitest";

import { AuthGenerationGuard, StaleAuthGenerationError } from "../state/authGeneration";
import {
  SnapshotLoadError,
  loadWorkspaceSnapshot,
} from "../state/workspaceSnapshot";

describe("loadWorkspaceSnapshot", () => {
  it("exports the shared snapshot loader", async () => {
    const module = await import("../state/workspaceSnapshot").catch(() => null);

    expect(module?.loadWorkspaceSnapshot).toBeTypeOf("function");
  });

  it("loads declarative named reads with one shared generation and signal", async () => {
    const guard = new AuthGenerationGuard();
    const status = vi.fn(async ({ generation }: { generation: number }) => ({
      generation,
      ready: true,
    }));
    const accounts = vi.fn(async ({ signal }: { signal: AbortSignal }) => ({
      aborted: signal.aborted,
      items: ["account-1"],
    }));

    const snapshot = await loadWorkspaceSnapshot({ status, accounts }, { authGuard: guard });

    expect(snapshot).toEqual({
      status: { generation: 0, ready: true },
      accounts: { aborted: false, items: ["account-1"] },
    });
    expect(status).toHaveBeenCalledOnce();
    expect(accounts).toHaveBeenCalledOnce();
  });

  it("substitutes an empty value only for an explicitly optional read", async () => {
    const snapshot = await loadWorkspaceSnapshot({
      status: async () => ({ ready: true }),
      activity: {
        read: async () => {
          throw new Error("activity unavailable");
        },
        optional: true,
        empty: () => [] as string[],
      },
    });

    expect(snapshot).toEqual({ status: { ready: true }, activity: [] });
  });

  it("fails closed with the read name and aborts sibling reads", async () => {
    let siblingSignal: AbortSignal | undefined;
    const promise = loadWorkspaceSnapshot({
      accounts: async () => {
        throw new Error("accounts unavailable");
      },
      positions: ({ signal }: { signal: AbortSignal }) => {
        siblingSignal = signal;
        return new Promise<never>((_resolve, reject) => {
          signal.addEventListener("abort", () => reject(signal.reason), { once: true });
        });
      },
    });

    await expect(promise).rejects.toMatchObject({
      name: "SnapshotLoadError",
      readName: "accounts",
    });
    expect(siblingSignal?.aborted).toBe(true);
    await expect(promise).rejects.toBeInstanceOf(SnapshotLoadError);
  });

  it("rejects a completed snapshot after an auth boundary", async () => {
    let resolveRead!: (value: { ready: boolean }) => void;
    const guard = new AuthGenerationGuard();
    const pending = loadWorkspaceSnapshot(
      {
        status: () => new Promise<{ ready: boolean }>((resolve) => {
          resolveRead = resolve;
        }),
      },
      { authGuard: guard },
    );

    await Promise.resolve();
    guard.advance("logout");
    resolveRead({ ready: true });

    await expect(pending).rejects.toBeInstanceOf(StaleAuthGenerationError);
  });
});
