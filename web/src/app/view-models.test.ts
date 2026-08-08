import { describe, expect, it } from "vitest";

import {
  activityDisplay,
  canSubmitPreview,
  environmentLabel,
  flattenRuntimeCapabilities,
  formatDecimal,
  mergePositionUpdate,
  runtimeDisplay,
  stateTone,
} from "./view-models.ts";

describe("authoritative view models", () => {
  it("collapses venue demo and testnet names into the product Test mode", () => {
    expect(environmentLabel("test", "demo")).toBe("Test");
    expect(environmentLabel("test", "testnet")).toBe("Test");
    expect(environmentLabel("live", "live")).toBe("Live");
  });

  it("leaves missing economic values unknown instead of reconstructing them", () => {
    expect(formatDecimal(undefined, "USDT")).toBe("-");
    expect(formatDecimal(null, "USDT")).toBe("-");
    expect(formatDecimal("12.3400", "USDT")).toBe("12.3400 USDT");
  });

  it("preserves authoritative economics across sparse position updates", () => {
    const current = {
      lineageId: "lineage-1",
      quantity: "1",
      realizedPnl: "2.5",
      unrealizedPnl: "1.25",
      markPrice: "101",
    };
    expect(mergePositionUpdate(current, { lineageId: "lineage-1", quantity: "0.5" })).toEqual({
      ...current,
      quantity: "0.5",
    });
  });

  it("submits only an unblocked server-approved preview", () => {
    expect(canSubmitPreview({ canSubmit: true, blockers: [] })).toBe(true);
    expect(canSubmitPreview({ canSubmit: true, blockers: ["ACCOUNT_NOT_READY"] })).toBe(false);
    expect(canSubmitPreview({ canSubmit: false, blockers: [] })).toBe(false);
  });

  it("distinguishes uncertain and manual intervention from ordinary failures", () => {
    expect(stateTone("uncertain")).toBe("warning");
    expect(stateTone("manual_intervention")).toBe("danger");
    expect(stateTone("filled")).toBe("success");
  });

  it("flattens capabilities from authoritative account runtime projections", () => {
    expect(flattenRuntimeCapabilities({
      "account-1": [{
        accountId: "account-1",
        readiness: { ready: true },
        writeReadiness: { ready: true },
        blockedReason: null,
        capabilities: [{
          rowId: "cap-1",
          venue: "binance",
          product: "perp",
          settlement: "USDT",
          writeReady: true,
          operations: { place: true, amend: false, cancel: true },
        }],
      }],
    })).toEqual([
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "place", supported: true }),
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "amend", supported: false }),
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "cancel", supported: true }),
    ]);
  });

  it("derives runtime state and reason from nested readiness projections", () => {
    expect(runtimeDisplay({
      readiness: { ready: true },
      writeReadiness: { ready: false, reason: "credentials require rotation" },
      blockedReason: null,
    })).toEqual({ state: "read_only", reason: "credentials require rotation" });
    expect(runtimeDisplay({
      readiness: { ready: true },
      writeReadiness: { ready: true },
      blockedReason: "account disabled",
    })).toEqual({ state: "blocked", reason: "account disabled" });
  });

  it("derives activity display fields only from the authoritative payload", () => {
    expect(activityDisplay({
      eventId: "event-1",
      sequence: 1,
      cursor: "cursor-1",
      kind: "command.finalized",
      productId: "funding",
      accountId: "account-1",
      resourceId: "command-1",
      payload: { summary: "Funding command finalized", status: "filled" },
      observedAt: "2026-08-08T10:00:00Z",
    })).toEqual({ summary: "Funding command finalized", status: "filled" });
    expect(activityDisplay({
      eventId: "event-2",
      sequence: 2,
      cursor: "cursor-2",
      kind: "runtime.observed",
      productId: null,
      accountId: null,
      resourceId: null,
      payload: {},
      observedAt: "2026-08-08T10:01:00Z",
    })).toEqual({ summary: "runtime.observed", status: "recorded" });
  });
});
