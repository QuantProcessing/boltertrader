import { AlertCircle, RefreshCw, X } from "lucide-react";
import { useCallback, useEffect, useMemo, useRef, useState } from "react";

import { apiClient } from "../api/client";
import { connectRealtime } from "../state/realtime";
import { loadWorkspaceSnapshot } from "../state/workspaceSnapshot";
import { LoginView } from "../components/LoginView";
import { Shell } from "../components/Shell";
import { AccountsRoute } from "./routes/Accounts";
import { ActivityRoute } from "./routes/Activity";
import { FundingRoute } from "./routes/Funding";
import { ManualRoute } from "./routes/Manual";
import { OverviewRoute } from "./routes/Overview";
import { SettingsRoute } from "./routes/Settings";
import type {
  Account,
  ActivityEvent,
  AuditRecord,
  Capability,
  Command,
  Fill,
  FundingOpportunity,
  FundingPreview,
  FundingReservationRequirement,
  Instrument,
  LedgerEntry,
  Mode,
  Position,
  Preview,
  RuntimeView,
  Venue,
} from "./types";
import { flattenRuntimeCapabilities } from "./view-models";

interface SessionResult {
  principal: { id: string; mode: Mode; username: string };
  session: { csrfToken: string; expiresAt: string };
}

interface Readiness {
  ready: boolean;
  components?: Record<string, { ready?: boolean; reason?: string; state?: string }>;
}

interface FundingLineage {
  lineageId: string;
  closeReservationRequirements: FundingReservationRequirement[];
}

interface WorkspaceSnapshot {
  status: Readiness;
  readiness: Readiness;
  accounts: Account[];
  runtimes: Record<string, RuntimeView[]>;
  venues: Venue[];
  capabilities: Capability[];
  instruments: Instrument[];
  opportunities: FundingOpportunity[];
  commands: Command[];
  fills: Fill[];
  positions: Position[];
  ledger: LedgerEntry[];
  activity: ActivityEvent[];
  audit: AuditRecord[];
  manualOrders: Command[];
}

type Theme = "dark" | "light";
const routes = new Set(["/overview", "/funding", "/manual", "/accounts", "/activity", "/settings"]);
const emptySnapshot: WorkspaceSnapshot = {
  status: { ready: false, components: {} }, readiness: { ready: false, components: {} }, accounts: [], runtimes: {}, venues: [], capabilities: [], instruments: [], opportunities: [], commands: [], fills: [], positions: [], ledger: [], activity: [], audit: [], manualOrders: [],
};

function unwrap<T>(response: { data: T } | null): T {
  if (!response || response.data === undefined || (typeof response.data === "object" && response.data !== null && "scaffold" in response.data && response.data.scaffold)) {
    throw new Error("Platform returned an incomplete browser projection.");
  }
  return response.data;
}

function list<T>(value: unknown, key = "items"): T[] {
  if (Array.isArray(value)) return value as T[];
  if (value && typeof value === "object") {
    const nested = (value as Record<string, unknown>)[key];
    if (Array.isArray(nested)) return nested as T[];
  }
  return [];
}

function errorMessage(error: unknown): string {
  if (error && typeof error === "object" && "code" in error) return String((error as { code: unknown }).code).replaceAll("_", " ");
  return error instanceof Error ? error.message : "The request could not be completed.";
}

function isTerminalIdempotencyConflict(error: unknown): boolean {
  if (typeof error !== "object" || error === null) return false;
  const candidate = error as { status?: unknown; code?: unknown };
  return candidate.status === 409 && candidate.code === "IDEMPOTENCY_CONFLICT";
}

function initialTheme(): Theme {
  try {
    const stored = typeof window.localStorage?.getItem === "function" ? window.localStorage.getItem("bt-theme") : null;
    return stored === "light" ? "light" : "dark";
  } catch {
    return "dark";
  }
}

function initialPath(): string {
  return routes.has(window.location.pathname) ? window.location.pathname : "/overview";
}

async function readWorkspace(): Promise<WorkspaceSnapshot> {
  const snapshot = await loadWorkspaceSnapshot({
    status: async ({ signal }) => unwrap<Readiness>(await apiClient.get("/system/status", { signal })),
    readiness: async ({ signal }) => unwrap<Readiness>(await apiClient.get("/system/readiness", { signal, allowStatuses: [503] })),
    accounts: async ({ signal }) => list<Account>(unwrap(await apiClient.get("/accounts", { signal })), "accounts"),
    runtimes: async ({ signal }) => {
      const accounts = list<Account>(unwrap(await apiClient.get("/accounts", { signal })), "accounts");
      const entries = await Promise.all(accounts.map(async (account) => [account.accountId, list<RuntimeView>(unwrap(await apiClient.get(`/accounts/${encodeURIComponent(account.accountId)}/runtimes`, { signal })))] as const));
      return Object.fromEntries(entries);
    },
    venues: async ({ signal }) => list<Venue>(unwrap(await apiClient.get("/catalog/venues", { signal }))),
    instruments: async ({ signal }) => list<Instrument>(unwrap(await apiClient.get("/catalog/instruments", { signal }))),
    opportunities: async ({ signal }) => list<FundingOpportunity>(unwrap(await apiClient.get("/products/funding/opportunities", { signal }))),
    commands: async ({ signal }) => list<Command>(unwrap(await apiClient.get("/commands", { signal }))),
    fills: async ({ signal }) => list<Fill>(unwrap(await apiClient.get("/fills", { signal }))),
    positions: async ({ signal }) => list<Position>(unwrap(await apiClient.get("/positions", { signal }))),
    ledger: async ({ signal }) => list<LedgerEntry>(unwrap(await apiClient.get("/ledger", { signal }))),
    activity: async ({ signal }) => list<ActivityEvent>(unwrap(await apiClient.get("/activity", { signal }))),
    audit: async ({ signal }) => list<AuditRecord>(unwrap(await apiClient.get("/audit", { signal })), "audit"),
    manualOrders: async ({ signal }) => list<Command>(unwrap(await apiClient.get("/products/manual/orders", { signal }))),
  });
  return { ...snapshot, capabilities: flattenRuntimeCapabilities(snapshot.runtimes) } as WorkspaceSnapshot;
}

export function App() {
  const [theme, setTheme] = useState<Theme>(initialTheme);
  const [path, setPath] = useState(initialPath);
  const [session, setSession] = useState<SessionResult | null>();
  const [snapshot, setSnapshot] = useState<WorkspaceSnapshot>(emptySnapshot);
  const [loading, setLoading] = useState(true);
  const [busy, setBusy] = useState(false);
  const [error, setError] = useState("");
  const [notice, setNotice] = useState("");
  const [streamState, setStreamState] = useState("disconnected");
  const [contractVersion, setContractVersion] = useState("boltertrader-product-platform-upgrade/v4");
  const [fundingPreview, setFundingPreview] = useState<FundingPreview | null>(null);
  const [manualPreview, setManualPreview] = useState<Preview | null>(null);
  const resnapshotRef = useRef<() => Promise<void>>(async () => undefined);

  useEffect(() => {
    document.documentElement.dataset.theme = theme;
    try {
      if (typeof window.localStorage?.setItem === "function") window.localStorage.setItem("bt-theme", theme);
    } catch {
      // Theme persistence is optional; authenticated state never depends on it.
    }
  }, [theme]);

  useEffect(() => {
    const pop = () => setPath(initialPath());
    window.addEventListener("popstate", pop);
    return () => window.removeEventListener("popstate", pop);
  }, []);

  const load = useCallback(async () => {
    setLoading(true);
    try {
      const next = await readWorkspace();
      setSnapshot(next);
      setError("");
    } catch (loadError) {
      setError(errorMessage(loadError));
    } finally {
      setLoading(false);
    }
  }, []);
  resnapshotRef.current = load;

  useEffect(() => {
    let live = true;
    const unsubscribe = apiClient.onSessionExpired(() => {
      if (!live) return;
      setSession(null);
      setSnapshot(emptySnapshot);
      setFundingPreview(null);
      setManualPreview(null);
    });
    apiClient.get<SessionResult>("/auth/session").then((response) => {
      if (!live) return;
      const restored = unwrap(response);
      apiClient.setSession({ csrfToken: restored.session.csrfToken });
      setContractVersion(response?.meta?.contractVersion ?? contractVersion);
      setSession(restored);
    }).catch(() => {
      if (!live) return;
      apiClient.clearSession("bootstrap-unauthenticated");
      setSession(null);
      setLoading(false);
    });
    return () => { live = false; unsubscribe(); };
  }, []);

  useEffect(() => {
    if (!session) return;
    void load();
    const realtime = connectRealtime({ onEvent: (message) => {
      if (message.type === "status" || message.type === "transport") setStreamState(message.state.status);
      if (message.type === "resnapshot-required") {
        setStreamState("resnapshot");
        void resnapshotRef.current().then(() => realtime.resumeAfterSnapshot());
      }
      if (message.type === "event") void resnapshotRef.current();
    }});
    const stop = realtime.subscribe((state) => setStreamState(state.status));
    setStreamState(realtime.getState().status);
    return () => { stop(); realtime.close(); };
  }, [session?.principal.id, load]);

  const login = async (username: string, password: string) => {
    setBusy(true); setError("");
    try {
      const response = await apiClient.post<SessionResult>("/auth/login", { username, password });
      const result = unwrap(response);
      apiClient.setSession({ csrfToken: result.session.csrfToken });
      setSession(result);
    } catch (loginError) { setError(errorMessage(loginError)); }
    finally { setBusy(false); }
  };

  const logout = async () => {
    setBusy(true);
    try { await apiClient.delete("/auth/session"); }
    finally { apiClient.clearSession("logout"); setSession(null); setSnapshot(emptySnapshot); setBusy(false); }
  };

  const navigate = (next: string) => {
    if (!routes.has(next)) return;
    window.history.pushState({}, "", next);
    setPath(next);
  };

  const perform = async (operation: () => Promise<unknown>, success: string, reload = true) => {
    setBusy(true); setError(""); setNotice("");
    try { await operation(); if (reload) await load(); setNotice(success); }
    catch (operationError) { setError(errorMessage(operationError)); }
    finally { setBusy(false); }
  };

  const createAccount = (request: Record<string, unknown>) => perform(() => apiClient.post("/accounts", request, { idempotencyScope: `account.create:${request.venue}:${request.displayName}` }), "Account submitted.");
  const accountAction = (accountID: string, action: string, payload: Record<string, unknown> = {}) => {
    const account = snapshot.accounts.find((item) => item.accountId === accountID);
    if (!account?.desiredStateRevision || account.desiredStateRevision < 1) {
      return perform(async () => { throw new Error("Authoritative account revision is unavailable."); }, "", false);
    }
    const options = { headers: { "If-Match": `\"desired-state-revision-${account.desiredStateRevision}\"` }, idempotencyScope: `account.${action}:${accountID}:${account.desiredStateRevision}` };
    if (action === "remove") return perform(() => apiClient.delete(`/accounts/${encodeURIComponent(accountID)}`, options), "Account removal submitted.");
    if (action === "update") return perform(() => apiClient.patch(`/accounts/${encodeURIComponent(accountID)}`, payload, options), "Account update submitted.");
    return perform(() => apiClient.post(`/accounts/${encodeURIComponent(accountID)}/${action}`, payload, options), `Account ${action} submitted.`);
  };

  const fundingSubmit = (request: Record<string, unknown>) => {
    const lineageID = `funding-${crypto.randomUUID()}`;
    const key = apiClient.idempotencyKey(`funding.open:${request.previewId}:${lineageID}`);
    return perform(async () => {
      if (!fundingPreview || fundingPreview.previewId !== request.previewId || fundingPreview.reservationRequirements.length === 0) {
        throw new Error("Platform did not issue Funding open authority.");
      }
      for (const requirement of fundingPreview.reservationRequirements) {
        const scope = requirement.nettingScope;
        if (!requirement.accountId || requirement.productId !== "funding" || requirement.reservationKind !== "open" || requirement.mode !== session?.principal.mode || !scope?.accountId || scope.accountId !== requirement.accountId || !scope.venue || !scope.instrumentId || !scope.venueEnvironment || !requirement.quantity || !requirement.notional || !requirement.currency) {
          throw new Error("Platform issued an incomplete Funding reservation requirement.");
        }
        await apiClient.post(
          "/reservations",
          requirement,
          { idempotencyScope: `funding.open.reserve:${fundingPreview.previewId}:${requirement.accountId}:${scope.instrumentId}` },
        );
      }
      await apiClient.post("/products/funding/commands", { ...request, lineageId: lineageID, idempotencyKey: key }, { idempotencyKey: key });
    }, "Funding command accepted.");
  };
  const fundingClose = (position: Position) => perform(async () => {
    const lineage = unwrap(await apiClient.get<FundingLineage>(`/products/funding/lineage/${encodeURIComponent(position.lineageId)}`));
    if (lineage.lineageId !== position.lineageId || !Array.isArray(lineage.closeReservationRequirements) || lineage.closeReservationRequirements.length === 0) {
      throw new Error("Platform did not issue Funding close authority.");
    }
    await Promise.all(lineage.closeReservationRequirements.map((requirement) => apiClient.post(
      "/reservations",
      requirement,
      { idempotencyScope: `funding.close.reserve:${position.lineageId}:${requirement.accountId}:${requirement.nettingScope.instrumentId}` },
    )));
    const key = apiClient.idempotencyKey(`funding.close:${position.lineageId}`);
    await apiClient.post(
      "/products/funding/commands",
      { previewId: "", action: "close", lineageId: position.lineageId, idempotencyKey: key },
      { idempotencyKey: key },
    );
  }, "Funding close accepted.");
  const manualSubmit = (request: Record<string, unknown>) => {
    const lineageID = `manual-${crypto.randomUUID()}`;
    const key = apiClient.idempotencyKey(`manual.place:${request.previewId}:${lineageID}`);
    return perform(() => apiClient.post("/products/manual/commands", { ...request, lineageId: lineageID, orderId: "", idempotencyKey: key }, { idempotencyKey: key }), "Manual order accepted.");
  };
	  const manualCancel = (order: Command) => {
	    const key = apiClient.idempotencyKey(`manual.cancel:${order.commandId}`);
	    return perform(() => apiClient.post("/products/manual/commands", { previewId: "", action: "cancel", lineageId: order.lineageId, orderId: order.commandId, idempotencyKey: key }, { idempotencyKey: key }), "Manual order canceled.");
	  };
  const manualClose = (position: Position) => {
    const scope = `manual.close:${position.lineageId}`;
    const key = apiClient.idempotencyKey(scope);
    return perform(async () => {
      try {
        await apiClient.post("/products/manual/commands", { previewId: "", action: "close", lineageId: position.lineageId, orderId: "", idempotencyKey: key }, { idempotencyKey: key });
      } catch (closeError) {
        if (isTerminalIdempotencyConflict(closeError)) apiClient.clearIdempotencyKey(scope);
        throw closeError;
      }
    }, "Manual close accepted.");
  };

  const page = useMemo(() => {
    switch (path) {
      case "/funding": return <FundingRoute mode={session?.principal.mode ?? "test"} accounts={snapshot.accounts} opportunities={snapshot.opportunities} positions={snapshot.positions.filter((item) => item.productId === "funding")} commands={snapshot.commands.filter((item) => item.productId === "funding")} preview={fundingPreview} busy={busy} onPreview={async (request) => perform(async () => setFundingPreview(unwrap(await apiClient.post<FundingPreview>("/products/funding/execution-previews", request))), "Preview refreshed.", false)} onSubmit={fundingSubmit} onClose={fundingClose} onRefresh={load} />;
      case "/manual": return <ManualRoute mode={session?.principal.mode ?? "test"} accounts={snapshot.accounts} instruments={snapshot.instruments} orders={snapshot.manualOrders} positions={snapshot.positions.filter((item) => item.productId === "manual")} preview={manualPreview} busy={busy} onPreview={async (request) => perform(async () => setManualPreview(unwrap(await apiClient.post<Preview>("/products/manual/execution-previews", request))), "Preview refreshed.", false)} onSubmit={manualSubmit} onCancel={manualCancel} onClose={manualClose} onRefresh={load} />;
      case "/accounts": return <AccountsRoute mode={session?.principal.mode ?? "test"} accounts={snapshot.accounts} runtimes={snapshot.runtimes} venues={snapshot.venues} capabilities={snapshot.capabilities} busy={busy} onCreate={createAccount} onAction={accountAction} onRefresh={load} />;
      case "/activity": return <ActivityRoute activity={snapshot.activity} audit={snapshot.audit} onRefresh={load} />;
      case "/settings": return <SettingsRoute theme={theme} mode={session?.principal.mode ?? "test"} username={session?.principal.username ?? ""} contractVersion={contractVersion} onThemeChange={setTheme} />;
      default: return <OverviewRoute mode={session?.principal.mode ?? "test"} readiness={snapshot.readiness} accounts={snapshot.accounts} positions={snapshot.positions} commands={snapshot.commands} fills={snapshot.fills} ledger={snapshot.ledger} onRefresh={load} />;
    }
  }, [path, session, snapshot, fundingPreview, manualPreview, busy, theme, contractVersion, load]);

  if (session === undefined) return <div className="boot-view" role="status"><div className="brand-mark">BT</div><span>Loading workspace...</span></div>;
  if (!session) return <LoginView theme={theme} busy={busy} error={error} onLogin={login} onThemeToggle={() => setTheme((current) => current === "dark" ? "light" : "dark")} />;

  return <Shell currentPath={path} mode={session.principal.mode} username={session.principal.username} ready={snapshot.readiness.ready} streamState={streamState} theme={theme} onNavigate={navigate} onThemeToggle={() => setTheme((current) => current === "dark" ? "light" : "dark")} onLogout={logout}>
    {error && <div className="state-banner state-banner--danger" role="alert"><AlertCircle /><div><strong>Authoritative state unavailable</strong><span>{error}</span></div><button className="icon-button" aria-label="Dismiss error" onClick={() => setError("")}><X /></button><button className="button button--quiet" onClick={load}><RefreshCw />Retry</button></div>}
    {notice && <div className="toast" role="status">{notice}<button className="icon-button" aria-label="Dismiss notification" onClick={() => setNotice("")}><X /></button></div>}
    {loading && <div className="route-skeleton" role="status" aria-label="Loading authoritative workspace"><span /><span /><span /></div>}
    {!loading && page}
  </Shell>;
}
