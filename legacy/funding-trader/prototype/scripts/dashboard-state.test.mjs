import assert from "node:assert/strict";
import test from "node:test";

import {
  canCommitAfterApiError,
  createAuthSnapshotGuard,
  clearPrincipalBusinessState,
	clearPrincipalRuntimeState,
	emptyPrincipalBusinessState,
	mergePositionUpsert,
	realtimeSnapshotFields,
} from "../src/dashboardState.js";

test("principal-scoped business state clears stale executions across auth boundaries without touching login/theme state", () => {
  const before = {
    theme: "light",
    loginEmail: "owner@example.test",
    opportunities: [{ id: "old-opp" }],
    alerts: [{ id: "old-alert" }],
    positions: [{ id: "old-position" }],
    closedPositions: [{ id: "old-closed" }],
    portfolioLedger: { totalNetPnl: "12" },
    verification: { status: "verified" },
    activities: [{ id: "old-activity" }],
    auditEvents: [{ id: "old-audit" }],
    executions: [{ executionId: "exec_0d203e29e7744bbd", state: "uncertain" }],
  };

  const after = clearPrincipalBusinessState(before);

  assert.equal(after.theme, "light");
  assert.equal(after.loginEmail, "owner@example.test");
  assert.deepEqual(after, {
    ...before,
    ...emptyPrincipalBusinessState(),
  });
  assert.deepEqual(after.executions, []);
  assert.equal(after.portfolioLedger, null);
  assert.equal(after.verification, null);
});

test("auth snapshot guard rejects old in-flight snapshots after logout login generation changes", () => {
  const guard = createAuthSnapshotGuard();
  guard.boundary("principal-old");
  const oldSnapshot = guard.startSnapshot();

  guard.boundary("");
  assert.equal(oldSnapshot.signal.aborted, true);
  guard.boundary("principal-new");

  assert.equal(guard.canCommit(oldSnapshot), false);
  const newSnapshot = guard.startSnapshot();
  assert.equal(guard.canCommit(newSnapshot), true);
  guard.finishSnapshot(newSnapshot);
});

test("deferred old snapshot cannot repopulate cleared business state after relogin", () => {
  const guard = createAuthSnapshotGuard();
  let dashboard = {
    ...emptyPrincipalBusinessState(),
    executions: [{ executionId: "exec_0d203e29e7744bbd", state: "uncertain" }],
    positions: [{ positionId: "old-position" }],
  };

  guard.boundary("principal-old");
  const oldSnapshot = guard.startSnapshot();
  const oldBackendData = {
    executions: [{ executionId: "exec_0d203e29e7744bbd", state: "uncertain" }],
    positions: [{ positionId: "old-position" }],
  };

  dashboard = clearPrincipalBusinessState(dashboard);
  guard.boundary("");
  guard.boundary("principal-new");
  const newSnapshot = guard.startSnapshot();
  if (guard.canCommit(newSnapshot)) {
    dashboard = { ...dashboard, executions: [], positions: [] };
  }
  if (guard.canCommit(oldSnapshot)) {
    dashboard = { ...dashboard, ...oldBackendData };
  }

  assert.deepEqual(dashboard.executions, []);
  assert.deepEqual(dashboard.positions, []);
});

test("principal runtime state clears old SSE cursor and close idempotency keys", () => {
  const cursorRef = { current: "old-cursor" };
  const closeKeysRef = { current: new Map([["position-1", "old-idempotency-key"]]) };

  clearPrincipalRuntimeState({ cursorRef, closeKeysRef });

  assert.equal(cursorRef.current, "");
  assert.equal(closeKeysRef.current.size, 0);
});

test("partial realtime snapshots preserve authoritative ledger state that is not present", () => {
	const fields = realtimeSnapshotFields({
		positions: [{ positionId: "position-open" }],
		activities: [],
	});

	assert.deepEqual(fields.positions, [{ positionId: "position-open" }]);
	assert.deepEqual(fields.activities, []);
	assert.equal(fields.portfolioLedger, undefined);
	assert.equal(fields.closedPositions, undefined);
	assert.equal(fields.verification, undefined);

	const explicitClear = realtimeSnapshotFields({ portfolio: null, closedPositions: [], verification: null });
	assert.equal(explicitClear.portfolioLedger, null);
	assert.deepEqual(explicitClear.closedPositions, []);
	assert.equal(explicitClear.verification, null);
});

test("sparse position upserts preserve snapshot economics unless explicitly provided", () => {
	const current = [{
		positionId: "position-open",
		symbol: "BTCUSDT",
		state: "open",
		pnl: 12.34,
		reportingAsset: "USDT",
		liveEconomicsStatus: "available",
	}];

	const sparse = mergePositionUpsert(current, {
		positionId: "position-open",
		symbol: "ETHUSDT",
		state: "attention",
		pnl: null,
		reportingAsset: "",
		liveEconomicsStatus: "",
	}, {
		positionId: "position-open",
		symbol: "ETHUSDT",
		state: "attention",
	});

	assert.deepEqual(sparse, [{
		positionId: "position-open",
		symbol: "ETHUSDT",
		state: "attention",
		pnl: 12.34,
		reportingAsset: "USDT",
		liveEconomicsStatus: "available",
	}]);

	const explicit = mergePositionUpsert(sparse, {
		positionId: "position-open",
		pnl: null,
		reportingAsset: "",
		liveEconomicsStatus: "unavailable",
	}, {
		positionId: "position-open",
		unrealizedPnlUsd: null,
		reportingAsset: "",
		liveEconomicsStatus: "unavailable",
	});

	assert.equal(explicit[0].pnl, null);
	assert.equal(explicit[0].reportingAsset, "");
	assert.equal(explicit[0].liveEconomicsStatus, "unavailable");
});

test("deferred preview submit and close actions cannot mutate state after auth generation changes", () => {
  const guard = createAuthSnapshotGuard();
  let state = {
    preview: null,
    executions: [],
    status: null,
    toast: null,
    nav: "机会",
  };
  const closeKeysRef = { current: new Map([["position-new", "new-principal-close-key"]]) };

  guard.boundary("principal-old");
  const previewAction = guard.captureAction();
  const submitAction = guard.captureAction();
  const closeAction = guard.captureAction();

  guard.boundary("");
  clearPrincipalRuntimeState({ closeKeysRef: { current: new Map([["position-old", "old-close-key"]]) } });
  guard.boundary("principal-new");

  if (guard.canCommit(previewAction)) {
    state.preview = { previewId: "old-preview" };
    state.status = { tone: "info", message: "old preview" };
  }
  if (guard.canCommit(submitAction)) {
    state.executions = [{ executionId: "old-exec" }];
    state.nav = "持仓";
    state.toast = { tone: "positive", message: "old submit accepted" };
  }
  if (guard.canCommit(closeAction)) {
    closeKeysRef.current.delete("position-new");
    state.toast = { tone: "positive", message: "old close accepted" };
  }

  assert.deepEqual(state, {
    preview: null,
    executions: [],
    status: null,
    toast: null,
    nav: "机会",
  });
  assert.equal(closeKeysRef.current.get("position-new"), "new-principal-close-key");
});

test("preview submit and close 401 handlers stop before leaving status or toast residue", () => {
  for (const operation of ["preview", "submit", "close"]) {
    const guard = createAuthSnapshotGuard();
    guard.boundary("principal-old");
    const action = guard.captureAction();
    let state = { status: null, toast: null };

    const handled = true;
    guard.boundary("");
    if (canCommitAfterApiError(guard, action, handled)) {
      if (operation === "close") {
        state.toast = { tone: "danger", message: "old close failed" };
      } else {
        state.status = { tone: "danger", message: `old ${operation} failed` };
      }
    }

    assert.deepEqual(state, { status: null, toast: null }, operation);
  }
});
