import { useNavigate, useSearch } from "@tanstack/react-router";
import type { ColumnDef } from "@tanstack/react-table";
import { AlertTriangle, Inbox, RefreshCcw, Search, Wallet } from "lucide-react";
import { useMemo } from "react";

import type { AccountPosition, LedgerEntry, PositionFreshness, PositionSettlementSummary } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { InstrumentName } from "@/components/ui/instrument-name";
import { Num } from "@/components/ui/num";
import { Metric } from "@/components/ui/panel";
import { Tag } from "@/components/ui/state-badge";
import { useLedger, usePortfolioPositions } from "@/features/portfolio/queries";
import { liquidationPrice } from "@/features/trade/live-position";
import { useTradeRealtime } from "@/features/trade/realtime";
import { decimalSign, formatDecimal, formatTimestamp } from "@/lib/format";
import { useT, type Translate } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export type PortfolioGroup = "account" | "venue" | "instrument";

export interface PortfolioSearch {
  account?: string;
  venue?: string;
  product?: string;
  query?: string;
  group?: PortfolioGroup;
}

const GROUPS: PortfolioGroup[] = ["account", "venue", "instrument"];

export function validatePortfolioSearch(search: Record<string, unknown>): PortfolioSearch {
  const value = (candidate: unknown) => typeof candidate === "string" && candidate.trim() !== "" ? candidate.trim() : undefined;
  const group = value(search.group);
  return {
    account: value(search.account),
    venue: value(search.venue),
    product: value(search.product),
    query: value(search.query),
    group: GROUPS.includes(group as PortfolioGroup) ? group as PortfolioGroup : undefined,
  };
}

/** All venue positions for the signed-in principal; no cross-venue netting. */
export function PortfolioPage() {
  const t = useT();
  const search = useSearch({ from: "/app/portfolio" });
  const navigate = useNavigate();
  const query = useMemo(() => ({
    accountId: search.account,
    venue: search.venue,
    product: search.product,
  }), [search.account, search.product, search.venue]);
  const positions = usePortfolioPositions(query);
  const snapshot = positions.data;
  useTradeRealtime(Boolean(snapshot?.cursor), snapshot?.cursor);

  const needle = search.query?.toLowerCase() ?? "";
  const rows = useMemo(() => (snapshot?.items ?? []).filter((position) => {
    if (needle === "") return true;
    return [position.symbol, position.instrumentId, position.accountLabel, position.accountId, position.venue]
      .some((candidate) => candidate?.toLowerCase().includes(needle));
  }), [needle, snapshot?.items]);
  const group = search.group ?? "account";
  const groups = useMemo(() => groupPositions(rows, group), [group, rows]);
  const accounts = useMemo(() => unique([
    ...(snapshot?.accounts.map((account) => account.accountId) ?? []),
    ...(search.account ? [search.account] : []),
  ]), [search.account, snapshot?.accounts]);
  const venues = useMemo(() => unique([...(snapshot?.accounts.map((account) => account.venue) ?? []), ...(search.venue ? [search.venue] : [])]), [search.venue, snapshot?.accounts]);
  const products = useMemo(() => unique([...(snapshot?.items.map((item) => item.product ?? "") ?? []), ...(search.product ? [search.product] : [])]), [search.product, snapshot?.items]);
  const accountLabels = new Map(snapshot?.accounts.map((account) => [account.accountId, account.label]) ?? []);

  const update = (patch: Partial<PortfolioSearch>) => {
    void navigate({ to: "/portfolio", search: { ...search, ...patch }, replace: true });
  };

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg">
      <header className="flex min-h-bar shrink-0 flex-wrap items-center gap-2 border-b border-line px-3 py-1.5">
        <div className="mr-2 flex items-baseline gap-2">
          <h1 className="text-title font-semibold text-text">{t("portfolio.title")}</h1>
          <Tag>{t("portfolio.positionsCount", { count: snapshot?.summary.positionCount ?? rows.length })}</Tag>
        </div>
        <select className={cn(inputClass, "w-44")} value={search.account ?? ""} onChange={(event) => update({ account: event.target.value || undefined })} aria-label={t("portfolio.account")}>
          <option value="">{t("filter.allAccounts")}</option>
          {accounts.map((account) => <option key={account} value={account}>{accountLabels.get(account) ?? account}</option>)}
        </select>
        <select className={cn(inputClass, "w-36")} value={search.venue ?? ""} onChange={(event) => update({ venue: event.target.value || undefined })} aria-label={t("portfolio.venue")}>
          <option value="">{t("portfolio.allVenues")}</option>
          {venues.map((venue) => <option key={venue} value={venue}>{venue}</option>)}
        </select>
        <select className={cn(inputClass, "w-36")} value={search.product ?? ""} onChange={(event) => update({ product: event.target.value || undefined })} aria-label={t("col.product")}>
          <option value="">{t("portfolio.allProducts")}</option>
          {products.map((product) => <option key={product} value={product}>{product}</option>)}
        </select>
        <label className="relative min-w-44 flex-1">
          <Search className="pointer-events-none absolute left-2 top-1/2 size-3.5 -translate-y-1/2 text-dim" aria-hidden="true" />
          <input className={cn(inputClass, "w-full pl-7")} value={search.query ?? ""} onChange={(event) => update({ query: event.target.value || undefined })} placeholder={t("portfolio.search")} aria-label={t("portfolio.search")} />
        </label>
        <select className={cn(inputClass, "w-36")} value={group} onChange={(event) => update({ group: event.target.value as PortfolioGroup })} aria-label={t("portfolio.groupBy")}>
          {GROUPS.map((option) => <option key={option} value={option}>{groupLabel(option, t)}</option>)}
        </select>
        <Button variant="ghost" size="icon" aria-label={t("utility.refresh")} title={t("utility.refresh")} onClick={() => void positions.refetch()} disabled={positions.isFetching}>
          <RefreshCcw className={cn("size-4", positions.isFetching && "animate-spin")} />
        </Button>
      </header>

      <SummaryStrip summaries={snapshot?.summary.bySettlementAsset ?? []} staleAccounts={snapshot?.summary.staleAccountCount ?? 0} t={t} />

      {snapshot && snapshot.summary.staleAccountCount > 0 ? (
        <div className="flex shrink-0 items-center gap-2 border-b border-warn/30 bg-warn/5 px-3 py-1.5 text-meta text-warn" role="status">
          <AlertTriangle className="size-3.5" aria-hidden="true" />
          {t("portfolio.staleAccounts", { count: snapshot.summary.staleAccountCount })}
        </div>
      ) : null}

      <main className="min-h-0 flex-1 overflow-y-auto">
        {positions.isError ? (
          <EmptyState icon={AlertTriangle} title={t("portfolio.unavailable")} detail={t("portfolio.unavailable.detail")} action={<Button size="sm" onClick={() => void positions.refetch()}>{t("utility.refresh")}</Button>} />
        ) : !positions.isPending && rows.length === 0 ? (
          <EmptyState icon={Wallet} title={t("portfolio.empty.positions")} detail={t("portfolio.empty.authoritativeDetail")} />
        ) : (
          groups.map(([key, items]) => {
            const state = groupState(items, t);
            return (
              <section key={key} className="border-b border-line">
                <header className="flex h-row items-center gap-2 bg-panel px-3">
                  <h2 className="text-base font-medium text-text">{groupTitle(group, key, items, accountLabels)}</h2>
                  <Tag>{items.length}</Tag>
                  {state ? <span className="ml-auto text-meta text-dim">{state}</span> : null}
                </header>
                <DataTable columns={positionColumns(t)} data={items} getRowId={positionId} caption={t("portfolio.caption.positions")} loading={positions.isPending} />
              </section>
            );
          })
        )}
      </main>

      <LedgerPanel accountId={search.account} />
    </div>
  );
}

function SummaryStrip({ summaries, staleAccounts, t }: { summaries: PositionSettlementSummary[]; staleAccounts: number; t: Translate }) {
  return (
    <section className="flex min-h-bar shrink-0 items-center gap-7 overflow-x-auto border-b border-line px-3 py-1.5" aria-label={t("portfolio.summary")}>
      {summaries.length === 0 ? <p className="text-meta text-dim">{t("portfolio.noSummary")}</p> : summaries.map((summary) => (
        <div key={summary.settlementAsset} className="flex shrink-0 items-center gap-5">
          <Metric label={summary.settlementAsset} value={t("portfolio.positionsCount", { count: summary.positionCount })} />
          <Metric label={t("portfolio.unrealized")} value={summary.unrealizedPnl === undefined ? "—" : formatDecimal(summary.unrealizedPnl, { decimals: 2, signed: true, grouped: true })} unit={summary.settlementAsset} tone={decimalSign(summary.unrealizedPnl) > 0 ? "up" : decimalSign(summary.unrealizedPnl) < 0 ? "down" : "none"} />
          {summary.excludedPnlCount > 0 ? <span className="text-meta text-warn">{t("portfolio.excluded", { count: summary.excludedPnlCount })}</span> : null}
        </div>
      ))}
      {staleAccounts > 0 ? <span className="ml-auto shrink-0 text-meta text-warn">{t("portfolio.staleAccountsShort", { count: staleAccounts })}</span> : null}
    </section>
  );
}

function LedgerPanel({ accountId }: { accountId?: string }) {
  const t = useT();
  const ledger = useLedger(useMemo(() => accountId ? { accountId } : {}, [accountId]));
  return (
    <section className="flex h-deck shrink-0 flex-col border-t border-line">
      <header className="flex h-row shrink-0 items-center gap-2 border-b border-line px-3">
        <h2 className="text-base font-medium text-text">{t("portfolio.attributionLedger")}</h2>
        {accountId ? <Tag>{accountId}</Tag> : null}
        <span className="ml-auto text-meta text-dim">{t("portfolio.entries", { count: ledger.data?.length ?? 0 })}</span>
      </header>
      <div className="min-h-0 flex-1 overflow-y-auto">
        <DataTable columns={ledgerColumns(t)} data={ledger.data ?? []} getRowId={(entry) => entry.entryId} caption={t("portfolio.caption.ledger")} loading={ledger.isPending} empty={<EmptyState icon={Inbox} title={t("portfolio.empty.ledger")} detail={t("portfolio.empty.ledger.detail")} />} />
      </div>
    </section>
  );
}

function positionColumns(t: Translate): ColumnDef<AccountPosition, unknown>[] {
  return [
    { id: "instrument", header: t("col.instrument"), accessorFn: (item) => item.symbol || item.instrumentId, cell: ({ row }) => <div><InstrumentName instrumentId={row.original.instrumentId} /><div className="text-meta text-dim">{[row.original.venue, row.original.product, row.original.settlementAsset].filter(Boolean).join(" · ")}</div></div>, meta: { sticky: true, width: "14rem" } },
    { id: "account", header: t("col.account"), accessorFn: (item) => item.accountLabel || item.accountId, cell: ({ row }) => <span title={row.original.accountId}>{row.original.accountLabel || row.original.accountId}</span> },
    { id: "side", header: t("col.side"), accessorFn: (item) => item.side ?? (decimalSign(item.quantity) < 0 ? "sell" : "buy"), cell: ({ getValue }) => <PositionSide side={String(getValue() ?? "")} t={t} /> },
    { id: "quantity", header: t("col.quantity"), accessorKey: "quantity", cell: ({ row }) => <Num value={row.original.quantity.replace(/^-/, "")} suffix={positionQuantityUnit(row.original)} />, meta: { numeric: true } },
    { id: "entry", header: t("col.entry"), accessorKey: "entryPrice", cell: ({ row }) => value(row.original.entryPrice), meta: { numeric: true } },
    { id: "mark", header: t("col.mark"), accessorKey: "markPrice", cell: ({ row }) => sourcedValue(row.original.markPrice, row.original.markPriceSource, t), meta: { numeric: true } },
    { id: "unrealized", header: t("col.unrealized"), accessorKey: "unrealizedPnl", cell: ({ row }) => sourcedPnl(row.original, t), meta: { numeric: true } },
    { id: "liquidation", header: t("portfolio.liquidation"), accessorFn: liquidationPrice, cell: ({ getValue }) => value(String(getValue() ?? "")), meta: { numeric: true } },
    { id: "margin", header: t("portfolio.margin"), accessorKey: "marginUsed", cell: ({ row }) => <div>{value(row.original.marginUsed)}{row.original.marginMode ? <div className="text-meta text-dim">{row.original.marginMode}</div> : null}</div>, meta: { numeric: true } },
    { id: "verified", header: t("col.updated"), accessorKey: "verifiedAt", cell: ({ row }) => <span className="text-dim">{row.original.verifiedAt ? formatTimestamp(row.original.verifiedAt) : "—"}</span>, meta: { align: "right", width: "7rem" } },
  ];
}

function sourcedValue(amount: string | undefined, source: AccountPosition["markPriceSource"], t: Translate) {
  if (!amount) return <span className="text-dim">—</span>;
  return <span title={sourceLabel(source, t)}>{source === "estimated" ? "≈ " : ""}<Num value={amount} /></span>;
}

function sourcedPnl(position: AccountPosition, t: Translate) {
  if (!position.unrealizedPnl) return <span className="text-dim">—</span>;
  return <span title={sourceLabel(position.unrealizedPnlSource, t)}>{position.unrealizedPnlSource === "estimated" ? "≈ " : ""}<Num value={position.unrealizedPnl} decimals={2} signed grouped directional /> <span className="text-meta text-dim">{position.unrealizedPnlCurrency || position.settlementAsset}</span></span>;
}

function value(amount: string | undefined) {
  return amount ? <Num value={amount} /> : <span className="text-dim">—</span>;
}

function positionId(position: AccountPosition): string {
  return [position.accountId, position.routeId ?? "", position.instrumentId, position.positionSlot ?? "net"].join(":");
}

function groupPositions(rows: AccountPosition[], group: PortfolioGroup): Array<[string, AccountPosition[]]> {
  const grouped = new Map<string, AccountPosition[]>();
  for (const row of rows) {
    const key = group === "account" ? row.accountId : group === "venue" ? row.venue ?? "—" : row.symbol || row.instrumentId;
    grouped.set(key, [...(grouped.get(key) ?? []), row]);
  }
  return [...grouped.entries()].sort(([left], [right]) => left.localeCompare(right));
}

function groupTitle(group: PortfolioGroup, key: string, rows: AccountPosition[], labels: Map<string, string>): string {
  if (group === "account") return [rows[0]?.venue, labels.get(key) ?? rows[0]?.accountLabel ?? key].filter(Boolean).join(" · ");
  return key;
}

function groupState(rows: AccountPosition[], t: Translate): string | null {
  const priority: PositionFreshness[] = ["unavailable", "stale", "delayed", "syncing", "live"];
  const state = priority.find((candidate) => rows.some((row) => row.freshness === candidate));
  if (!state || state === "live") return null;
  return freshnessLabel(state, t);
}

function freshnessLabel(freshness: PositionFreshness | undefined, t: Translate): string {
  if (freshness === "live") return t("portfolio.freshness.live");
  if (freshness === "delayed") return t("portfolio.freshness.delayed");
  if (freshness === "stale") return t("portfolio.freshness.stale");
  if (freshness === "syncing") return t("portfolio.freshness.syncing");
  return t("portfolio.freshness.unavailable");
}

function sourceLabel(source: string | undefined, t: Translate): string {
  if (source === "estimated") return t("portfolio.source.estimated");
  if (source === "venue_public") return t("portfolio.source.public");
  if (source === "venue_position") return t("portfolio.source.venue");
  return t("portfolio.source.unavailable");
}

function PositionSide({ side, t }: { side: string; t: Translate }) {
  const sell = side.toLowerCase() === "sell" || side.toLowerCase() === "short";
  return <span className={cn("font-medium", sell ? "text-down" : "text-up")}>{t(sell ? "ticket.side.sell" : "ticket.side.buy")}</span>;
}

function positionQuantityUnit(position: AccountPosition): string | undefined {
  const symbol = position.symbol || position.instrumentId.split(":").at(-1) || "";
  const [base, quote] = symbol.toUpperCase().split(/[-_/]/);
  if (!base || !quote) return undefined;
  return ` ${base}`;
}

function groupLabel(group: PortfolioGroup, t: Translate): string {
  if (group === "venue") return t("portfolio.group.venue");
  if (group === "instrument") return t("portfolio.group.instrument");
  return t("portfolio.group.account");
}

function unique(values: string[]): string[] {
  return [...new Set(values.filter(Boolean))].sort((left, right) => left.localeCompare(right));
}

function ledgerColumns(t: Translate): ColumnDef<LedgerEntry, unknown>[] {
  return [
    { id: "occurred", header: t("col.time"), accessorKey: "occurredAt", meta: { width: "7rem" }, cell: ({ row }) => <span className="text-dim">{formatTimestamp(row.original.occurredAt)}</span> },
    { id: "kind", header: t("col.kind"), accessorKey: "kind", meta: { width: "9rem" } },
    { id: "instrument", header: t("col.instrument"), accessorKey: "instrumentId", cell: ({ row }) => <InstrumentName instrumentId={row.original.instrumentId} /> },
    { id: "account", header: t("col.account"), accessorKey: "accountId", meta: { width: "12rem" } },
    { id: "amount", header: t("col.amount"), accessorKey: "amount", meta: { numeric: true }, cell: ({ row }) => <Num value={row.original.amount} decimals={2} signed grouped directional /> },
    { id: "currency", header: t("col.currency"), accessorKey: "currency", meta: { width: "5rem" } },
  ];
}
