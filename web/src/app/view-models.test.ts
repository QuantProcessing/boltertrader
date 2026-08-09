import { describe, expect, expectTypeOf, it } from "vitest";

import type { CredentialSchema, NativeEnvironment, ProductPolicy, RouteProfile, RuntimeSnapshotView, RuntimeView, Venue } from "./types.ts";

import {
  activityDisplay,
  canSubmitPreview,
  environmentLabel,
  flattenRuntimeCapabilities,
  formatDecimal,
  mergePositionUpdate,
  runtimeDisplay,
  safeFinding,
  stateTone,
} from "./view-models.ts";

const runtime = (overrides: Partial<RuntimeView> = {}): RuntimeView => ({
  accountId: "account-1",
  desiredStateRevision: 1,
  runtimeGeneration: 1,
  credentialVersion: 1,
  runtimeVersion: 1,
  readiness: { ready: true },
  writeReadiness: { ready: true },
  lastAppliedDesiredStateRevision: 1,
  lastAppliedRuntimeGeneration: 1,
  blockedReason: null,
  capabilities: [],
  retiring: [],
  observedAt: "2026-08-08T10:00:00Z",
  ...overrides,
});

describe("authoritative view models", () => {
  it("requires complete authoritative venue and runtime contracts", () => {
    expectTypeOf<Venue["nativeEnvironment"]>().toEqualTypeOf<NativeEnvironment>();
    expectTypeOf<Venue["environment"]>().toEqualTypeOf<NativeEnvironment>();
    expectTypeOf<Venue["products"]>().toEqualTypeOf<ProductPolicy[]>();
    expectTypeOf<Venue["credentialSchema"]>().toEqualTypeOf<CredentialSchema>();
    expectTypeOf<Venue["routeProfiles"]>().toEqualTypeOf<RouteProfile[]>();
    expectTypeOf<RuntimeView["desiredStateRevision"]>().toEqualTypeOf<number>();
    expectTypeOf<RuntimeView["retiring"]>().toEqualTypeOf<RuntimeSnapshotView[]>();
    expectTypeOf(runtimeDisplay).parameter(0).toEqualTypeOf<RuntimeView | undefined>();
  });

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
      "account-1": [runtime({
        capabilities: [{
          rowId: "cap-1",
          venue: "binance",
          product: "perp",
          settlement: "USDT",
          writeReady: true,
          operations: { place: true, amend: false, cancel: true },
        }],
      })],
    })).toEqual([
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "place", supported: true }),
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "amend", supported: false }),
      expect.objectContaining({ accountId: "account-1", rowId: "cap-1", operation: "cancel", supported: true }),
    ]);
  });

  it("derives runtime state and reason from nested readiness projections", () => {
    expect(runtimeDisplay(runtime({
      readiness: { ready: true },
      writeReadiness: { ready: false, reason: "credentials require rotation" },
      blockedReason: null,
    }))).toEqual({ state: "read_only", reason: "credentials require rotation" });
    expect(runtimeDisplay(runtime({
      readiness: { ready: true },
      writeReadiness: { ready: true },
      blockedReason: "account disabled",
    }))).toEqual({ state: "blocked", reason: "account disabled" });
  });

  it("derives runtime state from redacted v5 active and pending snapshots", () => {
    expect(runtimeDisplay(runtime({
      pending: {
        desiredStateRevision: 2,
        runtimeGeneration: 2,
        credentialVersion: 2,
        requestedState: "rotating",
        state: "blocked",
        reason: "validation failed",
        routes: [],
      },
    }))).toEqual({ state: "blocked", reason: "validation failed" });
    expect(runtimeDisplay(runtime({
      active: {
        desiredStateRevision: 1,
        runtimeGeneration: 1,
        credentialVersion: 1,
        requestedState: "active",
        routes: [{ routeId: "perp-usdt", product: "perp", routeProfileId: "profile-1", requestedState: "active", ready: true }],
      },
    }))).toEqual({ state: "ready" });
  });

  it("derives activity display fields only from the authoritative payload", () => {
    expect(activityDisplay({
      kind: "command.finalized",
      payload: { summary: "Funding command finalized", status: "filled" },
    })).toEqual({ summary: "Funding command finalized", status: "filled" });
    expect(activityDisplay({
      kind: "runtime.observed",
      payload: {},
    })).toEqual({ summary: "runtime.observed", status: "recorded" });
  });

  it("redacts secret references, provider paths, tokens, and credential material", () => {
    expect(safeFinding("PRIVATE_STREAM_GAP")).toBe("PRIVATE STREAM GAP");
    for (const value of [
      "secretRef=opaque-canary",
      "providerPath=/var/lib/credentials/account-a",
      "gatewayToken=bearer-canary",
      "apiSecret=credential-canary",
    ]) expect(safeFinding(value)).toBe("Sensitive diagnostic redacted.");
  });
});
