import assert from "node:assert/strict";
import test from "node:test";

import {
  adaptActivity,
  adaptAlert,
  adaptAuditEvent,
  adaptClosedPositionLedger,
  adaptExecution,
  adaptExecutionPreview,
  adaptFundingPaymentPage,
  adaptOpportunity,
  adaptPortfolioLedger,
  adaptPosition,
  adaptSystemStatus,
  adaptTradingReadiness,
  adaptVerificationStatus,
  executionActionGate,
  formatMoney,
  formatLedgerMoney,
  formatPercent,
  isOpportunityWriteReady,
  mapExecutionStateTone,
  normalizeBackendList,
  operationReadinessGate,
  opportunityWriteStatus,
  parseDecimalString,
  plannedLegNotionals,
  readinessWriteGate,
} from "../src/adapters.js";

test("backend list normalization accepts frozen shapes and rejects malformed payloads", () => {
  assert.deepEqual(normalizeBackendList([{ id: "direct" }]), [{ id: "direct" }]);
  assert.deepEqual(normalizeBackendList({ data: { items: [{ id: "wrapped" }] } }), [{ id: "wrapped" }]);
  assert.deepEqual(normalizeBackendList(null), []);
  assert.throws(
    () => normalizeBackendList({ data: { unexpected: [] } }),
    /backend list response does not contain an array/,
  );
});

test("adapters parse frozen opportunity, alert, and preview DTOs deterministically", () => {
  const opportunity = adaptOpportunity({
    opportunityId: "opp-1",
    symbol: "BTC-USDT",
    longVenue: "Binance",
    shortVenue: "Bybit",
    longAccountId: "a",
    shortAccountId: "b",
    longVenueId: "binance-demo",
    shortVenueId: "bybit-demo",
    netAprPct: 18.5,
    grossAprPct: 22.1,
    settlement: "USDT",
    stabilityPct: 83,
    capacityUsd: "12000.50",
    roundTripFeeBps: 6.2,
    estimatedSlippageBps: 1.5,
    basisPct: 0.08,
    dataAgeSeconds: 1.2,
    twoLegWriteReady: false,
    twoLegWriteReason: "venue-maintenance",
    nextFundingAt: "2026-07-29T14:17:27Z",
    fundingSeries: [{ spreadPct: "0.11" }, { spreadPct: "0.13" }],
    calculationInputs: {
      liquidationBufferPct: "60",
      longLeg: {
        accountId: "a",
        instrumentId: "binance:demo:perp:USDT:BTC-USDT",
        venue: "binance",
        venueEnvironment: "demo",
        currentFunding: { rate: "-0.0002" },
      },
      shortLeg: {
        accountId: "b",
        instrumentId: "okx:testnet:perp:USDT:BTC-USDT",
        venue: "okx",
        venueEnvironment: "testnet",
        currentFunding: { rate: "0.0004" },
      },
    },
    blockers: [],
    warnings: [{ code: "capacity_attention", message: "容量接近上限" }],
  });
  assert.equal(opportunity.id, "opp-1");
  assert.equal(opportunity.capacity, 12000.5);
  assert.equal(opportunity.fundingTone, "warning");
  assert.equal(opportunity.settlement, "USDT");
  assert.equal(opportunity.nextFundingTime, "14:17:27");
  assert.equal(opportunity.longVenueId, "binance-demo");
  assert.equal(opportunity.shortVenueId, "bybit-demo");
  assert.equal(opportunity.longVenueEnvironment, "demo");
  assert.equal(opportunity.shortVenueEnvironment, "testnet");
  assert.equal(opportunity.longInstrumentId, "binance:demo:perp:USDT:BTC-USDT");
  assert.equal(opportunity.shortInstrumentId, "okx:testnet:perp:USDT:BTC-USDT");
  assert.equal(Object.hasOwn(opportunity, "venueEnvironment"), false);
  assert.deepEqual(opportunity.spark, [0.11, 0.13]);
  assert.equal(opportunity.longCurrentFunding, -0.02);
  assert.equal(opportunity.shortCurrentFunding, 0.04);
  assert.equal(opportunity.currentFunding, 0.06);
  assert.equal(opportunity.bufferByLeverage[1], 60);
  assert.equal(opportunity.twoLegWriteReady, false);
  assert.equal(opportunity.twoLegWriteReason, "venue-maintenance");

  const alert = adaptAlert({
    alertId: "alert-1",
    token: "BTC",
    exchangeId: "binance",
    currentRatePct: "-0.0142",
    changePct: "-0.0029",
    severity: "critical",
  });
  assert.equal(alert.tone, "danger");
  assert.equal(alert.currentRate, -0.0142);

  const preview = adaptExecutionPreview({
    previewId: "preview-1",
    canSubmit: false,
    blockers: [{ code: "risk_unverifiable", message: "risk not verifiable" }],
    warnings: [],
    estimatedFeesUsd: "2.10",
    expectedNetPnL7dUsd: "8.15",
  });
  assert.equal(preview.status.key, "blocked");
  assert.equal(preview.estimatedFeesUsd, 2.1);
});

test("opportunity adapter never uses account ids as venue ids", () => {
  const opportunity = adaptOpportunity({
    opportunityId: "opp-account-only",
    symbol: "ETH-USDT",
    longVenue: "Binance",
    shortVenue: "Bybit",
    longAccountId: "acct-long",
    shortAccountId: "acct-short",
    longLeg: { accountId: "leg-long" },
    shortLeg: { accountId: "leg-short" },
  });

  assert.equal(opportunity.longVenueId, "");
  assert.equal(opportunity.shortVenueId, "");
  assert.equal(opportunity.longAccountId, "acct-long");
  assert.equal(opportunity.shortAccountId, "acct-short");
});

test("opportunity adapter does not fabricate unavailable funding economics", () => {
  const opportunity = adaptOpportunity({
    opportunityId: "opp-empty",
    symbol: "SOL-USDT",
    blockers: [],
    warnings: [],
  });

  assert.equal(opportunity.currentFunding, null);
  assert.equal(opportunity.longCurrentFunding, null);
  assert.equal(opportunity.shortCurrentFunding, null);
  assert.equal(opportunity.sevenDayFunding, null);
  assert.deepEqual(opportunity.spark, []);
  assert.equal(opportunity.basis, null);
  assert.equal(opportunity.delta, null);
  assert.equal(opportunity.grossApr, null);
  assert.equal(opportunity.stability, null);
  assert.equal(opportunity.positiveSettlements, null);
  assert.equal(opportunity.validSettlements, null);
  assert.equal(opportunity.fundingState, "数据不完整");

  const alert = adaptAlert({ alertId: "alert-empty" });
  assert.equal(alert.currentRate, null);
  assert.equal(alert.change, null);
});

test("preview notional display preserves each backend leg without deriving gross exposure", () => {
  const notionals = plannedLegNotionals({
    plannedLegs: [
      { role: "long", notional: "100.125000000000000001" },
      { role: "short", notional: "99.875000000000000009" },
    ],
  });

  assert.deepEqual(notionals, ["100.125000000000000001", "99.875000000000000009"]);
});

test("execution/position/activity/audit adapters preserve absent economics as null or dash", () => {
  const execution = adaptExecution({
    executionId: "ex-1",
    state: "manualInterventionRequired",
    manualInterventionRequired: true,
    legs: [{ legId: "leg-1", filledQuantity: "0.25" }],
    fills: [{ fee: { amount: "0.01", currency: "USDT" } }],
  });
  assert.equal(execution.tone, "danger");
  assert.equal(execution.filledQuantity, null);
  assert.equal(execution.fees, null);
  assert.equal(execution.legs[0].filledQuantity, "0.25");
  assert.equal(execution.fills[0].fee.amount, "0.01");

  const rejectedExecution = adaptExecution({
    executionId: "ex-rejected-1",
    state: "rejected",
    manualInterventionRequired: false,
    blockingReason: "gateway did not accept the order",
  });
  assert.deepEqual(rejectedExecution.submitFeedback, {
    accepted: false,
    tone: "danger",
    message: "订单已拒绝：ex-rejected-1 · gateway did not accept the order",
    toastMessage: "后端订单已拒绝",
  });

  const failedExecution = adaptExecution({
    executionId: "ex-failed-1",
    state: "failed",
    manualInterventionRequired: false,
  });
  assert.equal(failedExecution.tone, "danger");
  assert.equal(failedExecution.status, "已失败");
  assert.deepEqual(failedExecution.submitFeedback, {
    accepted: false,
    tone: "danger",
    message: "订单执行失败：ex-failed-1",
    toastMessage: "后端订单执行失败",
  });

  const position = adaptPosition({
    positionId: "pos-1",
    symbol: "ETH-USDT",
    longLeg: { venue: "Binance", venueEnvironment: "demo" },
    shortLeg: { venue: "Bybit", venueEnvironment: "testnet" },
    settlement: "USDT",
    nextFundingAt: "2026-07-29T15:00:00Z",
    capitalUsdt: "5000",
    singleLegNotional: "10000",
    expectedNetAprPct: "12.4",
		reportingAsset: "USDT",
		liveEconomicsStatus: "available",
		unrealizedPnlUsd: "0.013",
    state: "open",
  });
	assert.equal(position.pnl, 0.013);
	assert.equal(position.reportingAsset, "USDT");
	assert.equal(position.liveEconomicsStatus, "available");
  assert.equal(position.buffer, null);
  assert.equal(position.delta, null);
  assert.equal(position.settlement, "USDT");
  assert.equal(position.nextFundingTime, "15:00:00");
  assert.equal(position.longVenueEnvironment, "demo");
  assert.equal(position.shortVenueEnvironment, "testnet");
  assert.equal(position.tone, "positive");
  assert.equal(position.statusLabel, "持仓中");

  const positionWithoutEconomics = adaptPosition({ positionId: "pos-empty" });
	assert.equal(positionWithoutEconomics.pnl, null);
	assert.equal(positionWithoutEconomics.reportingAsset, "");
	assert.equal(positionWithoutEconomics.liveEconomicsStatus, "");
  assert.equal(positionWithoutEconomics.capital, null);
  assert.equal(positionWithoutEconomics.amount, null);
  assert.equal(positionWithoutEconomics.leverage, null);
  assert.equal(positionWithoutEconomics.apr, null);

  const positionWithDelta = adaptPosition({
    positionId: "pos-delta",
    status: "attention",
    state: "partially_hedged",
    hedgeDeltaPct: "2.5",
  });
  assert.equal(positionWithDelta.delta, 2.5);
  assert.equal(positionWithDelta.statusLabel, "需关注");
  assert.equal(positionWithDelta.tone, "warning");

  const activity = adaptActivity({
    activityId: "act-1",
    positionId: "pos-1",
    executionId: "ex-close-1",
    occurredAt: "2026-07-29T14:17:27Z",
    kind: "position.closed",
    status: "closed",
    state: "closed",
    operation: "close",
  });
  assert.equal(activity.type, "平仓");
  assert.equal(activity.value, null);
  assert.equal(activity.status, "已完成");
  assert.equal(activity.tone, "positive");

  const rolledBackOpen = adaptActivity({
    activityId: "act-rollback-1",
    positionId: "pos-rollback-1",
    executionId: "ex-open-rollback-1",
    occurredAt: "2026-07-29T14:18:27Z",
    kind: "position.closed",
    status: "closed",
    state: "failed",
    operation: "open",
  });
  assert.equal(rolledBackOpen.type, "开仓回滚");
  assert.equal(rolledBackOpen.status, "已完成");
  assert.equal(rolledBackOpen.tone, "positive");

  const rejectedActivity = adaptActivity({
    activityId: "act-rejected-1",
    executionId: "ex-rejected-1",
    occurredAt: "2026-07-29T14:19:27Z",
    kind: "position.attention_required",
    status: "rejected",
    state: "rejected",
    operation: "open",
    manualInterventionRequired: false,
  });
  assert.equal(rejectedActivity.type, "开仓");
  assert.equal(rejectedActivity.status, "已拒绝");
  assert.equal(rejectedActivity.tone, "danger");

  const manualActivity = adaptActivity({
    activityId: "act-manual-1",
    executionId: "ex-manual-1",
    occurredAt: "2026-07-29T14:20:27Z",
    kind: "position.attention_required",
    status: "manualInterventionRequired",
    state: "manualInterventionRequired",
    operation: "open",
    manualInterventionRequired: true,
  });
  assert.equal(manualActivity.type, "需人工关注");
  assert.equal(manualActivity.status, "需要人工处理");
  assert.equal(manualActivity.tone, "danger");

  const audit = adaptAuditEvent({ auditId: "audit-1", outcome: "denied" });
  assert.equal(audit.tone, "danger");
});

test("execution adapter distinguishes working, recovering, and hedged states", () => {
  const working = adaptExecution({
    executionId: "ex-working-1",
    operationKind: "open",
    state: "open",
    legs: [{ legId: "long", state: "open", filledQuantity: "0" }],
  });
  assert.equal(working.status, "执行中");
  assert.equal(working.tone, "info");

  const recovering = adaptExecution({
    data: {
      executionId: "ex-recovering-1",
      operationKind: "close",
      state: "partiallyFilled",
      legs: [
        { legId: "long", state: "completed", filledQuantity: "0.05" },
        { legId: "short", state: "rejected", filledQuantity: "0" },
      ],
    },
    meta: { degraded: true, recoverable: true },
  });
  assert.equal(recovering.executionId, "ex-recovering-1");
  assert.equal(recovering.status, "恢复中");
  assert.equal(recovering.tone, "warning");
  assert.equal(recovering.degraded, true);
  assert.equal(recovering.recoverable, true);
  assert.deepEqual(recovering.submitFeedback, {
    accepted: true,
    tone: "warning",
    message: "订单已接受，正在恢复：ex-recovering-1",
    toastMessage: "后端订单已接受，正在恢复",
  });

  const hedged = adaptExecution({
    executionId: "ex-hedged-1",
    operationKind: "open",
    state: "open",
    legs: [
      { legId: "long", state: "completed", filledQuantity: "0.05" },
      { legId: "short", state: "completed", filledQuantity: "0.05" },
    ],
  });
  assert.equal(hedged.status, "已对冲");
  assert.equal(hedged.tone, "positive");
});

test("ledger adapters preserve backend economics without recalculating them", () => {
  const closed = adaptClosedPositionLedger({
    positionId: "position-closed-1",
    lineageId: "lineage-1",
    principalId: "principal-1",
    mode: "test",
    symbol: "BTC-USDT",
    reportingAsset: "USDT",
    longLeg: { accountId: "long-account", venue: "binance", instrumentId: "long-inst", venueEnvironment: "demo" },
    shortLeg: { accountId: "short-account", venue: "okx", instrumentId: "short-inst", venueEnvironment: "testnet" },
    realizedTradingPnl: "10.5",
    unrealizedTradingPnl: "-2.25",
    actualFundingCashFlow: "1.75",
    fees: "-0.5",
    netPnl: "777.125",
    closedTradingUpnl: "-2.25",
    verificationStatus: "revised",
    residual: {
      legQuantity: "0.001",
      reportingAssetNotional: "0.25",
      openOrderCount: 1,
      pendingCompensation: 2,
      blockerClass: "dust",
    },
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T12:00:00Z" },
    conversionSource: "USDT/USD",
    conversionAsOf: "2026-08-01T11:59:59Z",
    roundingMode: "half_even",
    scale: 8,
    formulaVersion: "ledger-v1",
  });

  assert.equal(closed.id, "position-closed-1");
  assert.equal(closed.netPnl, "777.125");
  assert.equal(closed.realizedTradingPnl, "10.5");
  assert.equal(closed.actualFundingCashFlow, "1.75");
  assert.equal(closed.fees, "-0.5");
  assert.equal(closed.closedTradingUpnl, "-2.25");
  assert.equal(closed.verificationStatus, "revised");
  assert.equal(closed.residual.legQuantity, "0.001");
  assert.equal(closed.residual.openOrderCount, 1);
  assert.equal(closed.watermark.completeThrough, "2026-08-01T12:00:00Z");
  assert.equal(closed.longLeg.venueEnvironment, "demo");
  assert.equal(closed.shortLeg.venueEnvironment, "testnet");

  const portfolio = adaptPortfolioLedger({
    principalId: "principal-1",
    mode: "test",
    reportingAsset: "USDT",
    asOf: "2026-08-01T12:00:00Z",
    cumulativeRealizedNetPnl: "12.5",
    currentOpenUnrealizedPnl: "-3",
    totalNetPnl: "999.25",
    actualFundingCashFlow: "2.75",
    fees: "-0.75",
    openPositionCount: 1,
    closedPositionCount: 4,
    verificationStatus: "provisional",
		liveEconomicsStatus: "available",
    residual: { legQuantity: "0", reportingAssetNotional: "0", openOrderCount: 0, pendingCompensation: 0, blockerClass: "" },
    watermark: { status: "incomplete", verified: false, completeThrough: "" },
  });
  assert.equal(portfolio.totalNetPnl, "999.25");
  assert.equal(portfolio.currentOpenUnrealizedPnl, "-3");
  assert.equal(portfolio.actualFundingCashFlow, "2.75");
  assert.equal(portfolio.openPositionCount, 1);
  assert.equal(portfolio.closedPositionCount, 4);
  assert.equal(portfolio.verificationStatus, "provisional");
	assert.equal(portfolio.liveEconomicsStatus, "available");

	const unavailable = adaptPortfolioLedger({
		verificationStatus: "incomplete",
		liveEconomicsStatus: "incomplete",
		currentOpenUnrealizedPnl: null,
		totalNetPnl: null,
	});
  assert.equal(unavailable.totalNetPnl, null);
	assert.equal(unavailable.actualFundingCashFlow, null);
	assert.equal(unavailable.liveEconomicsStatus, "incomplete");
});

test("funding payment and verification adapters keep payment finality distinct from rates", () => {
  const page = adaptFundingPaymentPage({
    items: [{
      paymentId: "payment-1",
      factId: "fact-1",
      accountId: "account-1",
      instrumentId: "BTC-USDT-PERP",
      venue: "binance",
      venueEnvironment: "demo",
      settlementAsset: "USDT",
      amount: "-1.25000001",
      effectiveAt: "2026-08-01T08:00:00Z",
      observedAt: "2026-08-01T08:00:05Z",
      revision: "17",
      formulaVersion: "ledger-v1",
      rate: "0.50",
    }],
    status: "complete",
    completeThrough: "2026-08-01T08:00:00Z",
    revision: "17",
    nextCursor: "opaque-next",
    watermark: { status: "complete", verified: true, completeThrough: "2026-08-01T08:00:00Z" },
  });
  assert.equal(page.items[0].amount, "-1.25000001");
  assert.equal(page.items[0].settlementAsset, "USDT");
  assert.equal(Object.hasOwn(page.items[0], "rate"), false);
  assert.equal(page.status, "complete");
  assert.equal(page.completeThrough, "2026-08-01T08:00:00Z");
  assert.equal(page.nextCursor, "opaque-next");
  assert.equal(adaptFundingPaymentPage({ items: [{ amount: "" }], status: "incomplete" }).items[0].amount, null);
  assert.equal(formatLedgerMoney("12345678901234567890.12345678", "USDT", { signed: true }), "+USDT 12345678901234567890.12345678");

  const verification = adaptVerificationStatus({
    status: "incomplete",
    reason: "funding_watermark_incomplete",
    details: "waiting for exact scope",
    openReady: false,
    closeReady: true,
    recoveryReady: true,
    watermark: { status: "incomplete", verified: false, completeThrough: "" },
  });
  assert.equal(verification.openReady, false);
  assert.equal(verification.closeReady, true);
  assert.equal(verification.recoveryReady, true);
  assert.equal(verification.status, "incomplete");
});

test("trading readiness keeps open close and recovery gates independent", () => {
  assert.deepEqual(
    adaptTradingReadiness({ openReady: false, closeReady: true, recoveryReady: true, readinessReason: "active_lineage_close" }),
    { openReady: false, closeReady: true, recoveryReady: true, reason: "active_lineage_close" },
  );
  assert.deepEqual(
    readinessWriteGate({
      openReady: false,
      closeReady: true,
      recoveryReady: true,
      readinessReason: "active_lineage_close",
      components: { execution: { details: { twoLegWriteReady: true, twoLegWriteReason: "legacy-ready" } } },
    }),
    { ready: false, reason: "active_lineage_close" },
  );
  assert.deepEqual(
    operationReadinessGate({ openReady: false, closeReady: true, recoveryReady: true, readinessReason: "active_lineage_close" }, "close"),
    { ready: true, reason: "active_lineage_close" },
  );
  assert.deepEqual(
    operationReadinessGate({ openReady: false, closeReady: true, recoveryReady: true }, "open"),
    { ready: false, reason: "" },
  );
  assert.deepEqual(
    operationReadinessGate({ openReady: true, closeReady: true, recoveryReady: true }, "unknown"),
    { ready: false, reason: "" },
  );
});

test("generic trading not-ready reason exposes the actionable two-leg blocker", () => {
  assert.deepEqual(
    adaptTradingReadiness({
      openReady: false,
      closeReady: false,
      recoveryReady: false,
      readinessReason: "not_ready",
      components: {
        twoLegWrite: { ready: false, reason: "incompatible-settlement-assets" },
      },
    }),
    {
      openReady: false,
      closeReady: false,
      recoveryReady: false,
      reason: "incompatible-settlement-assets",
    },
  );
});

test("system status adapter exposes backend product mode without browser overrides", () => {
  assert.deepEqual(
    adaptSystemStatus({ data: { mode: "test", environment: "demo", ready: true, build: "abc" } }),
    { mode: "test", modeLabel: "TEST", ready: true, build: "abc", version: "", maintenance: false, maintenanceReason: "" },
  );
  assert.deepEqual(
    adaptSystemStatus({ mode: "live", ready: false, version: "1.2.3", maintenance: true, maintenanceReason: "upgrade" }),
    { mode: "live", modeLabel: "LIVE", ready: false, build: "", version: "1.2.3", maintenance: true, maintenanceReason: "upgrade" },
  );
  assert.equal(adaptSystemStatus({ mode: "demo" }).mode, "");
});

test("write gates fail closed unless deterministic readiness returns literal openReady", () => {
  assert.deepEqual(
    readinessWriteGate({
      openReady: true,
      closeReady: false,
      recoveryReady: false,
      readinessReason: "normal_test_open",
    }),
    { ready: true, reason: "normal_test_open" },
  );
  assert.deepEqual(
    readinessWriteGate({
      components: { execution: { details: { twoLegWriteReady: true, twoLegWriteReason: "legacy-shape" } } },
      reason: "missing-private-v1-execution-component",
    }),
    { ready: false, reason: "missing-private-v1-execution-component" },
  );
  assert.equal(
    readinessWriteGate({
      openReady: "true",
    }).ready,
    false,
  );

  assert.equal(isOpportunityWriteReady({ twoLegWriteReady: true }, true), true);
  assert.equal(isOpportunityWriteReady({ twoLegWriteReady: false }, true), false);
  assert.equal(isOpportunityWriteReady({}, true), false);
  assert.equal(isOpportunityWriteReady({}, true, true), true);
  assert.equal(isOpportunityWriteReady({ twoLegWriteReady: false }, true, true), false);
  assert.equal(isOpportunityWriteReady({}, false, true), false);
  assert.equal(isOpportunityWriteReady({ twoLegWriteReady: "true" }, true), false);
  assert.equal(isOpportunityWriteReady({ twoLegWriteReady: true }, false), false);
});

test("opportunity write status distinguishes preview-pending rows from backend write blocks", () => {
  assert.deepEqual(
    opportunityWriteStatus({}, true),
    { ready: false, reason: "待预览校验", tone: "pending" },
  );
  assert.deepEqual(
    opportunityWriteStatus({ twoLegWriteReady: false, twoLegWriteReason: "账户未就绪" }, true),
    { ready: false, reason: "账户未就绪", tone: "blocked" },
  );
  assert.deepEqual(
    opportunityWriteStatus({}, false, { globalWriteBlockReason: "gateway not ready" }),
    { ready: false, reason: "gateway not ready", tone: "blocked" },
  );
  assert.deepEqual(
    opportunityWriteStatus({}, true, { previewCanSubmit: true }),
    { ready: true, reason: "两腿写入就绪", tone: "ready" },
  );
});

test("execution action gate allows safe previews while keeping private submit gated", () => {
  assert.deepEqual(
    executionActionGate({
      step: 0,
      hasOpportunity: true,
      submitting: false,
      writesReady: false,
      previewCanSubmit: false,
    }),
    { disabled: false, label: "载入后端预览" },
  );
  assert.deepEqual(
    executionActionGate({
      step: 1,
      hasOpportunity: true,
      submitting: false,
      writesReady: false,
      previewCanSubmit: true,
    }),
    { disabled: true, label: "后端禁止写入" },
  );
  assert.deepEqual(
    executionActionGate({
      step: 1,
      hasOpportunity: true,
      submitting: false,
      writesReady: true,
      previewCanSubmit: true,
    }),
    { disabled: false, label: "确认提交" },
  );
  assert.deepEqual(
    executionActionGate({
      step: 1,
      hasOpportunity: true,
      submitting: false,
      writesReady: true,
      previewCanSubmit: false,
    }),
    { disabled: true, label: "确认提交" },
  );
  assert.equal(
    executionActionGate({
      step: 0,
      hasOpportunity: false,
      submitting: false,
      writesReady: true,
      previewCanSubmit: false,
    }).disabled,
    true,
  );
  assert.equal(
    executionActionGate({
      step: 0,
      hasOpportunity: true,
      submitting: true,
      writesReady: true,
      previewCanSubmit: false,
    }).disabled,
    true,
  );
});

test("formatting helpers never fabricate invalid values", () => {
  assert.equal(parseDecimalString("bad"), null);
  assert.equal(formatMoney(null), "—");
  assert.equal(formatPercent(1.234, 1, { signed: true }), "+1.2%");
  assert.equal(mapExecutionStateTone("compensating"), "warning");
});
