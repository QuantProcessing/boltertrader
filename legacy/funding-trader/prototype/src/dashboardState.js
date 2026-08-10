export const PRINCIPAL_SCOPED_DASHBOARD_KEYS = Object.freeze([
  "opportunities",
  "alerts",
  "positions",
  "closedPositions",
  "activities",
  "auditEvents",
  "executions",
]);

export function emptyPrincipalBusinessState() {
  return {
    opportunities: [],
    alerts: [],
    positions: [],
    closedPositions: [],
    portfolioLedger: null,
    verification: null,
    activities: [],
    auditEvents: [],
    executions: [],
  };
}

export function clearPrincipalBusinessState(current = {}) {
  return {
    ...current,
    ...emptyPrincipalBusinessState(),
  };
}

export function clearPrincipalRuntimeState({ cursorRef, closeKeysRef } = {}) {
  if (cursorRef) cursorRef.current = "";
  closeKeysRef?.current?.clear?.();
}

export function realtimeSnapshotFields(data = {}) {
  const fields = {};
  for (const key of ["opportunities", "alerts", "positions", "closedPositions", "activities", "auditEvents", "executions"]) {
    if (Array.isArray(data?.[key])) fields[key] = data[key];
  }
  if (Object.prototype.hasOwnProperty.call(data ?? {}, "portfolioLedger") ||
    Object.prototype.hasOwnProperty.call(data ?? {}, "portfolio")) {
    fields.portfolioLedger = data?.portfolioLedger ?? data?.portfolio ?? null;
  }
  if (Object.prototype.hasOwnProperty.call(data ?? {}, "verification")) {
    fields.verification = data?.verification ?? null;
  }
  return fields;
}

function idOf(record, keys) {
  for (const key of keys) {
    if (record?.[key] != null && String(record[key]).trim() !== "") return String(record[key]);
  }
  return "";
}

function hasOwn(data, key) {
  return Object.prototype.hasOwnProperty.call(data ?? {}, key);
}

function positionUpsertEconomicsPatch(next, rawPayload) {
  const patch = {};
  if (!hasOwn(rawPayload, "unrealizedPnlUsd")) patch.pnl = next?.pnl;
  if (!hasOwn(rawPayload, "reportingAsset")) patch.reportingAsset = next?.reportingAsset;
  if (!hasOwn(rawPayload, "liveEconomicsStatus")) patch.liveEconomicsStatus = next?.liveEconomicsStatus;
  return patch;
}

export function mergePositionUpsert(current = [], next, rawPayload = {}) {
  if (!next) return current;
  const nextId = idOf(next, ["id", "positionId"]);
  if (!nextId) return current;
  const index = current.findIndex((item) => idOf(item, ["id", "positionId"]) === nextId);
  if (index < 0) return [next, ...current];
  return current.map((item, itemIndex) => {
    if (itemIndex !== index) return item;
    return {
      ...item,
      ...next,
      ...positionUpsertEconomicsPatch(item, rawPayload),
    };
  });
}

export function canCommitAfterApiError(guard, actionToken, handled) {
  return handled !== true && guard?.canCommit?.(actionToken) === true;
}

export function createAuthSnapshotGuard({ AbortControllerImpl = globalThis.AbortController } = {}) {
  let generation = 0;
  let principalId = "";
  let activeSnapshotController = null;

  const abortActiveSnapshot = () => {
    activeSnapshotController?.abort();
    activeSnapshotController = null;
  };

  return {
    getGeneration() {
      return generation;
    },
    getPrincipalId() {
      return principalId;
    },
    boundary(nextPrincipalId = "") {
      generation += 1;
      principalId = String(nextPrincipalId ?? "");
      abortActiveSnapshot();
      return generation;
    },
    startSnapshot() {
      abortActiveSnapshot();
      const controller = typeof AbortControllerImpl === "function" ? new AbortControllerImpl() : null;
      activeSnapshotController = controller;
      return {
        generation,
        principalId,
        signal: controller?.signal,
      };
    },
    captureAction() {
      return {
        generation,
        principalId,
      };
    },
    canCommit(snapshot) {
      return Boolean(snapshot) &&
        snapshot.generation === generation &&
        snapshot.principalId === principalId &&
        snapshot.signal?.aborted !== true;
    },
    finishSnapshot(snapshot) {
      if (activeSnapshotController?.signal === snapshot?.signal) {
        activeSnapshotController = null;
      }
    },
  };
}
