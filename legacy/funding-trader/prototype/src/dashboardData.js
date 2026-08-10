const LEDGER_INVALIDATION_EVENTS = new Set([
  "ledger.funding_payment.append",
  "ledger.position.verification_changed",
  "ledger.portfolio.updated",
  "verification.status",
]);

export async function loadDashboardSnapshots(api, {
  opportunitySort = "netApr_desc",
  venue = "",
  alertSeverity = "all",
  reportingAsset = "USDT",
  signal,
} = {}) {
  if (!api) throw new TypeError("dashboard API client is required");
  const options = signal ? { signal } : undefined;
  const requests = [
    ["systemStatus", api.getSystemStatus(options)],
    ["readiness", api.getReadiness(options)],
    ["opportunities", api.listOpportunities({ sort: opportunitySort, shortVenueId: venue }, options)],
    ["alerts", api.listAlerts({ severity: alertSeverity }, options)],
    ["positions", api.listPositions({}, options)],
    ["activities", api.listActivities({}, options)],
    ["auditEvents", api.listAuditEvents({}, options)],
    ["portfolio", api.getPortfolioLedger({ reportingAsset }, options)],
    ["closedPositions", api.listClosedPositions({ reportingAsset }, options)],
    ["verification", api.getVerification({}, options)],
  ];
  const settled = await Promise.allSettled(requests.map(([, request]) => request));
  const snapshots = {};
  for (let index = 0; index < requests.length; index += 1) {
    const [key] = requests[index];
    const result = settled[index];
    if (result.status === "fulfilled") {
      snapshots[key] = result.value;
      continue;
    }
    if (key === "portfolio" && isPortfolioEmptyError(result.reason)) {
      snapshots[key] = null;
      continue;
    }
    throw result.reason;
  }
  return snapshots;
}

export function isLedgerInvalidationEvent(kind) {
  return LEDGER_INVALIDATION_EVENTS.has(String(kind ?? ""));
}

function isPortfolioEmptyError(error) {
  return error?.status === 404 && error?.code === "ledger_not_found";
}
