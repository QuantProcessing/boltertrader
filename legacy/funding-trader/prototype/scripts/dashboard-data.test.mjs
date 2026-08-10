import assert from "node:assert/strict";
import test from "node:test";

import {
  isLedgerInvalidationEvent,
  loadDashboardSnapshots,
} from "../src/dashboardData.js";

test("dashboard snapshot loader always includes ledger, closed positions, and verification", async () => {
  const calls = [];
  const api = fakeDashboardApi(calls, {
    getPortfolioLedger: async () => {
      const error = new Error("portfolio not projected yet");
      error.status = 404;
      error.code = "ledger_not_found";
      throw error;
    },
  });

  const snapshots = await loadDashboardSnapshots(api, {
    opportunitySort: "netApr_asc",
    venue: "venue-1",
    alertSeverity: "warning",
    reportingAsset: "USDT",
  });

  assert.deepEqual(calls.map((call) => call.name), [
    "getSystemStatus",
    "getReadiness",
    "listOpportunities",
    "listAlerts",
    "listPositions",
    "listActivities",
    "listAuditEvents",
    "getPortfolioLedger",
    "listClosedPositions",
    "getVerification",
  ]);
  assert.deepEqual(calls.find((call) => call.name === "listOpportunities").params, {
    sort: "netApr_asc",
    shortVenueId: "venue-1",
  });
  assert.deepEqual(calls.find((call) => call.name === "listClosedPositions").params, {
    reportingAsset: "USDT",
  });
  assert.equal(snapshots.portfolio, null);
  assert.deepEqual(snapshots.closedPositions, { data: [{ positionId: "closed-1" }] });
  assert.deepEqual(snapshots.verification, { data: { status: "incomplete" } });
});

test("dashboard snapshot loader fails closed on non-empty-state ledger errors", async () => {
  const calls = [];
  const expected = new Error("verification unavailable");
  expected.code = "ledger_unavailable";
  const api = fakeDashboardApi(calls, {
    getVerification: async () => {
      throw expected;
    },
  });

  await assert.rejects(() => loadDashboardSnapshots(api), (error) => error === expected);
});

test("dashboard snapshot loader forwards abort signal to every authoritative read", async () => {
  const calls = [];
  const signal = AbortSignal.timeout(10_000);
  const api = fakeDashboardApi(calls);

  await loadDashboardSnapshots(api, { signal });

  for (const call of calls) {
    assert.equal(call.options?.signal, signal, call.name);
  }
});

test("ledger SSE events invalidate authoritative snapshots instead of replacing DTOs", () => {
  for (const kind of [
    "ledger.funding_payment.append",
    "ledger.position.verification_changed",
    "ledger.portfolio.updated",
    "verification.status",
  ]) {
    assert.equal(isLedgerInvalidationEvent(kind), true, kind);
  }
  assert.equal(isLedgerInvalidationEvent("position.upsert"), false);
  assert.equal(isLedgerInvalidationEvent("gateway.raw.resync"), false);
});

function fakeDashboardApi(calls, overrides = {}) {
  const defaults = {
    getSystemStatus: async () => ({ data: { mode: "test" } }),
    getReadiness: async () => ({ data: { openReady: false, closeReady: true, recoveryReady: true } }),
    listOpportunities: async () => ({ data: [] }),
    listAlerts: async () => ({ data: [] }),
    listPositions: async () => ({ data: [] }),
    listActivities: async () => ({ data: [] }),
    listAuditEvents: async () => ({ data: [] }),
    getPortfolioLedger: async () => ({ data: { totalNetPnl: "1" } }),
    listClosedPositions: async () => ({ data: [{ positionId: "closed-1" }] }),
    getVerification: async () => ({ data: { status: "incomplete" } }),
    ...overrides,
  };
  return Object.fromEntries(Object.entries(defaults).map(([name, implementation]) => [
    name,
    async (params, options) => {
      const normalizedOptions = options ?? (params?.signal ? params : undefined);
      calls.push({ name, params, options: normalizedOptions });
      return implementation(params, options);
    },
  ]));
}
