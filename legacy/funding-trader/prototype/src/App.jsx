"use client";

import { useCallback, useEffect, useLayoutEffect, useMemo, useRef, useState } from "react";
import { LoginScreen } from "./AccessViews";
import { ApiError, apiClient, generateIdempotencyKey } from "./apiClient";
import { isLedgerInvalidationEvent, loadDashboardSnapshots } from "./dashboardData";
import { canCommitAfterApiError, createAuthSnapshotGuard, clearPrincipalRuntimeState, emptyPrincipalBusinessState, mergePositionUpsert, realtimeSnapshotFields } from "./dashboardState";
import {
  PRIVATE_V1_CONTRACT_VERSION,
  createRealtimeController,
  realtimeDisplayStatus,
  validateRealtimeEnvelope,
} from "./realtime";
import {
  adaptActivity,
  adaptActivityList,
  adaptAlert,
  adaptAlertList,
  adaptAuditEvent,
  adaptAuditEventList,
  adaptClosedPositionLedgerList,
  adaptExecution,
  adaptExecutionPreview,
  adaptOpportunity,
  adaptOpportunityList,
  adaptPortfolioLedger,
  adaptPosition,
  adaptPositionList,
  adaptSystemStatus,
  adaptTradingReadiness,
  adaptVerificationStatus,
  executionActionGate,
  formatLedgerMoney,
  isOpportunityWriteReady,
  operationReadinessGate,
  opportunityWriteStatus,
  plannedLegNotionals,
} from "./adapters";

const THEME_STORAGE_KEY = "funding-arb-theme";
const DASH = "—";
const MAX_CAPITAL = 10000;
const DEFAULT_CAPITAL = "5000";
const stepLabels = ["参数设置", "预览与检查"];
const NAV_TARGETS = {
  机会: "#opportunities",
  账本: "#ledger",
  持仓: "#positions",
  执行: "#executions",
  活动: "#activity",
  审计: "#audit",
};

function unwrap(response) {
  if (!response) return response;
  if (Object.prototype.hasOwnProperty.call(response, "data")) return response.data;
  return response;
}

function requireAuthenticatedPrincipal(data) {
  const principal = data?.principal;
  if (!principal?.principalId || (principal.mode !== "test" && principal.mode !== "live")) {
    throw new Error("后端认证响应缺少有效 principal");
  }
  return principal;
}

function idOf(record, keys) {
  for (const key of keys) {
    if (record?.[key] != null && String(record[key]).trim() !== "") return String(record[key]);
  }
  return "";
}

function replaceById(current, next, keys) {
  if (!next) return current;
  const nextId = idOf(next, keys);
  if (!nextId) return current;
  const index = current.findIndex((item) => idOf(item, keys) === nextId);
  if (index < 0) return [next, ...current];
  return current.map((item, itemIndex) => (itemIndex === index ? next : item));
}

function formatNumber(value, digits = 0) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return DASH;
  return new Intl.NumberFormat("en-US", {
    minimumFractionDigits: digits,
    maximumFractionDigits: digits,
  }).format(numeric);
}

function formatMoney(value, digits = 2) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return DASH;
  return `$${formatNumber(numeric, digits)}`;
}

function formatSignedMoney(value) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return DASH;
  return `${numeric >= 0 ? "+" : "−"}$${formatNumber(Math.abs(numeric), 2)}`;
}

function formatPercent(value, digits = 1) {
  const numeric = Number(value);
  if (!Number.isFinite(numeric)) return DASH;
  return `${numeric >= 0 ? "+" : "−"}${Math.abs(numeric).toFixed(digits)}%`;
}

function formatFundingRate(value) {
  return formatPercent(value, 4);
}

function qualifiedVenue(venue, venueEnvironment) {
  const venueLabel = venue || DASH;
  const environmentLabel = String(venueEnvironment || "").trim().toUpperCase();
  return environmentLabel ? `${venueLabel} · ${environmentLabel}` : venueLabel;
}

function readableError(error, fallback = "请求失败") {
  if (error instanceof ApiError) return error.message || fallback;
  if (error?.message) return error.message;
  return fallback;
}

function isSessionError(error) {
  return error instanceof ApiError && (error.status === 401 || error.code === "unauthenticated");
}

function isAbortError(error) {
  return error?.name === "AbortError" || (error instanceof ApiError && error.code === "network_error" && /abort/i.test(error.message ?? ""));
}

function StatusTag({ tone = "neutral", children }) {
  return <span className={`status-tag status-tag--${tone}`}>{children}</span>;
}

function Sparkline({ values = [], color = "#52ecf2", height = 30 }) {
  const canvasRef = useRef(null);
  const cleanValues = values.map(Number).filter(Number.isFinite);

  useEffect(() => {
    const canvas = canvasRef.current;
    if (!canvas || cleanValues.length < 2) return undefined;

    let frameId;
    const draw = () => {
      const rect = canvas.getBoundingClientRect();
      if (rect.width === 0 || rect.height === 0) return;

      const ratio = window.devicePixelRatio || 1;
      canvas.width = Math.max(1, Math.round(rect.width * ratio));
      canvas.height = Math.max(1, Math.round(rect.height * ratio));
      const context = canvas.getContext("2d");
      context.setTransform(ratio, 0, 0, ratio, 0, 0);
      context.clearRect(0, 0, rect.width, rect.height);

      const minimum = Math.min(...cleanValues);
      const maximum = Math.max(...cleanValues);
      const range = maximum - minimum || 1;
      const horizontalPadding = 2;
      const verticalPadding = 4;

      context.beginPath();
      context.strokeStyle = "rgba(82, 236, 242, 0.08)";
      context.lineWidth = 1;
      context.moveTo(0, rect.height - 1);
      context.lineTo(rect.width, rect.height - 1);
      context.stroke();

      context.beginPath();
      cleanValues.forEach((value, index) => {
        const x = horizontalPadding + (index / (cleanValues.length - 1)) * (rect.width - horizontalPadding * 2);
        const y = rect.height - verticalPadding - ((value - minimum) / range) * (rect.height - verticalPadding * 2);
        if (index === 0) context.moveTo(x, y);
        else context.lineTo(x, y);
      });
      context.strokeStyle = color;
      context.lineWidth = 1.35;
      context.lineJoin = "round";
      context.lineCap = "round";
      context.stroke();
    };

    const scheduleDraw = () => {
      window.cancelAnimationFrame(frameId);
      frameId = window.requestAnimationFrame(draw);
    };
    const resizeObserver = new ResizeObserver(scheduleDraw);
    resizeObserver.observe(canvas);
    scheduleDraw();

    return () => {
      resizeObserver.disconnect();
      window.cancelAnimationFrame(frameId);
    };
  }, [cleanValues, color, height]);

  return <canvas ref={canvasRef} className="sparkline" style={{ height }} aria-label="资金费率趋势图" />;
}

function Header({ activeNav, onNavigate, onMode, theme, onToggleTheme, currentUser, menuOpen, onToggleMenu, onLogout, systemStatus, realtimeStatus }) {
  const identity = currentUser?.email ?? currentUser?.principalId ?? "已认证用户";
  const productMode = currentUser?.mode ? String(currentUser.mode).toUpperCase() : systemStatus?.modeLabel ?? "authenticated";
  return (
    <header className="topbar">
      <button className="wordmark" onClick={() => onNavigate("机会")} aria-label="前往机会列表">
        FUNDING <span>/</span> ARB
      </button>
      <nav className="primary-nav" aria-label="主导航">
        {["机会", "账本", "持仓", "执行", "活动", "审计"].map((item) => (
          <button key={item} className={activeNav === item ? "is-active" : ""} onClick={() => onNavigate(item)}>
            {item}
          </button>
        ))}
      </nav>
      <div className="topbar__meta">
        <StatusTag tone={systemStatus?.ready ? "positive" : "warning"}>{systemStatus?.ready ? "系统就绪" : "系统检查"}</StatusTag>
        <span className="latency mono">{realtimeStatus ?? "stream disconnected"}</span>
        <button className="theme-button" type="button" onClick={onToggleTheme} aria-label={`切换为${theme === "dark" ? "浅色" : "深色"}主题`}>
          {theme === "dark" ? "浅色主题" : "深色主题"}
        </button>
        <button className="mode-button" onClick={onMode}>{systemStatus?.modeLabel ?? "模式未知"}</button>
        <div className="account-menu">
          <button className={`account-button ${menuOpen ? "is-open" : ""}`} type="button" onClick={onToggleMenu} aria-expanded={menuOpen}>
            <span>{productMode}</span>
            <strong>{identity}</strong>
          </button>
          {menuOpen && (
            <div className="account-popover" role="menu">
              <div className="account-popover__identity">
                <span>当前账号</span>
                <strong>{identity}</strong>
                <small>{currentUser?.principalId ?? "single-user session"}</small>
              </div>
              <button type="button" role="menuitem" onClick={onLogout}>退出登录</button>
            </div>
          )}
        </div>
      </div>
    </header>
  );
}

function StateBanner({ state, error, onRetry }) {
  if (state === "loading") {
    return <div className="app-state app-state--info" role="status">正在载入后端快照…</div>;
  }
  if (state === "expired") {
    return <div className="app-state app-state--danger" role="alert">会话已过期，请重新登录。</div>;
  }
  if (!error) return null;
  return (
    <div className="app-state app-state--danger" role="alert">
      <span>{error}</span>
      <button className="text-button" type="button" onClick={onRetry}>重试</button>
    </div>
  );
}

function FundingAlertList({ alerts, filter, onFilterChange, onOpenOpportunity, locked }) {
  const tabs = [
    { id: "all", label: "全部" },
    { id: "critical", label: "严重" },
    { id: "warning", label: "警告" },
    { id: "watch", label: "观察" },
  ];
  const visibleAlerts = filter === "all" ? alerts : alerts.filter((alert) => alert.severity === filter);

  return (
    <section className="panel funding-alert-panel" aria-labelledby="funding-alerts-heading">
      <div className="panel-heading funding-alert-heading">
        <div>
          <div className="eyebrow">TOKEN × VENUE</div>
          <h2 id="funding-alerts-heading">资金费率预警</h2>
        </div>
        <div className="header-metric funding-alert-count">
          <span>触发中</span>
          <strong className="mono">{alerts.length}</strong>
        </div>
      </div>
      <div className="funding-alert-tabs" role="tablist" aria-label="按预警级别筛选">
        {tabs.map((tab) => {
          const count = tab.id === "all" ? alerts.length : alerts.filter((alert) => alert.severity === tab.id).length;
          return (
            <button type="button" role="tab" key={tab.id} aria-selected={filter === tab.id} onClick={() => onFilterChange(tab.id)}>
              {tab.label}<span className="mono">{count}</span>
            </button>
          );
        })}
      </div>
      <ul className="funding-alert-list" aria-label="按代币和交易所监控的资金费率预警">
        {visibleAlerts.map((alert) => (
          <li key={alert.id} className={`funding-alert-item funding-alert-item--${alert.tone ?? "info"}`}>
            <button type="button" onClick={() => onOpenOpportunity(alert)} disabled={locked || !alert.opportunityId}>
              <span className="funding-alert-item__top">
                <span className="funding-alert-market"><strong>{alert.token ?? alert.symbol ?? DASH}</strong><small>{alert.exchange ?? alert.venue ?? DASH}</small></span>
                <StatusTag tone={alert.tone ?? "info"}>{alert.severityLabel ?? alert.severity ?? "预警"}</StatusTag>
              </span>
              <span className="funding-alert-item__rate">
                <span>当前费率</span>
                <strong className={`mono ${Number(alert.currentRate) >= 0 ? "positive-text" : "danger-text"}`}>{formatFundingRate(alert.currentRate)}</strong>
              </span>
              <span className="funding-alert-item__meta mono">
                <span>阈值 {alert.thresholdLabel ?? DASH}</span>
                <span>{alert.change == null ? DASH : `3期 ${formatFundingRate(alert.change)}`}</span>
              </span>
              <span className="funding-alert-item__settlement"><span>下次结算</span><strong className="mono">{alert.nextFundingTime ?? DASH}</strong></span>
            </button>
          </li>
        ))}
        {visibleAlerts.length === 0 && <li className="funding-alert-empty">该级别暂无预警</li>}
      </ul>
      <div className="table-footnote funding-alert-footnote">点击预警定位对应套利</div>
    </section>
  );
}

function OpportunityTable({
  rows,
  venueOptions,
  selectedId,
  selectedPreviewCanSubmit,
  onOpen,
  filters,
  setFilters,
  refreshing,
  onRefresh,
  locked,
  writesReady,
  writeBlockReason,
}) {
  return (
    <section className="panel opportunities-panel" id="opportunities" aria-labelledby="opportunities-heading">
      <div className="panel-heading opportunity-heading">
        <div>
          <div className="eyebrow">实时扫描</div>
          <h1 id="opportunities-heading">套利机会</h1>
        </div>
        <div className="header-metric">
          <span>机会数</span>
          <strong className="mono">{rows.length}</strong>
        </div>
      </div>
      <div className="filterbar" aria-label="机会筛选">
        <label>
          <span>交易场所</span>
          <select value={filters.venue} onChange={(event) => setFilters((current) => ({ ...current, venue: event.target.value }))} disabled={locked}>
            <option value="all">全部场所</option>
            {venueOptions.map((venue) => <option key={venue.id} value={venue.id}>{venue.label}</option>)}
          </select>
        </label>
        <label className="sort-control">
          <span>排序</span>
          <select value={filters.sort} onChange={(event) => setFilters((current) => ({ ...current, sort: event.target.value }))} disabled={locked}>
            <option value="desc">净 APR 从高到低</option>
            <option value="asc">净 APR 从低到高</option>
          </select>
        </label>
        <div className="filter-note">
          <span>统一口径</span>
          <strong>后端快照 · 不补齐缺失字段</strong>
        </div>
        <button className="quiet-button refresh-button" onClick={onRefresh} disabled={refreshing || locked}>
          {refreshing ? "刷新中…" : "刷新数据"}
        </button>
      </div>
      <div className="opportunity-table" role="table" aria-label="按净 APR 排序的套利机会">
        <div className="opportunity-grid opportunity-grid--header" role="row">
          <div role="columnheader">交易对 / 路径</div>
          <div role="columnheader">净 APR</div>
          <div role="columnheader">稳定性</div>
          <div role="columnheader">可用容量</div>
          <div role="columnheader">费率状态</div>
          <div role="columnheader">费率趋势</div>
          <div role="columnheader">下次结算</div>
        </div>
        <div className="opportunity-table__body" role="rowgroup">
          {rows.map((opportunity) => {
            const writeStatus = opportunityWriteStatus(opportunity, writesReady, {
              globalWriteBlockReason: writeBlockReason,
              previewCanSubmit: selectedId === opportunity.id && selectedPreviewCanSubmit === true,
            });
            return (
              <div
                key={opportunity.id}
                className={`opportunity-grid opportunity-row ${selectedId === opportunity.id ? "is-selected" : ""} ${locked ? "is-locked" : ""}`}
                role="row"
                aria-selected={selectedId === opportunity.id}
                aria-disabled={locked}
                onClick={() => !locked && onOpen(opportunity.id)}
              >
                <div className="pair-cell" role="cell">
                  <button
                    type="button"
                    className="pair-action"
                    disabled={locked}
                    onClick={(event) => {
                      event.stopPropagation();
                      onOpen(opportunity.id);
                    }}
                  >
                    <strong>{opportunity.symbol ?? DASH}</strong>
                    <span>{qualifiedVenue(opportunity.longVenue, opportunity.longVenueEnvironment)} 多 · {qualifiedVenue(opportunity.shortVenue, opportunity.shortVenueEnvironment)} 空</span>
                    <span className={`pair-connection pair-connection--${writeStatus.ready ? "ready" : "missing"}`}>
                      {writeStatus.reason}
                    </span>
                  </button>
                </div>
                <div className="apr-cell mono" role="cell"><strong>{formatPercent(opportunity.netApr)}</strong><span>后端净值</span></div>
                <div className="stability-cell mono" role="cell">
                  <strong>{opportunity.positiveSettlements ?? DASH}/{opportunity.validSettlements ?? DASH}</strong>
                  <span>{opportunity.stability == null ? DASH : `${opportunity.stability}% 为正`}</span>
                  <div className="meter"><i style={{ width: `${Math.max(0, Math.min(100, Number(opportunity.stability) || 0))}%` }} /></div>
                </div>
                <div className="mono capacity-cell" role="cell"><strong>{formatMoney(opportunity.capacity, 0)}</strong><span>后端容量</span></div>
                <div role="cell"><StatusTag tone={opportunity.fundingTone ?? "neutral"}>{opportunity.fundingState ?? DASH}</StatusTag></div>
                <div role="cell">{Array.isArray(opportunity.spark) && opportunity.spark.length > 1 ? <Sparkline values={opportunity.spark} /> : <span className="mono">{DASH}</span>}</div>
                <div className="mono settlement-cell" role="cell"><strong>{opportunity.nextFundingTime ?? DASH}</strong><span>UTC 结算</span></div>
              </div>
            );
          })}
          {rows.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={7}>当前筛选条件下暂无机会。</div></div>}
        </div>
      </div>
      <div className="table-footnote">
        <span>点击机会查看后端返回详情</span>
        <span className="mono">SSE 自动更新 · 需要重取时会重新载入快照</span>
      </div>
    </section>
  );
}

function previewMetrics(preview, amount) {
  const capital = Number(amount);
  const legs = preview?.plannedLegs ?? [];
  const legNotionals = plannedLegNotionals(preview);
  const fees = Number(preview?.estimatedFeesUsd);
  const slippage = Number(preview?.estimatedSlippageUsd);
  const net = Number(preview?.expectedNetPnL7dUsd);
  const apr = Number(preview?.expectedNetAprPct);
  const blockers = preview?.blockers ?? [];
  const warnings = preview?.warnings ?? [];
  const status = blockers.length > 0
    ? { key: "blocked", tone: "danger", label: "已阻断", reasons: blockers.map((item) => ({ text: item.message ?? item.code, state: "danger" })) }
    : warnings.length > 0
      ? { key: "warning", tone: "warning", label: "可提交", reasons: warnings.map((item) => ({ text: item.message ?? item.code, state: "warning" })) }
      : { key: "passed", tone: "positive", label: "可提交", reasons: [{ text: "后端预览允许提交", state: "positive" }] };
  return {
    capital,
    legNotionals,
    fees,
    slippage,
    netProfit: net,
    expectedApr: apr,
    status,
    checks: [
      { label: "预览编号", value: preview?.previewId ?? DASH, state: preview?.previewId ? "positive" : "warning" },
      { label: "后端可提交", value: preview?.canSubmit ? "true" : "false", state: preview?.canSubmit ? "positive" : "danger" },
      { label: "计划腿数", value: String(legs.length), state: legs.length >= 2 ? "positive" : "warning" },
    ],
  };
}

function ExecutionPanel({
  opportunity,
  amount,
  onAmountChange,
  leverage,
  onLeverageChange,
  step,
  setStep,
  onNext,
  executionStatus,
  preview,
  submitting,
  writesReady,
  writeBlockReason,
  writeStatus,
}) {
  const metrics = previewMetrics(preview, amount);
  const displayWriteStatus = writeStatus ?? { ready: writesReady === true, reason: writeBlockReason || (writesReady ? "两腿写入就绪" : "后端当前禁止写入"), tone: writesReady ? "ready" : "blocked" };
  const interactionLocked = submitting || !opportunity;
  const actionGate = executionActionGate({
    step,
    hasOpportunity: Boolean(opportunity),
    submitting,
    writesReady,
    previewCanSubmit: preview?.canSubmit && metrics.status.key !== "blocked",
  });

  return (
    <aside className="panel execution-panel" aria-labelledby="execution-heading">
      <div className="panel-heading execution-heading">
        <div>
          <div className="eyebrow">两步执行</div>
          <h2 id="execution-heading">执行工作台</h2>
        </div>
        <StatusTag tone={displayWriteStatus.ready ? "positive" : "warning"}>{displayWriteStatus.ready ? "可写入" : displayWriteStatus.tone === "blocked" ? "不可写" : "待校验"}</StatusTag>
      </div>
      <div className="execution-scroll">
        <div className="selected-route">
          <div><span>当前机会</span><strong>{opportunity?.symbol ?? DASH}</strong></div>
          <div className="route-apr mono"><span>后端净 APR</span><strong>{formatPercent(opportunity?.netApr)}</strong></div>
        </div>
        <div className="legs-grid">
          <div className="leg-card"><span className="leg-label leg-label--long">多头腿</span><strong>{qualifiedVenue(opportunity?.longVenue, opportunity?.longVenueEnvironment)}</strong><small>由后端预览确定</small></div>
          <div className="leg-card"><span className="leg-label leg-label--short">空头腿</span><strong>{qualifiedVenue(opportunity?.shortVenue, opportunity?.shortVenueEnvironment)}</strong><small>由后端预览确定</small></div>
        </div>
        <div className={`access-gate ${displayWriteStatus.ready ? "access-gate--ready" : "access-gate--blocked"}`}>
          <div><span>交易权限检查</span><strong>{displayWriteStatus.reason}</strong></div>
          <StatusTag tone={displayWriteStatus.ready ? "positive" : "warning"}>{displayWriteStatus.ready ? "可开单" : displayWriteStatus.tone === "blocked" ? "不可写" : "需预览"}</StatusTag>
        </div>
        <div className="form-section">
          <label className="field-label" htmlFor="capital">总投入资金</label>
          <div className="amount-input">
            <span>$</span>
            <input id="capital" type="number" min="100" max={MAX_CAPITAL} step="100" value={amount} onChange={(event) => onAmountChange(event.target.value)} disabled={interactionLocked} />
            <button onClick={() => onAmountChange(String(MAX_CAPITAL))} disabled={interactionLocked}>MAX</button>
          </div>
          <div className="quick-values">
            {["1000", "5000", "10000"].map((value) => <button key={value} onClick={() => onAmountChange(value)} disabled={interactionLocked}>${formatNumber(Number(value))}</button>)}
          </div>
        </div>
        <div className="form-section inline-form-section">
          <div>
            <span className="field-label">杠杆</span>
            <div className="segmented" aria-label="杠杆倍数">
              {[1, 2].map((value) => (
                <button key={value} className={leverage === value ? "is-active" : ""} onClick={() => onLeverageChange(value)} aria-pressed={leverage === value} disabled={interactionLocked}>{value}x</button>
              ))}
            </div>
          </div>
          <div><span className="field-label">最大滑点</span><div className="readonly-control mono"><strong>{DASH}</strong><span>以后端预览为准</span></div></div>
        </div>
        <div className="estimate-block">
          <div><span>预计 7 日毛收入</span><strong className="mono">{DASH}</strong></div>
          <div><span>预估开平仓手续费</span><strong className="mono">{formatMoney(metrics.fees)}</strong></div>
          <div><span>预估滑点成本</span><strong className="mono">{formatMoney(metrics.slippage)}</strong></div>
          <div className="estimate-total"><span>预计 7 日净收益</span><strong className="mono">{formatSignedMoney(metrics.netProfit)}</strong></div>
        </div>
        {step === 1 && (
          <>
            <div className="order-preview">
              <div><span>总投入资金</span><strong className="mono">{formatMoney(metrics.capital, 0)}</strong></div>
              <div><span>计划腿 1 名义</span><strong className="mono">{formatLedgerMoney(metrics.legNotionals[0], "USD")}</strong></div>
              <div><span>计划腿 2 名义</span><strong className="mono">{formatLedgerMoney(metrics.legNotionals[1], "USD")}</strong></div>
              <div><span>预览净 APR</span><strong className="mono">{formatPercent(metrics.expectedApr, 3)}</strong></div>
            </div>
            <div className={`execution-check execution-check--${metrics.status.tone}`} role="status" aria-live="polite">
              <div className="execution-check__heading">
                <div><span>执行条件检查</span><strong>{metrics.status.key === "passed" ? "后端允许提交" : `${metrics.status.reasons.length} 项需要处理`}</strong></div>
                <StatusTag tone={metrics.status.tone}>{metrics.status.label}</StatusTag>
              </div>
              <ul className="execution-reasons">{metrics.status.reasons.map((reason) => <li key={reason.text} className={`execution-reason--${reason.state}`}>{reason.text}</li>)}</ul>
              <div className="check-list">{metrics.checks.map((check) => <div key={check.label} className={`check-row check-row--${check.state}`}><span>{check.label}</span><strong className="mono">{check.value}</strong></div>)}</div>
              <p>提交判断、费率、手续费、滑点和计划腿由后端预览返回；前端不补算缺失指标。</p>
            </div>
          </>
        )}
        <div className="execution-steps" aria-label="执行进度">
          {stepLabels.map((label, index) => <div key={label} className={index <= step ? "is-active" : ""}><span className="mono">0{index + 1}</span><small>{label}</small></div>)}
        </div>
        {executionStatus && <div className={`execution-status execution-status--${executionStatus.tone}`} role="status">{executionStatus.message}</div>}
        <div className="execution-actions">
          {step > 0 && <button className="secondary-button" onClick={() => setStep(0)} disabled={submitting}>返回修改</button>}
          <button className="primary-button" onClick={onNext} disabled={actionGate.disabled}>{actionGate.label}</button>
        </div>
      </div>
    </aside>
  );
}

function LedgerOverviewPanel({ portfolio, verification, tradingReadiness }) {
  const asset = portfolio?.reportingAsset ?? "";
  return (
    <section className="panel ledger-panel" id="ledger" aria-labelledby="ledger-heading">
      <div className="panel-heading ledger-heading">
        <div><div className="eyebrow">AUTHORITATIVE ECONOMICS</div><h2 id="ledger-heading" tabIndex={-1}>组合经济账本</h2></div>
        <div className="ledger-status-stack">
          <StatusTag tone={verification?.tone ?? "neutral"}>{verification?.label ?? "验证未知"}</StatusTag>
          <span className="mono">{verification?.reason || "no-verification-reason"}</span>
        </div>
      </div>
      <div className="ledger-overview">
        <div className="ledger-metrics" aria-label="组合经济指标">
          <LedgerMetric label="总净收益" value={formatLedgerMoney(portfolio?.totalNetPnl, asset, { signed: true })} />
          <LedgerMetric label="已实现净收益" value={formatLedgerMoney(portfolio?.cumulativeRealizedNetPnl, asset, { signed: true })} />
          <LedgerMetric label="当前未实现收益" value={formatLedgerMoney(portfolio?.currentOpenUnrealizedPnl, asset, { signed: true })} />
          <LedgerMetric label="实际资金费" value={formatLedgerMoney(portfolio?.actualFundingCashFlow, asset, { signed: true })} />
          <LedgerMetric label="手续费 / 返佣" value={formatLedgerMoney(portfolio?.fees, asset, { signed: true })} />
          <LedgerMetric label="开仓 / 已平" value={portfolio ? `${portfolio.openPositionCount ?? DASH} / ${portfolio.closedPositionCount ?? DASH}` : DASH} />
        </div>
        <div className="ledger-proof-grid">
          <div><span>组合状态</span><StatusTag tone={portfolio?.tone ?? "neutral"}>{portfolio?.verificationLabel ?? "暂无账本"}</StatusTag></div>
          <div><span>组合完整水位</span><strong className="mono">{portfolio?.watermark?.completeThrough || DASH}</strong></div>
          <div><span>全局验证水位</span><strong className="mono">{verification?.watermark?.completeThrough || DASH}</strong></div>
          <div><span>账本时间</span><strong className="mono">{portfolio?.asOf || DASH}</strong></div>
          <div><span>转换证据</span><strong className="mono">{portfolio?.conversionSource || DASH} · {portfolio?.conversionAsOf || DASH}</strong></div>
          <div><span>公式</span><strong className="mono">{portfolio?.formulaVersion || DASH} · {portfolio?.roundingMode || DASH} · scale {portfolio?.scale ?? DASH}</strong></div>
        </div>
        <div className="ledger-readiness" aria-label="交易操作就绪状态">
          <ReadinessGate label="开仓" ready={tradingReadiness.openReady} />
          <ReadinessGate label="平仓" ready={tradingReadiness.closeReady} />
          <ReadinessGate label="恢复" ready={tradingReadiness.recoveryReady} />
          <div className="ledger-readiness__reason"><span>原因</span><strong className="mono">{tradingReadiness.reason || DASH}</strong></div>
        </div>
        <div className="ledger-residual"><span>残余证据</span><strong className="mono">{ledgerResidualText(portfolio?.residual)}</strong></div>
      </div>
    </section>
  );
}

function LedgerMetric({ label, value }) {
  return <div><span>{label}</span><strong className="mono">{value}</strong></div>;
}

function ReadinessGate({ label, ready }) {
  return <div><span>{label}</span><StatusTag tone={ready ? "positive" : "warning"}>{ready ? "READY" : "BLOCKED"}</StatusTag></div>;
}

function ledgerResidualText(residual) {
  if (!residual) return DASH;
  return [
    `leg=${residual.legQuantity ?? DASH}`,
    `notional=${residual.reportingAssetNotional ?? DASH}`,
    `orders=${residual.openOrderCount ?? DASH}`,
    `compensation=${residual.pendingCompensation ?? DASH}`,
    `class=${residual.blockerClass || DASH}`,
  ].join(" · ");
}

function PositionsPanel({ positions, portfolio, onClose, closingId, closeReady }) {
  const attentionCount = positions.filter((position) => position.tone === "warning" || position.status === "attention").length;
	const reportingAsset = portfolio?.reportingAsset ?? "";
  return (
    <section className="panel positions-panel" id="positions" aria-labelledby="positions-heading">
      <div className="panel-heading positions-heading">
        <div><div className="eyebrow">组合监控</div><h2 id="positions-heading" tabIndex={-1}>当前持仓</h2></div>
        <div className="position-summary">
          <div><span>当前未实现收益</span><strong className="mono">{formatLedgerMoney(portfolio?.currentOpenUnrealizedPnl, reportingAsset, { signed: true })}</strong></div>
          <div><span>组合总净收益</span><strong className="mono">{formatLedgerMoney(portfolio?.totalNetPnl, reportingAsset, { signed: true })}</strong></div>
          <div><span>需关注</span><strong className="mono">{attentionCount}</strong></div>
        </div>
      </div>
      <div className="positions-table" role="table" aria-label="当前套利持仓">
        <div className="position-grid position-grid--header" role="row">
          <div role="columnheader">交易对 / 路径</div><div role="columnheader">单腿名义</div><div role="columnheader">净 APR</div><div role="columnheader">累计净收益</div><div role="columnheader">对冲偏差</div><div role="columnheader">最低强平缓冲</div><div role="columnheader">下次结算</div><div role="columnheader">状态</div><div role="columnheader">操作</div>
        </div>
        <div role="rowgroup">
          {positions.map((position) => (
            <div className="position-grid position-row" role="row" key={position.id}>
              <div className="pair-cell" role="cell"><strong>{position.symbol ?? DASH}</strong><span>{qualifiedVenue(position.longVenue, position.longVenueEnvironment)} 多 · {qualifiedVenue(position.shortVenue, position.shortVenueEnvironment)} 空</span></div>
              <div className="mono" role="cell"><strong>{formatMoney(position.amount ?? position.notional)}</strong><span>{position.leverage ? `${position.leverage}x` : DASH}</span></div>
              <div className="mono positive-text" role="cell"><strong>{formatPercent(position.apr ?? position.netApr)}</strong><span>后端净值</span></div>
								<div className="mono" role="cell"><strong>{formatLedgerMoney(position.pnl, position.reportingAsset || reportingAsset, { signed: true })}</strong><span>{position.pnl == null ? "后端未返回" : "后端持仓 UPnL"}</span></div>
              <div className="mono" role="cell"><strong>{position.delta == null ? DASH : `${Number(position.delta).toFixed(2)}%`}</strong><span>后端值</span></div>
              <div className="buffer-cell mono" role="cell"><strong>{position.buffer == null ? DASH : `${position.buffer}%`}</strong><div className="meter"><i style={{ width: `${Math.max(0, Math.min(100, Number(position.buffer) || 0))}%` }} /></div></div>
              <div className="mono" role="cell"><strong>{position.nextFundingTime ?? DASH}</strong><span>UTC 结算</span></div>
              <div className="position-status" role="cell"><StatusTag tone={position.tone ?? "positive"}>{position.statusLabel ?? position.status ?? "运行中"}</StatusTag><span>{position.note ?? ""}</span></div>
              <div className="row-actions" role="cell"><button className="danger-link" onClick={() => onClose(position.id)} disabled={!closeReady || closingId === position.id}>{closingId === position.id ? "确认中" : "平仓"}</button></div>
            </div>
          ))}
          {positions.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={9}>暂无持仓。</div></div>}
        </div>
      </div>
    </section>
  );
}

function ClosedPositionsPanel({ positions }) {
  return (
    <section className="panel closed-positions-panel" id="closed-positions" aria-labelledby="closed-positions-heading">
      <div className="panel-heading">
        <div><div className="eyebrow">IMMUTABLE LEDGER PROJECTION</div><h2 id="closed-positions-heading">已平仓经济明细</h2></div>
        <div className="header-metric"><span>已平仓</span><strong className="mono">{positions.length}</strong></div>
      </div>
      <div className="positions-table" role="table" aria-label="已平仓账本明细">
        <div className="closed-position-grid position-grid--header" role="row">
          <div role="columnheader">交易对 / 路径</div><div role="columnheader">交易收益</div><div role="columnheader">实际资金费</div><div role="columnheader">手续费</div><div role="columnheader">净收益</div><div role="columnheader">已平 UPnL</div><div role="columnheader">验证 / 水位</div><div role="columnheader">残余 / 公式</div>
        </div>
        <div role="rowgroup">
          {positions.map((position) => (
            <div className="closed-position-grid position-row" role="row" key={position.id}>
              <div className="pair-cell" role="cell"><strong>{position.symbol}</strong><span>{qualifiedVenue(position.longLeg.venue, position.longLeg.venueEnvironment)} 多 · {qualifiedVenue(position.shortLeg.venue, position.shortLeg.venueEnvironment)} 空</span><small className="mono">{position.positionId}</small></div>
              <div className="mono" role="cell"><strong>{formatLedgerMoney(position.realizedTradingPnl, position.reportingAsset, { signed: true })}</strong></div>
              <div className="mono" role="cell"><strong>{formatLedgerMoney(position.actualFundingCashFlow, position.reportingAsset, { signed: true })}</strong></div>
              <div className="mono" role="cell"><strong>{formatLedgerMoney(position.fees, position.reportingAsset, { signed: true })}</strong></div>
              <div className="mono" role="cell"><strong>{formatLedgerMoney(position.netPnl, position.reportingAsset, { signed: true })}</strong></div>
              <div className="mono" role="cell"><strong>{formatLedgerMoney(position.closedTradingUpnl, position.reportingAsset, { signed: true })}</strong></div>
              <div className="position-status" role="cell"><StatusTag tone={position.tone}>{position.verificationLabel}</StatusTag><span className="mono">{position.watermark.completeThrough || DASH}</span></div>
              <div className="position-status" role="cell"><strong className="mono">{ledgerResidualText(position.residual)}</strong><span className="mono">{position.formulaVersion || DASH} · {position.conversionSource || DASH}</span></div>
            </div>
          ))}
          {positions.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={8}>暂无已平仓账本记录；缺失记录不会显示为零收益。</div></div>}
        </div>
      </div>
    </section>
  );
}

function ExecutionsPanel({ executions }) {
  return (
    <section className="panel activity-panel executions-panel" id="executions" aria-labelledby="executions-heading">
      <div className="panel-heading">
        <div><div className="eyebrow">ORDER LIFECYCLE</div><h2 id="executions-heading">执行状态</h2></div>
        <div className="header-metric"><span>执行数</span><strong className="mono">{executions.length}</strong></div>
      </div>
      <div className="activity-table" role="table" aria-label="执行状态">
        <div className="execution-grid execution-grid--header" role="row">
          <div role="columnheader">执行 ID</div>
          <div role="columnheader">操作</div>
          <div role="columnheader">状态</div>
          <div role="columnheader">最近事件</div>
          <div role="columnheader">腿 / 成交</div>
          <div role="columnheader">阻断 / 人工</div>
        </div>
        <div role="rowgroup">
          {executions.map((execution) => (
            <div className="execution-grid activity-row" role="row" key={execution.id || execution.executionId}>
              <div className="mono" role="cell"><strong>{execution.executionId ?? execution.id ?? DASH}</strong></div>
              <div role="cell"><strong>{execution.operationKind ?? DASH}</strong></div>
              <div role="cell"><StatusTag tone={execution.tone ?? "neutral"}>{execution.status ?? execution.state ?? DASH}</StatusTag></div>
              <div className="mono" role="cell">{execution.latestEventAt ?? DASH}</div>
              <div className="mono" role="cell">{Array.isArray(execution.legs) ? execution.legs.length : DASH} / {Array.isArray(execution.fills) ? execution.fills.length : DASH}</div>
              <div role="cell"><strong>{execution.blockingReason ?? (execution.manualInterventionRequired ? "manualInterventionRequired" : DASH)}</strong></div>
            </div>
          ))}
          {executions.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={6}>暂无执行状态。</div></div>}
        </div>
      </div>
    </section>
  );
}

function ActivityPanel({ activities, auditEvents }) {
  return (
    <section className="panel activity-panel" id="activity" aria-labelledby="activity-heading">
      <div className="panel-heading">
        <div><div className="eyebrow">闭环记录</div><h2 id="activity-heading">活动记录</h2></div>
        <div className="header-metric"><span>活动 / 审计</span><strong className="mono">{activities.length}/{auditEvents.length}</strong></div>
      </div>
      <div className="activity-table" role="table" aria-label="套利活动记录">
        <div className="activity-grid activity-grid--header" role="row">
          <div role="columnheader">UTC 时间</div><div role="columnheader">类型</div><div role="columnheader">交易对 / 路径</div><div role="columnheader">结果</div><div role="columnheader">金额 / 收益</div><div role="columnheader">状态</div>
        </div>
        <div role="rowgroup">
          {activities.map((activity) => (
            <div className="activity-grid activity-row" role="row" key={activity.id}>
              <div className="mono" role="cell"><strong>{activity.time ?? activity.occurredAt ?? DASH}</strong></div>
              <div role="cell"><strong>{activity.type ?? activity.kind ?? DASH}</strong></div>
              <div className="pair-cell" role="cell"><strong>{activity.symbol ?? DASH}</strong><span>{activity.route ?? DASH}</span></div>
              <div role="cell"><strong>{activity.result ?? activity.state ?? DASH}</strong></div>
              <div className="mono" role="cell"><strong>{activity.value == null ? DASH : formatSignedMoney(activity.value)}</strong></div>
              <div role="cell"><StatusTag tone={activity.tone ?? "neutral"}>{activity.status ?? activity.state ?? "记录"}</StatusTag></div>
            </div>
          ))}
          {activities.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={6}>暂无活动记录。</div></div>}
        </div>
      </div>
    </section>
  );
}

function AuditPanel({ auditEvents }) {
  return (
    <section className="panel activity-panel audit-panel" id="audit" aria-labelledby="audit-heading">
      <div className="panel-heading">
        <div><div className="eyebrow">SECURITY AUDIT</div><h2 id="audit-heading">审计事件</h2></div>
        <div className="header-metric"><span>事件数</span><strong className="mono">{auditEvents.length}</strong></div>
      </div>
      <div className="activity-table" role="table" aria-label="审计事件">
        <div className="audit-grid audit-grid--header" role="row">
          <div role="columnheader">UTC 时间</div><div role="columnheader">类型</div><div role="columnheader">结果</div><div role="columnheader">请求 ID</div>
        </div>
        <div role="rowgroup">
          {auditEvents.map((event) => (
            <div className="audit-grid activity-row" role="row" key={event.id}>
              <div className="mono" role="cell"><strong>{event.time ?? event.occurredAt ?? DASH}</strong></div>
              <div role="cell"><strong>{event.kind ?? event.type ?? DASH}</strong></div>
              <div role="cell"><StatusTag tone={event.tone ?? (event.outcome === "success" ? "positive" : "warning")}>{event.outcome ?? DASH}</StatusTag></div>
              <div className="mono" role="cell">{event.requestId ?? DASH}</div>
            </div>
          ))}
          {auditEvents.length === 0 && <div className="empty-state" role="row"><div role="cell" aria-colspan={4}>暂无审计事件。</div></div>}
        </div>
      </div>
    </section>
  );
}

function OpportunityDialog({ dialogRef, opportunity, onClose, onUse, writesReady, writeBlockReason, writeStatus }) {
  if (!opportunity) return <dialog ref={dialogRef} />;
  const displayWriteStatus = writeStatus ?? { ready: writesReady === true, reason: writeBlockReason || (writesReady ? "两腿写入就绪" : "后端禁止写入"), tone: writesReady ? "ready" : "blocked" };
  const writeBlocked = displayWriteStatus.tone === "blocked";
  return (
    <dialog ref={dialogRef} className="detail-dialog" aria-labelledby="opportunity-dialog-title" onClose={onClose} onClick={(event) => event.target === event.currentTarget && onClose()}>
      <div className="dialog-shell">
        <div className="dialog-heading">
          <div><div className="eyebrow">机会指标详情</div><h2 id="opportunity-dialog-title">{opportunity.symbol ?? DASH}</h2><p>{qualifiedVenue(opportunity.longVenue, opportunity.longVenueEnvironment)} 做多 · {qualifiedVenue(opportunity.shortVenue, opportunity.shortVenueEnvironment)} 做空</p></div>
          <button className="quiet-button" onClick={onClose}>关闭</button>
        </div>
        <div className="dialog-kpis">
          <div><span>净 APR</span><strong className="mono positive-text">{formatPercent(opportunity.netApr)}</strong><small>后端返回</small></div>
          <div><span>30 日稳定性</span><strong className="mono">{opportunity.stability == null ? DASH : `${opportunity.stability}%`}</strong><small>{opportunity.positiveSettlements ?? DASH}/{opportunity.validSettlements ?? DASH} 期为正</small></div>
          <div><span>可用容量</span><strong className="mono">{formatMoney(opportunity.capacity, 0)}</strong><small>后端容量</small></div>
          <div><span>最低强平缓冲</span><strong className="mono">{opportunity.bufferByLeverage?.[2] == null ? DASH : `${formatNumber(opportunity.bufferByLeverage[2], 1)}%`}</strong><small>2x · 后端返回</small></div>
        </div>
        <div className="dialog-body-grid">
          <div className="chart-card">
            <div className="subheading-row"><strong>资金费率走势</strong><span>{Array.isArray(opportunity.spark) ? `${opportunity.spark.length} 点` : DASH}</span></div>
            {Array.isArray(opportunity.spark) && opportunity.spark.length > 1 ? <Sparkline values={opportunity.spark} color="#52ecf2" height={145} /> : <div className="empty-state">暂无趋势数据</div>}
            <div className="chart-legend"><span>当前 {formatFundingRate(opportunity.currentFunding)}</span><span>7 日均值 {formatFundingRate(opportunity.sevenDayFunding)}</span></div>
          </div>
          <div className="metric-list">
            <div><span>毛 APR</span><strong className="mono">{formatPercent(opportunity.grossApr)}</strong></div>
            <div><span>现货 / 永续基差</span><strong className="mono">{formatPercent(opportunity.basis, 2)}</strong></div>
            <div><span>双腿开平仓费率</span><strong className="mono">{opportunity.roundTripFeeBps == null ? DASH : `${opportunity.roundTripFeeBps} bps`}</strong></div>
            <div><span>预估往返滑点</span><strong className="mono">{opportunity.estimatedSlippageBps == null ? DASH : `${opportunity.estimatedSlippageBps} bps`}</strong></div>
            <div><span>多头腿深度</span><strong className="mono">{formatMoney(opportunity.longDepth, 0)}</strong></div>
            <div><span>空头腿深度</span><strong className="mono">{formatMoney(opportunity.shortDepth, 0)}</strong></div>
            <div><span>下次结算</span><strong className="mono">{opportunity.nextFundingTime ?? DASH}</strong></div>
          </div>
        </div>
        <div className={`dialog-state dialog-state--${opportunity.fundingTone ?? "info"}`}>
          <strong>费率状态 · {opportunity.fundingState ?? DASH}</strong>
          <p>{opportunity.fundingNote ?? "后端未返回说明。"}</p>
        </div>
        <div className={`dialog-state ${displayWriteStatus.ready ? "dialog-state--positive" : "dialog-state--warning"}`}>
          <strong>写入状态 · {displayWriteStatus.ready ? "可以执行" : writeBlocked ? "后端禁止写入" : "待预览校验"}</strong>
          <p>{displayWriteStatus.ready ? "两腿写入能力已就绪，可以载入执行工作台。" : displayWriteStatus.reason}</p>
        </div>
        <div className="dialog-footer">
          <p>所有指标来自后端快照；前端不会补造未返回的收益、强平或资金费字段。</p>
          <button className="primary-button" onClick={onUse} disabled={writeBlocked}>用于执行</button>
        </div>
      </div>
    </dialog>
  );
}

function ConfirmCloseDialog({ dialogRef, position, onCancel, onConfirm, busy, closeReady, blockReason }) {
  return (
    <dialog ref={dialogRef} className="confirm-dialog" aria-labelledby="close-dialog-title" onCancel={(event) => busy && event.preventDefault()} onClose={onCancel} onClick={(event) => event.target === event.currentTarget && !busy && onCancel()}>
      <div className="confirm-shell">
        <div className="eyebrow">后端写入</div>
        <h2 id="close-dialog-title">确认平仓</h2>
        <p>将向后端提交 {position?.symbol ?? "该"} 持仓的平仓请求。请求使用稳定幂等键，重复点击不会生成新的意图。</p>
        {!closeReady && <div className="execution-status execution-status--danger" role="alert">{blockReason || "后端当前禁止平仓"}</div>}
        <div className="confirm-actions"><button className="secondary-button" onClick={onCancel} disabled={busy}>取消</button><button className="danger-button" onClick={onConfirm} disabled={busy || !closeReady}>{busy ? "处理中…" : closeReady ? "确认平仓" : "平仓已阻断"}</button></div>
      </div>
    </dialog>
  );
}

function StatusBar({ time, positionsCount, currentUser, systemStatus, readiness, realtimeStatus }) {
  const identity = currentUser?.email ?? currentUser?.principalId ?? "session";
  return (
    <footer className="statusbar">
      <div><StatusTag tone={readiness?.ready ? "positive" : "warning"}>{readiness?.ready ? "后端就绪" : "后端检查"}</StatusTag><span>{positionsCount} 个持仓 · {systemStatus?.modeLabel ?? DASH}</span></div>
      <div className="statusbar__center">{identity} · {realtimeStatus ?? "stream disconnected"} · build {systemStatus?.build ?? DASH}</div>
      <div className="mono">UTC {time}</div>
    </footer>
  );
}

export function App() {
  const [theme, setTheme] = useState("dark");
  const [currentUser, setCurrentUser] = useState(null);
  const [bootstrapping, setBootstrapping] = useState(true);
  const [sessionExpired, setSessionExpired] = useState(false);
  const [activeNav, setActiveNav] = useState("机会");
  const [accountMenuOpen, setAccountMenuOpen] = useState(false);
  const [filters, setFilters] = useState({ venue: "all", sort: "desc" });
  const [alertFilter, setAlertFilter] = useState("all");
  const [selectedId, setSelectedId] = useState(null);
  const [dialogId, setDialogId] = useState(null);
  const [amount, setAmount] = useState(DEFAULT_CAPITAL);
  const [leverage, setLeverage] = useState(2);
  const [step, setStep] = useState(0);
  const [preview, setPreview] = useState(null);
  const [executionStatus, setExecutionStatus] = useState(null);
  const [submitting, setSubmitting] = useState(false);
  const [closingId, setClosingId] = useState(null);
  const [closingBusy, setClosingBusy] = useState(false);
  const [refreshing, setRefreshing] = useState(false);
  const [toast, setToast] = useState(null);
  const [clock, setClock] = useState(new Date());
  const [systemStatus, setSystemStatus] = useState(null);
  const [readiness, setReadiness] = useState(null);
  const [loadError, setLoadError] = useState("");
  const [realtimeStatus, setRealtimeStatus] = useState(realtimeDisplayStatus("disconnected"));
  const [opportunities, setOpportunities] = useState([]);
  const [alerts, setAlerts] = useState([]);
  const [positions, setPositions] = useState([]);
  const [closedPositions, setClosedPositions] = useState([]);
  const [portfolioLedger, setPortfolioLedger] = useState(null);
  const [verification, setVerification] = useState(null);
  const [activities, setActivities] = useState([]);
  const [auditEvents, setAuditEvents] = useState([]);
  const [executions, setExecutions] = useState([]);
  const detailDialogRef = useRef(null);
  const closeDialogRef = useRef(null);
  const realtimeRef = useRef(null);
  const intentionalDisconnectRef = useRef(false);
  const cursorRef = useRef("");
  const csrfTokenRef = useRef("");
  const filtersRef = useRef(filters);
  const alertFilterRef = useRef(alertFilter);
  const submitKeyRef = useRef("");
  const closeKeysRef = useRef(new Map());
  const authSnapshotGuardRef = useRef(createAuthSnapshotGuard());

  const resetPreview = useCallback(() => {
    setStep(0);
    setPreview(null);
    setExecutionStatus(null);
    submitKeyRef.current = "";
  }, []);

  const clearPrincipalScopedState = useCallback((nextPrincipalId = "") => {
    const empty = emptyPrincipalBusinessState();
    authSnapshotGuardRef.current.boundary(nextPrincipalId);
    clearPrincipalRuntimeState({ cursorRef, closeKeysRef });
    setSystemStatus(null);
    setReadiness(null);
    setLoadError("");
    setOpportunities(empty.opportunities);
    setAlerts(empty.alerts);
    setPositions(empty.positions);
    setClosedPositions(empty.closedPositions);
    setPortfolioLedger(empty.portfolioLedger);
    setVerification(empty.verification);
    setActivities(empty.activities);
    setAuditEvents(empty.auditEvents);
    setExecutions(empty.executions);
    setSelectedId(null);
    setDialogId(null);
    setClosingId(null);
    setSubmitting(false);
    setClosingBusy(false);
    setRefreshing(false);
    setToast(null);
    resetPreview();
  }, [resetPreview]);

  const expireSession = useCallback(() => {
    intentionalDisconnectRef.current = true;
    realtimeRef.current?.close();
    realtimeRef.current = null;
    csrfTokenRef.current = "";
    clearPrincipalScopedState();
    setCurrentUser(null);
    setSessionExpired(true);
    setBootstrapping(false);
    setAccountMenuOpen(false);
  }, [clearPrincipalScopedState]);

  useEffect(() => {
    filtersRef.current = filters;
  }, [filters]);

  useEffect(() => {
    alertFilterRef.current = alertFilter;
  }, [alertFilter]);

  const loadSnapshots = useCallback(async () => {
    setLoadError("");
    const snapshotGuard = authSnapshotGuardRef.current.startSnapshot();
    const currentFilters = filtersRef.current;
    const currentAlertFilter = alertFilterRef.current;
    let snapshots;
    try {
      snapshots = await loadDashboardSnapshots(apiClient, {
        opportunitySort: currentFilters.sort === "asc" ? "netApr_asc" : "netApr_desc",
        venue: currentFilters.venue === "all" ? "" : currentFilters.venue,
        alertSeverity: currentAlertFilter,
        reportingAsset: "USDT",
        signal: snapshotGuard.signal,
      });
    } finally {
      authSnapshotGuardRef.current.finishSnapshot(snapshotGuard);
    }
    if (!authSnapshotGuardRef.current.canCommit(snapshotGuard)) return false;

    setSystemStatus(adaptSystemStatus(snapshots.systemStatus));
    setReadiness(unwrap(snapshots.readiness));
    setOpportunities(adaptOpportunityList(snapshots.opportunities));
    setAlerts(adaptAlertList(snapshots.alerts));
    setPositions(adaptPositionList(snapshots.positions));
    setActivities(adaptActivityList(snapshots.activities));
    setAuditEvents(adaptAuditEventList(snapshots.auditEvents));
    setPortfolioLedger(snapshots.portfolio ? adaptPortfolioLedger(unwrap(snapshots.portfolio)) : null);
    setClosedPositions(adaptClosedPositionLedgerList(snapshots.closedPositions));
    setVerification(adaptVerificationStatus(unwrap(snapshots.verification)));
    return true;
  }, []);

  const handleApiError = useCallback((error, fallback) => {
    if (isAbortError(error)) return true;
    if (isSessionError(error)) {
      expireSession();
      return true;
    }
    setLoadError(readableError(error, fallback));
    return false;
  }, [expireSession]);

  const applyRealtimeSnapshot = useCallback((payload) => {
    const data = unwrap(payload) ?? payload ?? {};
    const fields = realtimeSnapshotFields(data);
    if (fields.opportunities !== undefined) setOpportunities(adaptOpportunityList(fields.opportunities));
    if (fields.alerts !== undefined) setAlerts(adaptAlertList(fields.alerts));
    if (fields.positions !== undefined) setPositions(adaptPositionList(fields.positions));
    if (fields.closedPositions !== undefined) setClosedPositions(adaptClosedPositionLedgerList(fields.closedPositions));
    if (fields.portfolioLedger !== undefined) setPortfolioLedger(fields.portfolioLedger ? adaptPortfolioLedger(fields.portfolioLedger) : null);
    if (fields.verification !== undefined) setVerification(fields.verification ? adaptVerificationStatus(fields.verification) : null);
    if (fields.activities !== undefined) setActivities(adaptActivityList(fields.activities));
    if (fields.auditEvents !== undefined) setAuditEvents(adaptAuditEventList(fields.auditEvents));
    if (fields.executions !== undefined) setExecutions(fields.executions.map(adaptExecution));
  }, []);

  const applyRealtimeRecord = useCallback((record) => {
    if (record?.envelope) {
      const validation = validateRealtimeEnvelope(record.envelope, {
        principalId: currentUser?.principalId,
        mode: currentUser?.mode,
        contractVersion: PRIVATE_V1_CONTRACT_VERSION,
      });
      if (!validation.ok) {
        setRealtimeStatus(realtimeDisplayStatus("degraded"));
        setLoadError(`实时流契约不匹配（${validation.reason}），请刷新页面。`);
        return;
      }
    }
    if (record?.cursor) cursorRef.current = record.cursor;
    const kind = record?.kind ?? record?.event ?? record?.type;
    const payload = record?.data ?? record?.payload ?? record;
    if (kind === "snapshot") {
      applyRealtimeSnapshot(payload);
      setRealtimeStatus(realtimeDisplayStatus("connected"));
      return;
    }
    if (kind === "resnapshot.required" || record?.type === "resnapshot") {
      cursorRef.current = "";
      setRealtimeStatus(realtimeDisplayStatus("degraded"));
      void loadSnapshots().catch((error) => handleApiError(error, "重新载入快照失败"));
      return;
    }
    if (kind === "heartbeat" || record?.type === "heartbeat") {
      setRealtimeStatus(realtimeDisplayStatus("connected"));
      return;
    }
    if (record?.type === "malformed") {
      setRealtimeStatus(realtimeDisplayStatus("degraded"));
      return;
    }
    if (record?.type === "transport") {
      if (intentionalDisconnectRef.current) {
        setRealtimeStatus(realtimeDisplayStatus("disconnected"));
        return;
      }
      setRealtimeStatus(realtimeDisplayStatus("degraded"));
      void apiClient.getSession().catch((error) => {
        if (isSessionError(error)) expireSession();
      });
      return;
    }
    if (record?.type === "status") {
      setRealtimeStatus(realtimeDisplayStatus(record.status));
      return;
    }
    if (isLedgerInvalidationEvent(kind)) {
      void loadSnapshots().catch((error) => handleApiError(error, "重新载入账本快照失败"));
      return;
    }
    if (kind === "system.status") {
      setSystemStatus(adaptSystemStatus(payload));
      return;
    }
    if (kind === "opportunity.upsert") setOpportunities((current) => replaceById(current, adaptOpportunity(payload), ["id", "opportunityId"]));
    else if (kind === "alert.upsert") setAlerts((current) => replaceById(current, adaptAlert(payload), ["id", "alertId"]));
    else if (kind === "position.upsert") setPositions((current) => mergePositionUpsert(current, adaptPosition(payload), payload));
    else if (kind === "activity.append") setActivities((current) => replaceById(current, adaptActivity(payload), ["id", "activityId"]));
    else if (kind === "audit.append") setAuditEvents((current) => replaceById(current, adaptAuditEvent(payload), ["id", "auditId"]));
    else if (kind === "execution.upsert") setExecutions((current) => replaceById(current, adaptExecution(payload), ["id", "executionId"]));
  }, [applyRealtimeSnapshot, currentUser, expireSession, handleApiError, loadSnapshots]);

  const connectRealtime = useCallback(() => {
    intentionalDisconnectRef.current = false;
    realtimeRef.current?.close();
    const controller = createRealtimeController();
    realtimeRef.current = controller;
    controller.subscribe(applyRealtimeRecord);
    controller.connect({ cursor: cursorRef.current });
    setRealtimeStatus(realtimeDisplayStatus(controller.getState?.()?.status));
  }, [applyRealtimeRecord]);

  useLayoutEffect(() => {
    const savedTheme = window.localStorage.getItem(THEME_STORAGE_KEY);
    const initialTheme = savedTheme === "light" || savedTheme === "dark" ? savedTheme : "dark";
    document.documentElement.dataset.theme = initialTheme;
    document.documentElement.style.colorScheme = initialTheme;
    setTheme(initialTheme);
  }, []);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    document.documentElement.style.colorScheme = theme;
    window.localStorage.setItem(THEME_STORAGE_KEY, theme);
  }, [theme]);

  useEffect(() => {
    const timer = window.setInterval(() => setClock(new Date()), 1000);
    return () => window.clearInterval(timer);
  }, []);

  useEffect(() => {
    let active = true;
    const bootstrap = async () => {
      setBootstrapping(true);
      try {
        const sessionResult = await apiClient.getSession();
        if (!active) return;
        const sessionData = unwrap(sessionResult) ?? {};
        const principal = requireAuthenticatedPrincipal(sessionData);
        clearPrincipalScopedState(principal.principalId);
        csrfTokenRef.current = sessionData?.session?.csrfToken ?? "";
        setCurrentUser(principal);
        setSessionExpired(false);
      } catch (error) {
        if (isSessionError(error)) clearPrincipalScopedState();
        else setLoadError(readableError(error, "启动失败"));
      } finally {
        if (active) setBootstrapping(false);
      }
    };
    void bootstrap();
    return () => {
      active = false;
      realtimeRef.current?.close();
    };
  }, []);

  useEffect(() => {
    if (!currentUser) return undefined;
    connectRealtime();
    return () => {
      realtimeRef.current?.close();
      realtimeRef.current = null;
    };
  }, [connectRealtime, currentUser]);

  useEffect(() => {
    if (!currentUser) return;
    setRefreshing(true);
    loadSnapshots()
      .catch((error) => handleApiError(error, "刷新后端快照失败"))
      .finally(() => setRefreshing(false));
  }, [alertFilter, currentUser, filters.sort, filters.venue, handleApiError, loadSnapshots]);

  useEffect(() => {
    if (toast) {
      const timer = window.setTimeout(() => setToast(null), 2600);
      return () => window.clearTimeout(timer);
    }
    return undefined;
  }, [toast]);

  useEffect(() => {
    if (opportunities.length > 0 && (!selectedId || !opportunities.some((item) => item.id === selectedId))) {
      setSelectedId(opportunities[0].id);
      resetPreview();
    }
  }, [opportunities, resetPreview, selectedId]);

  useEffect(() => {
    const dialog = detailDialogRef.current;
    if (!dialog) return;
    if (dialogId && !dialog.open) dialog.showModal();
    if (!dialogId && dialog.open) dialog.close();
  }, [dialogId]);

  useLayoutEffect(() => {
    const dialog = closeDialogRef.current;
    if (!dialog) return;
    if (closingId) {
      if (!dialog.open) dialog.showModal();
      return;
    }
    if (dialog.open) dialog.close();
  }, [closingId]);

  const selectedOpportunity = opportunities.find((item) => item.id === selectedId) ?? opportunities[0] ?? null;
  const dialogOpportunity = opportunities.find((item) => item.id === dialogId) ?? null;
  const closingPosition = positions.find((item) => item.id === closingId) ?? null;
  const tradingReadiness = adaptTradingReadiness(readiness);
  const openGate = operationReadinessGate(readiness, "open");
  const closeGate = operationReadinessGate(readiness, "close");
  const globalWritesReady = openGate.ready;
  const closeReady = closeGate.ready;
  const globalWriteBlockReason = openGate.reason;
  // Real opportunity projections intentionally contain public market facts,
  // not private account readiness. A fresh backend preview revalidates the
  // exact selected accounts, instruments, balances and rules before submit.
  const writesReady = isOpportunityWriteReady(
    selectedOpportunity,
    globalWritesReady,
    preview?.canSubmit === true,
  );
  const writeBlockReason = !globalWritesReady ? globalWriteBlockReason : selectedOpportunity?.twoLegWriteReason ?? globalWriteBlockReason;
  const selectedWriteStatus = opportunityWriteStatus(selectedOpportunity, globalWritesReady, {
    globalWriteBlockReason,
    previewCanSubmit: preview?.canSubmit === true,
  });
  const dialogWriteStatus = opportunityWriteStatus(dialogOpportunity, globalWritesReady, {
    globalWriteBlockReason,
  });

  const visibleOpportunities = useMemo(() => {
    return opportunities
      .filter((item) => filters.venue === "all" || item.shortVenueId === filters.venue || String(item.shortVenue ?? "").toLowerCase() === filters.venue)
      .sort((a, b) => filters.sort === "desc" ? (Number(b.netApr) || 0) - (Number(a.netApr) || 0) : (Number(a.netApr) || 0) - (Number(b.netApr) || 0));
  }, [filters, opportunities]);

  const venueOptions = useMemo(() => {
    const byId = new Map();
    for (const opportunity of opportunities) {
      const candidates = [
        [opportunity.shortVenueId, opportunity.shortVenue],
        [opportunity.longVenueId, opportunity.longVenue],
      ];
      for (const [rawId, rawLabel] of candidates) {
        const id = String(rawId ?? rawLabel ?? "").trim();
        if (!id || byId.has(id)) continue;
        byId.set(id, { id, label: String(rawLabel ?? rawId) });
      }
    }
    return Array.from(byId.values()).sort((a, b) => a.label.localeCompare(b.label));
  }, [opportunities]);

  const handleLogin = async ({ email, password }) => {
    try {
      const result = await apiClient.login({ email, password });
      const sessionData = unwrap(result);
      const principal = requireAuthenticatedPrincipal(sessionData);
      clearPrincipalScopedState(principal.principalId);
      csrfTokenRef.current = sessionData?.session?.csrfToken ?? "";
      setCurrentUser(principal);
      setSessionExpired(false);
      setLoadError("");
      setToast({ tone: "positive", message: "已登录" });
      return { ok: true };
    } catch (error) {
      return { ok: false, message: readableError(error, "邮箱或密码错误") };
    }
  };

  const handleLogout = async () => {
    intentionalDisconnectRef.current = true;
    try {
      await apiClient.logout({ csrfToken: csrfTokenRef.current });
    } catch (error) {
      if (!isSessionError(error)) setToast({ tone: "warning", message: readableError(error, "退出登录请求失败") });
    }
    realtimeRef.current?.close();
    realtimeRef.current = null;
    csrfTokenRef.current = "";
    clearPrincipalScopedState();
    setCurrentUser(null);
    setAccountMenuOpen(false);
    setSessionExpired(false);
  };

  const handleRefresh = async () => {
    setRefreshing(true);
    try {
      const committed = await loadSnapshots();
      if (committed) setToast({ tone: "positive", message: "后端快照已同步" });
    } catch (error) {
      handleApiError(error, "刷新后端快照失败");
    } finally {
      setRefreshing(false);
    }
  };

  const openOpportunity = (id) => {
    setSelectedId(id);
    setDialogId(id);
    resetPreview();
  };

  const openAlertOpportunity = (alert) => {
    if (!alert.opportunityId) return;
    setFilters((current) => ({ ...current, venue: "all" }));
    setSelectedId(alert.opportunityId);
    resetPreview();
    setToast({ tone: alert.tone ?? "info", message: `已定位 ${alert.token ?? alert.symbol ?? "预警"} 对应机会` });
  };

  const handleTradeNext = async () => {
    if (!selectedOpportunity) return;
    const numericAmount = Number(amount);
    if (!Number.isFinite(numericAmount) || numericAmount < 100 || numericAmount > MAX_CAPITAL) {
      setExecutionStatus({ tone: "danger", message: "总投入资金需在 $100-$10,000 之间" });
      return;
    }
    if (step === 0) {
      const actionGuard = authSnapshotGuardRef.current.captureAction();
      setSubmitting(true);
      setExecutionStatus({ tone: "info", message: "正在请求后端执行预览…" });
      try {
        const result = await apiClient.createExecutionPreview(
          { opportunityId: selectedOpportunity.id, capital: String(numericAmount), leverage },
          { csrfToken: csrfTokenRef.current },
        );
        if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
        setPreview(adaptExecutionPreview(unwrap(result)));
        setStep(1);
        setExecutionStatus(null);
      } catch (error) {
        if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
        const handled = handleApiError(error, "执行预览失败");
        if (!canCommitAfterApiError(authSnapshotGuardRef.current, actionGuard, handled)) return;
        setExecutionStatus({ tone: "danger", message: readableError(error, "执行预览失败") });
      } finally {
        if (authSnapshotGuardRef.current.canCommit(actionGuard)) setSubmitting(false);
      }
      return;
    }
    if (!preview?.previewId || !preview?.canSubmit) return;
    if (!writesReady) {
      setExecutionStatus({ tone: "danger", message: writeBlockReason || "后端当前禁止写入" });
      return;
    }
    const actionGuard = authSnapshotGuardRef.current.captureAction();
    setSubmitting(true);
    setExecutionStatus({ tone: "info", message: "正在提交后端订单…" });
    try {
      submitKeyRef.current ||= generateIdempotencyKey();
      const result = await apiClient.submitOrder(
        { opportunityId: selectedOpportunity.id, previewId: preview.previewId, capital: String(numericAmount), leverage, operationKind: "open" },
        { csrfToken: csrfTokenRef.current, idempotencyKey: submitKeyRef.current },
      );
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      const execution = adaptExecution(result);
      setExecutions((current) => replaceById(current, execution, ["id", "executionId"]));
      await loadSnapshots();
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      if (!execution.recoverable) submitKeyRef.current = "";
      if (execution.submitFeedback?.accepted === false) {
        setStep(0);
        setPreview(null);
        setExecutionStatus({ tone: execution.submitFeedback.tone, message: execution.submitFeedback.message });
        setToast({ tone: execution.submitFeedback.tone, message: execution.submitFeedback.toastMessage });
        return;
      }
      resetPreview();
      setActiveNav("持仓");
      setExecutionStatus({ tone: execution.submitFeedback?.tone ?? "positive", message: execution.submitFeedback?.message ?? `订单已提交：${execution.executionId ?? execution.id ?? "accepted"}` });
      setToast({ tone: execution.submitFeedback?.tone ?? "positive", message: execution.submitFeedback?.toastMessage ?? "后端订单已接受" });
      window.requestAnimationFrame(() => document.querySelector("#positions")?.scrollIntoView({ behavior: "smooth" }));
    } catch (error) {
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      const handled = handleApiError(error, "提交订单失败");
      if (!canCommitAfterApiError(authSnapshotGuardRef.current, actionGuard, handled)) return;
      setExecutionStatus({ tone: "danger", message: readableError(error, "提交订单失败") });
    } finally {
      if (authSnapshotGuardRef.current.canCommit(actionGuard)) setSubmitting(false);
    }
  };

  const confirmClose = async () => {
    if (!closingPosition || !closeReady || closingBusy) return;
    const positionId = closingPosition.id;
    const actionGuard = authSnapshotGuardRef.current.captureAction();
    const key = closeKeysRef.current.get(positionId) ?? generateIdempotencyKey();
    closeKeysRef.current.set(positionId, key);
    setClosingBusy(true);
    try {
      const result = await apiClient.closePosition(positionId, { csrfToken: csrfTokenRef.current, idempotencyKey: key });
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      const execution = adaptExecution(result);
      setExecutions((current) => replaceById(current, execution, ["id", "executionId"]));
      await loadSnapshots();
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      setToast({
        tone: execution.submitFeedback?.tone ?? "positive",
        message: execution.submitFeedback?.toastMessage ?? `${closingPosition.symbol ?? positionId} 平仓请求已接受`,
      });
      if (!execution.recoverable) closeKeysRef.current.delete(positionId);
    } catch (error) {
      if (!authSnapshotGuardRef.current.canCommit(actionGuard)) return;
      const handled = handleApiError(error, "平仓失败");
      if (!canCommitAfterApiError(authSnapshotGuardRef.current, actionGuard, handled)) return;
      setToast({ tone: "danger", message: readableError(error, "平仓失败") });
    } finally {
      if (authSnapshotGuardRef.current.canCommit(actionGuard)) {
        setClosingBusy(false);
        setClosingId(null);
      }
    }
  };

  const navigate = (target) => {
    setActiveNav(target);
    setAccountMenuOpen(false);
    const selector = NAV_TARGETS[target] ?? "#activity";
    window.requestAnimationFrame(() => document.querySelector(selector)?.scrollIntoView({ behavior: "smooth" }));
  };

  const time = clock.toISOString().slice(11, 19);

  if (!currentUser) {
    return (
      <LoginScreen
        onLogin={handleLogin}
        theme={theme}
        onToggleTheme={() => setTheme((current) => current === "dark" ? "light" : "dark")}
        loading={bootstrapping}
        sessionExpired={sessionExpired}
        systemStatus={systemStatus}
      />
    );
  }

  return (
    <div className="app-shell">
      <Header
        activeNav={activeNav}
        onNavigate={navigate}
        onMode={() => setToast({ tone: "info", message: `当前模式：${systemStatus?.modeLabel ?? "unknown"}` })}
        theme={theme}
        onToggleTheme={() => setTheme((current) => current === "dark" ? "light" : "dark")}
        currentUser={currentUser}
        menuOpen={accountMenuOpen}
        onToggleMenu={() => setAccountMenuOpen((open) => !open)}
        onLogout={handleLogout}
        systemStatus={systemStatus}
        realtimeStatus={realtimeStatus}
      />
      <StateBanner state={bootstrapping ? "loading" : sessionExpired ? "expired" : "ready"} error={loadError} onRetry={handleRefresh} />
      <main className="workspace">
        <div className="upper-grid">
          <FundingAlertList alerts={alerts} filter={alertFilter} onFilterChange={setAlertFilter} onOpenOpportunity={openAlertOpportunity} locked={submitting} />
          <OpportunityTable
            rows={visibleOpportunities}
            venueOptions={venueOptions}
            selectedId={selectedId}
            selectedPreviewCanSubmit={preview?.canSubmit === true}
            onOpen={openOpportunity}
            filters={filters}
            setFilters={setFilters}
            refreshing={refreshing}
            onRefresh={handleRefresh}
            locked={submitting}
            writesReady={globalWritesReady}
            writeBlockReason={globalWriteBlockReason}
          />
          <ExecutionPanel
            opportunity={selectedOpportunity}
            amount={amount}
            onAmountChange={(value) => {
              setAmount(value);
              resetPreview();
            }}
            leverage={leverage}
            onLeverageChange={(value) => {
              setLeverage(value);
              resetPreview();
            }}
            step={step}
            setStep={setStep}
            onNext={handleTradeNext}
            executionStatus={executionStatus}
            preview={preview}
            submitting={submitting}
            writesReady={writesReady}
            writeBlockReason={writeBlockReason}
            writeStatus={selectedWriteStatus}
          />
        </div>
        <LedgerOverviewPanel portfolio={portfolioLedger} verification={verification} tradingReadiness={tradingReadiness} />
        <PositionsPanel positions={positions} portfolio={portfolioLedger} onClose={(id) => setClosingId(id)} closingId={closingId} closeReady={closeReady} />
        <ClosedPositionsPanel positions={closedPositions} />
        <ExecutionsPanel executions={executions} />
        <ActivityPanel activities={activities} auditEvents={auditEvents} />
        <AuditPanel auditEvents={auditEvents} />
      </main>
      <StatusBar time={time} positionsCount={positions.length} currentUser={currentUser} systemStatus={systemStatus} readiness={readiness} realtimeStatus={realtimeStatus} />
      <OpportunityDialog
        dialogRef={detailDialogRef}
        opportunity={dialogOpportunity}
        writesReady={isOpportunityWriteReady(dialogOpportunity, globalWritesReady)}
        writeBlockReason={!globalWritesReady ? globalWriteBlockReason : dialogOpportunity?.twoLegWriteReason ?? globalWriteBlockReason}
        writeStatus={dialogWriteStatus}
        onClose={() => setDialogId(null)}
        onUse={() => {
          setSelectedId(dialogId);
          setDialogId(null);
          resetPreview();
          setToast({ tone: "info", message: "机会已载入执行工作台" });
        }}
      />
      <ConfirmCloseDialog
        dialogRef={closeDialogRef}
        position={closingPosition}
        onCancel={() => !closingBusy && setClosingId(null)}
        onConfirm={confirmClose}
        busy={closingBusy}
        closeReady={closeReady}
        blockReason={closeGate.reason}
      />
      {toast && <div className={`toast toast--${toast.tone}`} role="status">{toast.message}</div>}
    </div>
  );
}
