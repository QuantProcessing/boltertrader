const DASH = "—";
const LEDGER_DECIMAL_PATTERN = /^[+-]?(?:\d+(?:\.\d*)?|\.\d+)$/;
const VERIFICATION_STATUSES = new Set([
  "unsupported",
  "temporarily_unavailable",
  "incomplete",
  "provisional",
  "verified",
  "revised",
  "blocked",
]);

export function parseDecimalString(value) {
  if (value === null || value === undefined || value === "") return null;
  if (typeof value === "number") return Number.isFinite(value) ? value : null;
  const parsed = Number(String(value).trim());
  return Number.isFinite(parsed) ? parsed : null;
}

export function displayOrDash(value) {
  return value === null || value === undefined || value === "" ? DASH : value;
}

export function formatDecimal(value, digits = 2) {
  const parsed = parseDecimalString(value);
  return parsed === null ? DASH : parsed.toLocaleString("en-US", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  });
}

export function formatPercent(value, digits = 2, { signed = false } = {}) {
  const parsed = parseDecimalString(value);
  if (parsed === null) return DASH;
  const sign = signed && parsed > 0 ? "+" : "";
  return `${sign}${parsed.toFixed(digits)}%`;
}

export function formatMoney(value, currency = "USD", digits = 2, { signed = false } = {}) {
  const parsed = parseDecimalString(value);
  if (parsed === null) return DASH;
  const sign = signed && parsed > 0 ? "+" : "";
  const prefix = currency === "USD" || currency === "USDT" ? "$" : `${currency} `;
  return `${sign}${prefix}${formatDecimal(parsed, digits)}`;
}

export function ledgerDecimalString(value) {
  const raw = value && typeof value === "object" && "amount" in value ? value.amount : value;
  if (raw === null || raw === undefined || raw === "") return null;
  const text = String(raw).trim();
  return LEDGER_DECIMAL_PATTERN.test(text) ? text : null;
}

export function formatLedgerMoney(value, asset = "", { signed = false } = {}) {
  const amount = ledgerDecimalString(value);
  if (amount === null) return DASH;
  const unsigned = amount.replace(/^[+-]/, "");
  const nonZero = /[1-9]/.test(unsigned);
  const sign = signed && nonZero && !amount.startsWith("-") && !amount.startsWith("+") ? "+" : "";
  const prefix = String(asset ?? "").trim();
  return `${sign}${prefix ? `${prefix} ` : ""}${amount}`;
}

export function formatTime(value) {
  if (!value) return DASH;
  const date = new Date(value);
  if (Number.isNaN(date.getTime())) return DASH;
  return date.toISOString().slice(11, 19);
}

export function normalizeBackendList(response, keyCandidates = ["items"]) {
  const data = response?.data ?? response;
  if (data === null || data === undefined) return [];
  if (Array.isArray(data)) return data;
  for (const key of keyCandidates) {
    if (Array.isArray(data?.[key])) return data[key];
  }
  throw new TypeError("backend list response does not contain an array");
}

export function readinessWriteGate(readiness) {
  return operationReadinessGate(readiness, "open");
}

export function operationReadinessGate(readiness, operation) {
  const trading = adaptTradingReadiness(readiness);
  const ready = operation === "open"
    ? trading.openReady
    : operation === "close"
      ? trading.closeReady
      : operation === "recovery"
        ? trading.recoveryReady
        : false;
  return { ready, reason: trading.reason || readiness?.reason || "" };
}

export function adaptTradingReadiness(dto) {
  const data = dto?.data && dto.openReady === undefined ? dto.data : dto;
  const readinessReason = data?.readinessReason ?? data?.reason ?? "";
  const twoLegReason = data?.components?.twoLegWrite?.reason
    ?? data?.components?.execution?.details?.twoLegWriteReason
    ?? "";
  return {
    openReady: data?.openReady === true,
    closeReady: data?.closeReady === true,
    recoveryReady: data?.recoveryReady === true,
    reason: (!readinessReason || readinessReason === "not_ready") && twoLegReason
      ? twoLegReason
      : readinessReason,
  };
}

export function isOpportunityWriteReady(opportunity, globalWritesReady, previewCanSubmit = false) {
  if (globalWritesReady !== true || opportunity?.twoLegWriteReady === false) return false;
  return opportunity?.twoLegWriteReady === true || previewCanSubmit === true;
}

export function opportunityWriteStatus(
  opportunity,
  globalWritesReady,
  { globalWriteBlockReason = "", previewCanSubmit = false } = {},
) {
  if (globalWritesReady !== true) {
    return { ready: false, reason: globalWriteBlockReason || "后端禁止写入", tone: "blocked" };
  }
  if (opportunity?.twoLegWriteReady === false) {
    return { ready: false, reason: opportunity?.twoLegWriteReason || "后端禁止写入", tone: "blocked" };
  }
  if (previewCanSubmit === true) {
    return { ready: true, reason: "两腿写入就绪", tone: "ready" };
  }
  return { ready: false, reason: "待预览校验", tone: "pending" };
}

export function executionActionGate({ step, hasOpportunity, submitting, writesReady, previewCanSubmit }) {
  if (submitting) {
    return { disabled: true, label: "正在提交…" };
  }
  if (!hasOpportunity) {
    return { disabled: true, label: step === 0 ? "载入后端预览" : "确认提交" };
  }
  if (step === 0) {
    return { disabled: false, label: "载入后端预览" };
  }
  if (writesReady !== true) {
    return { disabled: true, label: "后端禁止写入" };
  }
  return { disabled: previewCanSubmit !== true, label: "确认提交" };
}

export function adaptOpportunity(dto) {
  const data = dto?.data && !dto.opportunityId ? dto.data : dto;
  const blockers = Array.isArray(data?.blockers) ? data.blockers : [];
  const warnings = Array.isArray(data?.warnings) ? data.warnings : [];
  const stabilityPct = parseDecimalString(data?.stabilityPct);
  const capacity = parseDecimalString(data?.capacityUsd);
  const netApr = parseDecimalString(data?.netAprPct);
  const grossApr = parseDecimalString(data?.grossAprPct);
  const dataAgeSeconds = parseDecimalString(data?.dataAgeSeconds);
  const positiveSettlements = nonNegativeIntegerOrNull(data?.positiveSettlements ?? data?.positiveFundingSettlements);
  const validSettlements = nonNegativeIntegerOrNull(data?.validSettlements ?? data?.fundingSettlements);
  const longLeg = data?.calculationInputs?.longLeg ?? data?.longLeg ?? {};
  const shortLeg = data?.calculationInputs?.shortLeg ?? data?.shortLeg ?? {};
  const longCurrentFundingRate = parseDecimalString(longLeg?.currentFunding?.rate);
  const shortCurrentFundingRate = parseDecimalString(shortLeg?.currentFunding?.rate);
  const longCurrentFunding = longCurrentFundingRate === null ? null : longCurrentFundingRate * 100;
  const shortCurrentFunding = shortCurrentFundingRate === null ? null : shortCurrentFundingRate * 100;
  const currentFunding = longCurrentFunding !== null && shortCurrentFunding !== null
    ? shortCurrentFunding - longCurrentFunding
    : null;
  const fundingTone = blockers.length > 0
    ? "danger"
    : warnings.length > 0
      ? "warning"
      : currentFunding === null
        ? "neutral"
        : "positive";
  const fundingSeries = Array.isArray(data?.fundingSeries) ? data.fundingSeries : [];
  const liquidationBufferPct = parseDecimalString(
    data?.liquidationBufferPct ?? data?.calculationInputs?.liquidationBufferPct,
  );

  return {
    raw: data,
    id: data?.opportunityId ?? data?.id ?? "",
    opportunityId: data?.opportunityId ?? data?.id ?? "",
    symbol: data?.symbol ?? DASH,
    longVenue: data?.longVenue ?? longLeg?.venue ?? DASH,
    shortVenue: data?.shortVenue ?? shortLeg?.venue ?? DASH,
    longVenueId: data?.longVenueId ?? longLeg?.venueId ?? longLeg?.venue ?? "",
    shortVenueId: data?.shortVenueId ?? shortLeg?.venueId ?? shortLeg?.venue ?? "",
    longAccountId: data?.longAccountId ?? longLeg?.accountId ?? "",
    shortAccountId: data?.shortAccountId ?? shortLeg?.accountId ?? "",
    longInstrumentId: data?.longInstrumentId ?? longLeg?.instrumentId ?? "",
    shortInstrumentId: data?.shortInstrumentId ?? shortLeg?.instrumentId ?? "",
    longVenueEnvironment: longLeg?.venueEnvironment ?? "",
    shortVenueEnvironment: shortLeg?.venueEnvironment ?? "",
    netApr,
    grossApr,
    roundTripFeeBps: parseDecimalString(data?.roundTripFeeBps),
    estimatedSlippageBps: parseDecimalString(data?.estimatedSlippageBps),
    positiveSettlements,
    validSettlements,
    stability: stabilityPct,
    longDepth: parseDecimalString(data?.longDepthUsd ?? data?.longLeg?.depthUsd),
    shortDepth: parseDecimalString(data?.shortDepthUsd ?? data?.shortLeg?.depthUsd),
    capacity,
    settlement: data?.settlement ?? "",
    nextFundingAt: data?.nextFundingAt ?? "",
    nextFundingTime: formatTime(data?.nextFundingAt),
    fundingState: blockers.length > 0 ? "不可执行" : warnings.length > 0 ? "需关注" : currentFunding === null ? "数据不完整" : "已载入",
    fundingTone,
    fundingNote: [...blockers, ...warnings].map(findingText).filter(Boolean).join("；") || "后端未返回风险提示。",
    recentReversal: warnings.some((item) => findingText(item).includes("反转")),
    delta: parseDecimalString(data?.hedgeDeltaPct),
    bufferByLeverage: {
      1: liquidationBufferPct,
      2: liquidationBufferPct,
    },
    currentFunding,
    longCurrentFunding,
    shortCurrentFunding,
    sevenDayFunding: parseDecimalString(data?.sevenDayFundingPct),
    basis: parseDecimalString(data?.basisPct),
    dataAgeSeconds,
    venueAvailable: blockers.length === 0,
    spark: fundingSeries.map((point) => parseDecimalString(point?.spreadPct)).filter((value) => value !== null),
    blockers,
    warnings,
    formulaVersion: data?.formulaVersion ?? "",
    observedAt: data?.observedAt ?? "",
    twoLegWriteReady: data?.twoLegWriteReady,
    twoLegWriteReason: data?.twoLegWriteReason ?? "",
  };
}

export function adaptOpportunityList(response) {
  return normalizeBackendList(response, ["items", "opportunities"]).map(adaptOpportunity);
}

export function adaptAlert(dto) {
  const data = dto?.data && !dto.alertId ? dto.data : dto;
  const severity = data?.severity ?? "watch";
  const tone = severity === "critical" ? "danger" : severity === "warning" ? "warning" : "info";
  return {
    raw: data,
    id: data?.alertId ?? data?.id ?? "",
    alertId: data?.alertId ?? data?.id ?? "",
    token: data?.token ?? DASH,
    contract: data?.contract ?? DASH,
    exchange: data?.exchange ?? data?.exchangeId ?? DASH,
    exchangeId: data?.exchangeId ?? "",
    currentRate: parseDecimalString(data?.currentRatePct),
    thresholdLabel: data?.thresholdLabel ?? DASH,
    change: parseDecimalString(data?.changePct),
    changeWindow: data?.changeWindow ?? "近 3 期",
    settlement: data?.settlement ?? "",
    nextFundingAt: data?.nextFundingAt ?? "",
    nextFundingTime: formatTime(data?.nextFundingAt),
    severity,
    tone,
    severityLabel: severity === "critical" ? "严重" : severity === "warning" ? "警告" : "观察",
    opportunityId: data?.relatedOpportunityId ?? data?.opportunityId ?? "",
  };
}

export function adaptAlertList(response) {
  return normalizeBackendList(response, ["items", "alerts"]).map(adaptAlert);
}

export function adaptExecutionPreview(dto) {
  const data = dto?.data && !dto.previewId ? dto.data : dto;
  const blockers = Array.isArray(data?.blockers) ? data.blockers : [];
  const warnings = Array.isArray(data?.warnings) ? data.warnings : [];
  return {
    raw: data,
    id: data?.previewId ?? "",
    previewId: data?.previewId ?? "",
    opportunityId: data?.opportunityId ?? "",
    snapshotId: data?.snapshotId ?? "",
    canSubmit: Boolean(data?.canSubmit),
    blockers,
    warnings,
    plannedLegs: Array.isArray(data?.plannedLegs) ? data.plannedLegs : [],
    estimatedFeesUsd: parseDecimalString(data?.estimatedFeesUsd),
    estimatedSlippageUsd: parseDecimalString(data?.estimatedSlippageUsd),
    expectedNetAprPct: parseDecimalString(data?.expectedNetAprPct),
    expectedNetPnL7dUsd: parseDecimalString(data?.expectedNetPnL7dUsd),
    status: blockers.length > 0
      ? { key: "blocked", label: "不可执行", tone: "danger", reasons: blockers.map(reasonFromFinding) }
      : warnings.length > 0
        ? { key: "attention", label: "需关注", tone: "warning", reasons: warnings.map(reasonFromFinding) }
        : { key: "passed", label: "检查通过", tone: "positive", reasons: [{ text: "后端执行预览检查通过", state: "positive" }] },
  };
}

export function plannedLegNotionals(preview) {
  const legs = Array.isArray(preview?.plannedLegs) ? preview.plannedLegs : [];
  return [ledgerDecimalString(legs[0]?.notional), ledgerDecimalString(legs[1]?.notional)];
}

export function adaptExecution(dto) {
  const data = dto?.data && !dto.executionId ? dto.data : dto;
  const meta = dto?.data && !dto.executionId ? dto.meta ?? {} : {};
  const state = data?.state ?? "uncertain";
  const legs = Array.isArray(data?.legs) ? data.legs : [];
  const fills = Array.isArray(data?.fills) ? data.fills : [];
  const degraded = Boolean(meta?.degraded ?? data?.degraded);
  const recoverable = Boolean(meta?.recoverable ?? data?.recoverable);
  const hedged = state === "open" && legs.length >= 2 && legs.every((leg) => leg?.state === "completed");
  const stateTone = degraded || recoverable
    ? "warning"
    : state === "open" && !hedged
      ? "info"
      : mapExecutionStateTone(state);
  return {
    raw: data,
    id: data?.executionId ?? "",
    executionId: data?.executionId ?? "",
    operationKind: data?.operationKind ?? "",
    opportunityId: data?.opportunityId ?? "",
    state,
    tone: stateTone,
    status: recoverable ? "恢复中" : state === "open" && !hedged ? "执行中" : executionStateLabel(state),
    submitFeedback: executionSubmitFeedback(data, state, { degraded, recoverable }),
    degraded,
    recoverable,
    latestEventAt: data?.latestEventAt ?? "",
    gatewayOrderIds: Array.isArray(data?.gatewayOrderIds) ? data.gatewayOrderIds : [],
    compensationState: data?.compensationState ?? null,
    reconciliationState: data?.reconciliationState ?? "",
    blockingReason: data?.blockingReason || null,
    manualInterventionRequired: Boolean(data?.manualInterventionRequired),
    legs,
    fills,
    filledQuantity: parseDecimalString(data?.filledQuantity),
    fees: parseDecimalString(data?.fees?.amount ?? data?.fees),
  };
}

export function adaptPosition(dto) {
  const data = dto?.data && !dto.positionId ? dto.data : dto;
  const longLeg = data?.longLeg ?? {};
  const shortLeg = data?.shortLeg ?? {};
  const pnl = parseDecimalString(data?.unrealizedPnlUsd);
  const status = data?.status ?? data?.state ?? "";
  const state = data?.state ?? status;
  return {
    raw: data,
    id: data?.positionId ?? data?.id ?? "",
    positionId: data?.positionId ?? data?.id ?? "",
    symbol: data?.symbol ?? DASH,
    longVenue: data?.longVenue ?? longLeg.venue ?? DASH,
    shortVenue: data?.shortVenue ?? shortLeg.venue ?? DASH,
    longVenueEnvironment: longLeg.venueEnvironment ?? "",
    shortVenueEnvironment: shortLeg.venueEnvironment ?? "",
    capital: parseDecimalString(data?.capital?.amount ?? data?.capitalUsdt ?? data?.capital),
    amount: parseDecimalString(data?.singleLegNotional?.amount ?? data?.singleLegNotional),
    leverage: parseDecimalString(data?.leverage),
    apr: parseDecimalString(data?.netAprPct ?? data?.expectedNetAprPct),
    pnl,
		reportingAsset: String(data?.reportingAsset ?? "").trim(),
		liveEconomicsStatus: String(data?.liveEconomicsStatus ?? "").trim(),
    delta: parseDecimalString(data?.hedgeDeltaPct),
    buffer: parseDecimalString(data?.liquidationBufferPct),
    settlement: data?.settlement ?? "",
    nextFundingAt: data?.nextFundingAt ?? "",
    nextFundingTime: formatTime(data?.nextFundingAt),
    state,
    status,
    statusLabel: positionStatusLabel(status, state),
    tone: positionStatusTone(status, state),
    warnings: Array.isArray(data?.warnings) ? data.warnings : [],
  };
}

export function adaptPositionList(response) {
  return normalizeBackendList(response, ["items", "positions"]).map(adaptPosition);
}

export function adaptClosedPositionLedger(dto) {
  const data = dto?.data && !dto.positionId ? dto.data : dto;
  const status = normalizeVerificationStatus(data?.verificationStatus);
  const reportingAsset = data?.reportingAsset ?? moneyAsset(
    data?.netPnl,
    data?.realizedTradingPnl,
    data?.actualFundingCashFlow,
  );
  return {
    raw: data,
    id: data?.positionId ?? "",
    positionId: data?.positionId ?? "",
    lineageId: data?.lineageId ?? "",
    principalId: data?.principalId ?? "",
    mode: data?.mode === "test" || data?.mode === "live" ? data.mode : "",
    symbol: data?.symbol ?? DASH,
    longLeg: adaptLedgerLeg(data?.longLeg),
    shortLeg: adaptLedgerLeg(data?.shortLeg),
    openedAt: data?.openedAt ?? "",
    closedAt: data?.closedAt ?? "",
    reportingAsset,
    realizedTradingPnl: ledgerDecimalString(data?.realizedTradingPnl),
    unrealizedTradingPnl: ledgerDecimalString(data?.unrealizedTradingPnl),
    actualFundingCashFlow: ledgerDecimalString(data?.actualFundingCashFlow),
    fees: ledgerDecimalString(data?.fees),
    netPnl: ledgerDecimalString(data?.netPnl),
    closedTradingUpnl: ledgerDecimalString(data?.closedTradingUpnl),
    verificationStatus: status,
    verificationLabel: verificationStatusLabel(status),
    tone: verificationStatusTone(status),
    residual: adaptLedgerResidual(data?.residual),
    watermark: adaptLedgerWatermark(data?.watermark),
    conversionSource: data?.conversionSource ?? data?.conversion?.conversionSource ?? "",
    conversionAsOf: data?.conversionAsOf ?? data?.conversion?.conversionAsOf ?? "",
    roundingMode: data?.roundingMode ?? data?.conversion?.roundingMode ?? "",
    scale: Number.isInteger(data?.scale) ? data.scale : Number.isInteger(data?.conversion?.scale) ? data.conversion.scale : null,
    formulaVersion: data?.formulaVersion ?? data?.conversion?.formulaVersion ?? "",
  };
}

export function adaptClosedPositionLedgerList(response) {
  return normalizeBackendList(response, ["items", "positions"]).map(adaptClosedPositionLedger);
}

export function adaptPortfolioLedger(dto) {
  const data = dto?.data && dto.reportingAsset === undefined ? dto.data : dto;
  const status = normalizeVerificationStatus(data?.verificationStatus);
  const reportingAsset = data?.reportingAsset ?? moneyAsset(
    data?.totalNetPnl,
    data?.cumulativeRealizedNetPnl,
    data?.actualFundingCashFlow,
  );
  return {
    raw: data,
    principalId: data?.principalId ?? "",
    mode: data?.mode === "test" || data?.mode === "live" ? data.mode : "",
    asOf: data?.asOf ?? "",
    reportingAsset,
    cumulativeRealizedNetPnl: ledgerDecimalString(data?.cumulativeRealizedNetPnl),
    currentOpenUnrealizedPnl: ledgerDecimalString(data?.currentOpenUnrealizedPnl),
    totalNetPnl: ledgerDecimalString(data?.totalNetPnl),
		liveEconomicsStatus: String(data?.liveEconomicsStatus ?? "").trim(),
    actualFundingCashFlow: ledgerDecimalString(data?.actualFundingCashFlow),
    fees: ledgerDecimalString(data?.fees),
    openPositionCount: nonNegativeIntegerOrNull(data?.openPositionCount),
    closedPositionCount: nonNegativeIntegerOrNull(data?.closedPositionCount),
    verificationStatus: status,
    verificationLabel: verificationStatusLabel(status),
    tone: verificationStatusTone(status),
    residual: adaptLedgerResidual(data?.residual),
    watermark: adaptLedgerWatermark(data?.watermark),
    conversionSource: data?.conversionSource ?? data?.conversion?.conversionSource ?? "",
    conversionAsOf: data?.conversionAsOf ?? data?.conversion?.conversionAsOf ?? "",
    roundingMode: data?.roundingMode ?? data?.conversion?.roundingMode ?? "",
    scale: Number.isInteger(data?.scale) ? data.scale : Number.isInteger(data?.conversion?.scale) ? data.conversion.scale : null,
    formulaVersion: data?.formulaVersion ?? data?.conversion?.formulaVersion ?? "",
  };
}

export function adaptFundingPaymentPage(dto) {
  const data = dto?.data && !Array.isArray(dto.items) ? dto.data : dto;
  const rawItems = Array.isArray(data?.items) ? data.items : [];
  return {
    items: rawItems.map(adaptFundingPayment),
    nextCursor: data?.nextCursor ?? "",
    scope: data?.scope ?? {},
    start: data?.start ?? data?.scope?.start ?? "",
    end: data?.end ?? data?.scope?.end ?? "",
    paginationTraversal: data?.paginationTraversal ?? data?.scope?.paginationTraversal ?? "",
    status: normalizeCompletenessStatus(data?.status),
    accountId: data?.accountId ?? data?.scope?.accountId ?? "",
    instrumentId: data?.instrumentId ?? data?.scope?.instrumentId ?? "",
    asOf: data?.asOf ?? "",
    completeThrough: data?.completeThrough ?? "",
    revision: data?.revision ?? "",
    watermark: adaptFactsWatermark(data?.watermark),
  };
}

export function adaptVerificationStatus(dto) {
  const data = dto?.data && dto.status === undefined ? dto.data : dto;
  const status = normalizeVerificationStatus(data?.status);
  return {
    status,
    label: verificationStatusLabel(status),
    tone: verificationStatusTone(status),
    reason: data?.reason ?? "",
    details: data?.details ?? "",
    watermark: adaptLedgerWatermark(data?.watermark),
    openReady: data?.openReady === true,
    closeReady: data?.closeReady === true,
    recoveryReady: data?.recoveryReady === true,
  };
}

export function verificationStatusLabel(status) {
  switch (status) {
    case "verified": return "已验证";
    case "revised": return "已修订";
    case "provisional": return "暂定";
    case "incomplete": return "不完整";
    case "temporarily_unavailable": return "暂不可用";
    case "unsupported": return "不支持";
    case "blocked": return "已阻断";
    default: return "未知";
  }
}

export function verificationStatusTone(status) {
  if (status === "verified") return "positive";
  if (status === "blocked") return "danger";
  if (status === "unsupported") return "neutral";
  return status ? "warning" : "neutral";
}

export function adaptActivity(dto) {
  const data = dto?.data && !dto.activityId ? dto.data : dto;
  const value = parseDecimalString(data?.amountUsd);
  const status = data?.status ?? data?.state ?? "";
  const state = data?.state ?? status;
  const occurredAt = data?.time ?? data?.occurredAt;
  const activityId = data?.activityId ?? data?.id ?? [
    data?.executionId,
    data?.positionId,
    data?.kind,
    occurredAt,
  ].filter(Boolean).join(":");
  return {
    raw: data,
    id: activityId,
    activityId,
    time: formatTime(occurredAt),
    kind: data?.kind ?? "",
    type: activityTypeLabel(data?.kind, data?.operation, data),
    symbol: data?.symbol ?? DASH,
    route: data?.route ?? DASH,
    result: data?.message ?? DASH,
    message: data?.message ?? "",
    value,
    amountUsd: value,
    status: positionStatusLabel(status, state),
    state,
    tone: positionStatusTone(status, state),
    executionId: data?.executionId ?? "",
  };
}

export function adaptActivityList(response) {
  return normalizeBackendList(response, ["items", "activities"]).map(adaptActivity);
}

export function adaptAuditEvent(dto) {
  const data = dto?.data && !dto.auditId ? dto.data : dto;
  return {
    raw: data,
    id: data?.auditId ?? data?.id ?? "",
    auditId: data?.auditId ?? data?.id ?? "",
    time: formatTime(data?.time ?? data?.occurredAt),
    principalId: data?.principalId ?? "",
    kind: data?.kind ?? "",
    targetId: data?.targetId ?? "",
    outcome: data?.outcome ?? "",
    requestId: data?.requestId ?? "",
    executionId: data?.executionId ?? "",
    tone: data?.outcome === "success" || data?.outcome === "allowed" ? "positive" : data?.outcome === "blocked" || data?.outcome === "denied" ? "danger" : "neutral",
  };
}

export function adaptAuditEventList(response) {
  return normalizeBackendList(response, ["items", "auditEvents"]).map(adaptAuditEvent);
}

export function adaptSystemStatus(dto) {
  const data = dto?.data && !dto.mode ? dto.data : dto;
  const mode = data?.mode === "test" || data?.mode === "live" ? data.mode : "";
  return {
    mode,
    modeLabel: mode ? mode.toUpperCase() : DASH,
    ready: Boolean(data?.ready),
    build: data?.build ?? "",
    version: data?.version ?? "",
    maintenance: Boolean(data?.maintenance),
    maintenanceReason: data?.maintenanceReason ?? "",
  };
}

export function mapExecutionStateTone(state) {
  switch (state) {
    case "open":
    case "completed":
      return "positive";
    case "partiallyFilled":
    case "partiallyHedged":
    case "compensating":
      return "warning";
    case "uncertain":
    case "manualInterventionRequired":
    case "rejected":
    case "failed":
      return "danger";
    default:
      return "neutral";
  }
}

export function mapBlockerTone(blocker) {
  const code = typeof blocker === "string" ? blocker : blocker?.code;
  if (!code) return "warning";
  return String(code).includes("unverifiable") || String(code).includes("blocked") ? "danger" : "warning";
}

function findingText(finding) {
  if (typeof finding === "string") return finding;
  return finding?.message || finding?.code || "";
}

function reasonFromFinding(finding) {
  return { text: findingText(finding) || DASH, state: mapBlockerTone(finding) };
}

function executionStateLabel(state) {
  switch (state) {
    case "open":
      return "已对冲";
    case "partiallyFilled":
      return "部分成交";
    case "partiallyHedged":
      return "部分对冲";
    case "compensating":
      return "补偿中";
    case "uncertain":
      return "结果待确认";
    case "manualInterventionRequired":
      return "需要人工处理";
    case "completed":
      return "已完成";
    case "rejected":
      return "已拒绝";
    case "failed":
      return "已失败";
    default:
      return displayOrDash(state);
  }
}

function executionSubmitFeedback(data, state, { degraded = false, recoverable = false } = {}) {
  const executionId = data?.executionId ?? data?.id ?? "accepted";
  const reason = data?.blockingReason || data?.lastError?.message || data?.error?.message || "";
  const suffix = reason ? ` · ${reason}` : "";
  if (recoverable) {
    return {
      accepted: true,
      tone: "warning",
      message: `订单已接受，正在恢复：${executionId}`,
      toastMessage: "后端订单已接受，正在恢复",
    };
  }
  if (degraded) {
    return {
      accepted: true,
      tone: "warning",
      message: `订单已接受，服务降级：${executionId}`,
      toastMessage: "后端订单已接受，服务当前降级",
    };
  }
  switch (state) {
    case "rejected":
      return {
        accepted: false,
        tone: "danger",
        message: `订单已拒绝：${executionId}${suffix}`,
        toastMessage: "后端订单已拒绝",
      };
    case "failed":
      return {
        accepted: false,
        tone: "danger",
        message: `订单执行失败：${executionId}${suffix}`,
        toastMessage: "后端订单执行失败",
      };
    case "manualInterventionRequired":
      return {
        accepted: false,
        tone: "danger",
        message: `订单需要人工处理：${executionId}${suffix}`,
        toastMessage: "后端订单需要人工处理",
      };
    case "uncertain":
      return {
        accepted: false,
        tone: "warning",
        message: `订单结果待确认：${executionId}${suffix}`,
        toastMessage: "后端订单结果待确认",
      };
    default:
      return {
        accepted: true,
        tone: "positive",
        message: `订单已提交：${executionId}`,
        toastMessage: "后端订单已接受",
      };
  }
}

function activityTypeLabel(kind, operation, data = {}) {
  if (kind === "position.closed" && operation === "open") return "开仓回滚";
  if (kind === "position.attention_required" && data?.manualInterventionRequired !== true && data?.state !== "manualInterventionRequired" && data?.status !== "manualInterventionRequired") {
    return operation === "close" ? "平仓" : operation === "open" ? "开仓" : displayOrDash(operation || kind);
  }
  switch (kind) {
    case "open":
    case "execution.open":
    case "position.opened":
      return "开仓";
    case "close":
    case "execution.close":
    case "position.closed":
      return "平仓";
    case "position.attention_required":
      return "需人工关注";
    case "position.updated":
      return "持仓更新";
    default:
      return displayOrDash(kind);
  }
}

function positionStatusLabel(status, state) {
  switch (status) {
    case "open":
      return "持仓中";
    case "attention":
      return "需关注";
    case "closed":
      return "已完成";
    default:
      return executionStateLabel(state);
  }
}

function positionStatusTone(status, state) {
  switch (status) {
    case "open":
    case "closed":
      return "positive";
    case "attention":
      return "warning";
    default:
      return mapExecutionStateTone(state);
  }
}

function adaptFundingPayment(data = {}) {
  return {
    paymentId: data?.paymentId ?? "",
    factId: data?.factId ?? "",
    supersedesFactId: data?.supersedesFactId ?? "",
    accountId: data?.accountId ?? "",
    instrumentId: data?.instrumentId ?? "",
    venue: data?.venue ?? "",
    venueEnvironment: data?.venueEnvironment ?? "",
    product: data?.product ?? "",
    settlementAsset: data?.settlementAsset ?? data?.payment?.asset ?? "",
    amount: ledgerDecimalString(data?.amount ?? data?.payment),
    effectiveAt: data?.effectiveAt ?? data?.provenance?.effectiveAt ?? "",
    observedAt: data?.observedAt ?? data?.provenance?.observedAt ?? "",
    revision: data?.revision ?? data?.provenance?.sourceRevision ?? "",
    version: data?.version ?? data?.provenance?.sourceVersion ?? "",
    sourceRef: data?.sourceRef ?? data?.provenance?.sourceRef ?? "",
    formulaVersion: data?.formulaVersion ?? "",
  };
}

function adaptLedgerLeg(leg = {}) {
  return {
    accountId: leg?.accountId ?? "",
    venue: leg?.venue ?? "",
    symbol: leg?.symbol ?? "",
    instrumentId: leg?.instrumentId ?? "",
    venueEnvironment: leg?.venueEnvironment ?? "",
  };
}

function adaptLedgerResidual(residual = {}) {
  return {
    legQuantity: ledgerDecimalString(residual?.legQuantity),
    reportingAssetNotional: ledgerDecimalString(residual?.reportingAssetNotional),
    openOrderCount: nonNegativeIntegerOrNull(residual?.openOrderCount),
    pendingCompensation: nonNegativeIntegerOrNull(residual?.pendingCompensation),
    blockerClass: residual?.blockerClass ?? "",
  };
}

function adaptLedgerWatermark(watermark = {}) {
  return {
    status: normalizeCompletenessStatus(watermark?.status),
    verified: watermark?.verified === true,
    completeThrough: watermark?.completeThrough ?? "",
  };
}

function adaptFactsWatermark(watermark = {}) {
  return {
    cursor: watermark?.cursor ?? "",
    revision: watermark?.revision ?? "",
    observedAt: watermark?.observedAt ?? "",
    effectiveThrough: watermark?.effectiveThrough ?? "",
    completeThrough: watermark?.completeThrough ?? "",
    verified: watermark?.verified === true,
    status: normalizeCompletenessStatus(watermark?.status),
  };
}

function normalizeVerificationStatus(value) {
  const status = String(value ?? "").trim();
  return VERIFICATION_STATUSES.has(status) ? status : "";
}

function normalizeCompletenessStatus(value) {
  const status = String(value ?? "").trim();
  return ["unsupported", "temporarily_unavailable", "incomplete", "complete_empty", "complete"].includes(status)
    ? status
    : "";
}

function nonNegativeIntegerOrNull(value) {
  return Number.isInteger(value) && value >= 0 ? value : null;
}

function moneyAsset(...values) {
  for (const value of values) {
    if (value && typeof value === "object" && typeof value.asset === "string" && value.asset.trim()) {
      return value.asset.trim();
    }
  }
  return "";
}
