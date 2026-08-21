import { useNavigate, useSearch } from "@tanstack/react-router";
import { AlertTriangle, RefreshCcw, SearchX } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import { describeError } from "@/api/errors";
import type { ArbitrageOpportunity, ArbitrageOpportunityLeg } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { Button } from "@/components/ui/button";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { SortableTableHeader, type TableSortDirection } from "@/components/ui/sortable-table-header";
import type { StateTone } from "@/components/ui/state-badge";
import { VenueBadge } from "@/components/ui/venue-badge";
import { opportunityOpenInterestNotional, sortArbitrageOpportunities, type ArbitrageSort, type ArbitrageSortKey } from "@/features/arbitrage/opportunity-sort";
import { StrategyHistory } from "@/features/arbitrage/strategy-history";
import { arbitrageView, matchesArbitrageView, orientedEdgeRate, type ArbitrageView, type ArbitrageViewConfig } from "@/features/arbitrage/strategy";
import { ArbitragePairs } from "@/features/funding/arbitrage-pairs";
import { ArbitrageTicket } from "@/features/funding/arbitrage-ticket";
import { arbitrageBlockerLabel } from "@/features/funding/blockers";
import { useArbitrageOpportunities } from "@/features/funding/queries";
import { SpreadChart } from "@/features/funding/spread-chart";
import { useAccounts } from "@/features/market/queries";
import { formatTimestamp } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

export interface ArbitrageSearch {
  opportunity?: string;
  symbol?: string;
  long?: string;
  short?: string;
}

export function validateArbitrageSearch(search: Record<string, unknown>): ArbitrageSearch {
  const string = (value: unknown) => typeof value === "string" && value !== "" ? value : undefined;
  return { opportunity: string(search.opportunity), symbol: string(search.symbol), long: string(search.long), short: string(search.short) };
}

export function CryptoBasisArbitragePage() {
  const search = useSearch({ from: "/app/arbitrage/crypto/basis" });
  return <ArbitrageWorkspace view="crypto-basis" search={search} />;
}

export function CryptoFundingArbitragePage() {
  const search = useSearch({ from: "/app/arbitrage/crypto/funding" });
  return <ArbitrageWorkspace view="crypto-funding" search={search} />;
}

export function TradfiFundingArbitragePage() {
  const search = useSearch({ from: "/app/arbitrage/tradfi/funding" });
  return <ArbitrageWorkspace view="tradfi-funding" search={search} />;
}

export function TradfiSpreadArbitragePage() {
  const search = useSearch({ from: "/app/arbitrage/tradfi/perp-spread" });
  return <ArbitrageWorkspace view="tradfi-spread" search={search} />;
}

function ArbitrageWorkspace({ view, search }: { view: ArbitrageView; search: ArbitrageSearch }) {
  const t = useT();
  const navigate = useNavigate();
  const config = arbitrageView(view);
  const readOnly = useReadOnly();
  const [needle, setNeedle] = useState("");
  const [sort, setSort] = useState<ArbitrageSort>({ key: "edge", direction: "desc" });
  const [selectedId, setSelectedId] = useState<string | undefined>(search.opportunity);

  const accountsQuery = useAccounts();
  const accounts = useMemo(() => (accountsQuery.data ?? []).filter((account) => account.productPolicy.includes("arbitrage")), [accountsQuery.data]);
  const accountIds = useMemo(() => accounts.map((account) => account.accountId).sort(), [accounts]);
  const opportunities = useArbitrageOpportunities(accountIds);
  const strategyItems = useMemo(() => (opportunities.data?.items ?? []).filter((item) => matchesArbitrageView(view, item)), [opportunities.data, view]);
  const selected = useMemo(
    () => strategyItems.find((item) => item.id === search.opportunity) ?? strategyItems.find((item) => item.id === selectedId) ?? strategyItems.find((item) => item.symbol === search.symbol) ?? strategyItems.find((item) => item.executable) ?? strategyItems[0],
    [search.opportunity, search.symbol, selectedId, strategyItems],
  );
  useEffect(() => {
    if (selected && selected.id !== selectedId) setSelectedId(selected.id);
  }, [selected, selectedId]);
  const oriented = useMemo(() => {
    if (!selected) return null;
    if (selected.strategy === "spot_perp_basis") return { long: selected.long, short: selected.short };
    const flipped = search.long !== undefined && search.long === selected.short.routeProfileId;
    return flipped ? { long: selected.short, short: selected.long } : { long: selected.long, short: selected.short };
  }, [search.long, selected]);
  const ranked = useMemo(() => {
    const query = needle.trim().toLowerCase();
    return sortArbitrageOpportunities(strategyItems.filter((item) => (
      query === "" || item.symbol.toLowerCase().includes(query) || item.baseAsset.toLowerCase().includes(query)
    )), sort);
  }, [needle, sort, strategyItems]);

  const changeSort = (key: ArbitrageSortKey) => {
    setSort((current) => current.key === key
      ? { key, direction: current.direction === "desc" ? "asc" : "desc" }
      : { key, direction: "desc" });
  };

  const select = (opportunity: ArbitrageOpportunity) => {
    setSelectedId(opportunity.id);
    void navigate({ to: config.path, search: { opportunity: opportunity.id, symbol: opportunity.symbol }, replace: true });
  };
  const swap = () => {
    if (!selected || !oriented || selected.strategy === "spot_perp_basis") return;
    void navigate({ to: config.path, search: { opportunity: selected.id, symbol: selected.symbol, long: oriented.short.routeProfileId, short: oriented.long.routeProfileId }, replace: true });
  };
  const notice = readOnly ? t("arb.readOnly") : accounts.length === 0 ? t("arb.needAccount") : opportunities.isError ? t("arb.stale") : null;
  const executableCount = strategyItems.filter((item) => item.executable).length;

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-bg px-2 pb-2">
      <StrategyContextBar
        config={config}
        opportunities={strategyItems.length}
        executable={executableCount}
        accounts={accounts.length}
        observedAt={opportunities.data?.observedAt}
      />
      {notice ? <p className="shrink-0 border-b border-line bg-panel px-3 py-1 text-meta text-warn" role="status">{notice}</p> : null}

      <div className="grid min-h-0 flex-1 grid-cols-1 grid-rows-[360px_420px_420px_var(--spacing-deck)] gap-2 overflow-y-auto pt-2 lg:grid-cols-[minmax(320px,0.9fr)_minmax(460px,1.1fr)] lg:grid-rows-[minmax(0,1fr)_minmax(300px,0.85fr)_var(--spacing-deck)] lg:overflow-hidden xl:grid-cols-[420px_minmax(0,1fr)_360px] xl:grid-rows-[minmax(0,1fr)_var(--spacing-deck)]">
        <aside className="col-start-1 row-start-1 flex min-h-0 flex-col overflow-hidden rounded-panel border border-line bg-panel">
          <OpportunityToolbar needle={needle} refreshing={opportunities.isFetching} onRefresh={() => void opportunities.refetch()} onNeedleChange={setNeedle} />
          <OpportunityList view={view} items={ranked} selected={selected?.id} sort={sort} pending={opportunities.isPending} error={opportunities.isError ? describeError(opportunities.error, t) : null} onSort={changeSort} onSelect={select} />
        </aside>

        <section className="col-start-1 row-start-2 flex min-h-0 min-w-0 flex-col overflow-hidden rounded-panel border border-line bg-panel lg:col-start-2 lg:row-start-1">
          {selected && oriented ? (
            <>
              <OpportunitySnapshot opportunity={selected} long={oriented.long} short={oriented.short} />
              {selected.strategy === "cross_perp_funding" ? (
                <SpreadChart symbol={selected.symbol} longInstrumentId={oriented.long.instrumentId} longVenue={oriented.long.venue} shortInstrumentId={oriented.short.instrumentId} shortVenue={oriented.short.venue} />
              ) : (
                <StrategyHistory opportunity={selected} long={oriented.long} short={oriented.short} />
              )}
            </>
          ) : (
            <EmptyState icon={AlertTriangle} title={opportunities.isPending ? t("arb.empty.reading") : t("arb.empty.none")} className="flex-1" />
          )}
        </section>
        <div className="col-start-1 row-start-4 flex min-h-0 flex-col overflow-hidden rounded-panel border border-line lg:col-span-2 lg:row-start-3 xl:row-start-2"><ArbitragePairs accounts={accounts} /></div>

        <aside className="col-start-1 row-start-3 min-h-0 overflow-y-auto rounded-panel border border-line bg-panel lg:col-span-2 lg:row-start-2 xl:col-span-1 xl:col-start-3 xl:row-span-2 xl:row-start-1">
          {oriented && selected ? (
            <ArbitrageTicket symbol={opportunityDisplaySymbol(selected)} strategy={selected.strategy} baseAsset={selected.baseAsset} quoteAsset={selected.quoteAsset} long={oriented.long} short={oriented.short} accounts={accounts} onSwap={swap} />
          ) : <div className="px-3 py-4 text-base text-dim">{t("arb.empty.none")}</div>}
        </aside>
      </div>
    </div>
  );
}

function StrategyContextBar({
  config,
  opportunities,
  executable,
  accounts,
  observedAt,
}: {
  config: ArbitrageViewConfig;
  opportunities: number;
  executable: number;
  accounts: number;
  observedAt?: string;
}) {
  const t = useT();
  return (
    <header className="mt-2 flex min-h-[56px] shrink-0 items-center gap-4 overflow-x-auto rounded-panel border border-line bg-panel px-4">
      <div className="min-w-[140px] shrink-0">
        <p className="text-micro uppercase tracking-wide text-dim">{t(config.familyLabel)}</p>
        <h1 className="truncate text-title font-semibold text-text">{t(config.label)}</h1>
      </div>
      <dl className="ml-auto grid shrink-0 grid-cols-4 gap-4 sm:gap-6">
        <ContextMetric label={t("arb.pairs")} value={String(opportunities)} />
        <ContextMetric label={t("arb.executable")} value={String(executable)} tone={executable > 0 ? "up" : "default"} />
        <ContextMetric label={t("arb.accounts")} value={String(accounts)} />
        <ContextMetric label={t("funding.observed")} value={observedAt ? formatTimestamp(observedAt) : "—"} />
      </dl>
    </header>
  );
}

function ContextMetric({ label, value, tone = "default" }: { label: string; value: string; tone?: "default" | "up" }) {
  return (
    <div className="min-w-16">
      <dt className="text-micro text-dim">{label}</dt>
      <dd data-numeric="" className={cn("mt-0.5 whitespace-nowrap text-title font-semibold", tone === "up" ? "text-up" : "text-text")}>{value}</dd>
    </div>
  );
}

function OpportunityToolbar({ needle, refreshing, onRefresh, onNeedleChange }: { needle: string; refreshing: boolean; onRefresh: () => void; onNeedleChange: (value: string) => void }) {
  const t = useT();
  return (
    <div className="shrink-0 border-b border-line">
      <div className="flex h-bar items-center justify-between px-3">
        <h2 className="text-title font-semibold text-text">{t("arb.spreads")}</h2>
        <Button size="icon" variant="ghost" onClick={onRefresh} aria-label={t("utility.refresh")}>
          <RefreshCcw aria-hidden className={cn("size-3.5", refreshing && "animate-spin")} />
        </Button>
      </div>
      <div className="px-3 pb-2">
        <label><span className="sr-only">{t("arb.searchUnderlyings")}</span><input type="search" value={needle} onChange={(event) => onNeedleChange(event.target.value)} placeholder={t("arb.searchPlaceholder")} className={cn(inputClass, "w-full")} /></label>
      </div>
    </div>
  );
}

function OpportunityList({ view, items, selected, sort, pending, error, onSort, onSelect }: { view: ArbitrageView; items: ArbitrageOpportunity[]; selected: string | undefined; sort: ArbitrageSort; pending: boolean; error: string | null; onSort: (key: ArbitrageSortKey) => void; onSelect: (opportunity: ArbitrageOpportunity) => void }) {
  const t = useT();
  if (pending) return <p className="px-3 py-4 text-base text-dim">{t("arb.ranking")}</p>;
  if (items.length === 0) return error ? <p className="px-3 py-4 text-base text-danger">{error}</p> : <EmptyState icon={SearchX} title={t("arb.noMatch")} />;
  const funding = view === "crypto-funding" || view === "tradfi-funding";
  const primaryColumns = "grid-cols-[minmax(0,1fr)_72px_66px_58px] gap-x-1.5";
  const direction = (key: ArbitrageSortKey): TableSortDirection => sort.key === key ? sort.direction : false;
  return (
    <div className="min-h-0 flex-1 overflow-auto" aria-label={t("arb.spreads")}>
      <div className={cn("sticky top-0 z-10 grid h-8 w-full items-center border-b border-line bg-panel px-3 text-micro uppercase tracking-wide text-dim", primaryColumns)}>
        <span>{t("arb.instrument")}</span>
        <SortableOpportunityHeader label={funding ? t("arb.spread") : view === "crypto-basis" ? t("arb.basis") : t("arb.priceSpread")} sortKey="edge" direction={direction("edge")} onSort={onSort} />
        <SortableOpportunityHeader label={funding ? "APR" : t("arb.funding")} sortKey="carry" direction={direction("carry")} onSort={onSort} />
        <SortableOpportunityHeader label="OI (U)" sortKey="openInterest" direction={direction("openInterest")} onSort={onSort} />
      </div>
      <ul>{items.map((item) => {
        const edge = opportunityPercent(item);
        const carry = opportunityCarryPercent(item);
        const oi = opportunityOpenInterest(item);
        const blocker = item.blockers[0] ? arbitrageBlockerLabel(item.blockers[0], t) : t("arb.blocked");
        const status = item.executable ? t("arb.executable") : blocker;
        const longContext = item.long.product === "spot" ? t("arb.buySpot") : t("arb.longPerp");
        const shortContext = item.short.product === "spot" ? t("arb.product.spot") : t("arb.sellPerp");
        return (
          <li key={item.id}>
            <button type="button" onClick={() => onSelect(item)} aria-current={item.id === selected ? "true" : undefined} className={cn("min-h-[48px] w-full border-b border-line px-3 py-1.5 text-left transition-colors hover:bg-raised", item.id === selected && "bg-raised")}>
              <span className={cn("grid items-center", primaryColumns)}>
                <span className="truncate pr-2 text-base font-medium text-text">{opportunityDisplaySymbol(item)}</span>
                <span className="whitespace-nowrap text-right text-base">{edge === null ? <span className="text-dim">—</span> : <Num value={edge} decimals={3} signed directional suffix={funding ? "%/d" : "%"} />}</span>
                <span className="whitespace-nowrap text-right text-meta">{funding ? item.spreadApr ? <Num value={percentOf(item.spreadApr)} decimals={1} signed directional suffix="%" /> : <span className="text-dim">—</span> : carry === null ? <span className="text-dim">—</span> : <Num value={carry} decimals={4} signed directional suffix="%/d" />}</span>
                <span className="whitespace-nowrap text-right text-meta text-muted">{oi === null ? "—" : <Num value={compactNotional(oi)} />}</span>
              </span>
              <span className="mt-0.5 flex min-w-0 items-center justify-between gap-2 text-micro">
                <span className="flex min-w-0 items-center gap-1 overflow-hidden">
                  <VenueBadge venue={item.long.venue} className="max-w-24 shrink" />
                  <span className="shrink-0 text-up">{longContext}</span>
                  <span className="shrink-0 text-dim" aria-hidden>→</span>
                  <VenueBadge venue={item.short.venue} className="max-w-24 shrink" />
                  <span className="shrink-0 text-down">{shortContext}</span>
                </span>
                <span className={cn("max-w-[42%] shrink-0 truncate text-right", item.executable ? "text-up" : "text-dim")} title={status}>{status}</span>
              </span>
            </button>
          </li>
        );
      })}</ul>
    </div>
  );
}

function SortableOpportunityHeader({ label, sortKey, direction, onSort }: { label: string; sortKey: ArbitrageSortKey; direction: TableSortDirection; onSort: (key: ArbitrageSortKey) => void }) {
  const t = useT();
  const state = direction === "asc" ? t("table.sort.ascending") : direction === "desc" ? t("table.sort.descending") : t("table.sort.none");
  return (
    <span className="min-w-0 text-right">
      <SortableTableHeader direction={direction} onToggle={() => onSort(sortKey)} align="right" ariaLabel={t("table.sort.control", { column: label, state })}>
        {label}
      </SortableTableHeader>
    </span>
  );
}

function OpportunitySnapshot({ opportunity, long, short }: { opportunity: ArbitrageOpportunity; long: ArbitrageOpportunityLeg; short: ArbitrageOpportunityLeg }) {
  const t = useT();
  const orientedRate = orientedEdgeRate(opportunity.strategy, long, short);
  const edge = orientedRate === null ? null : (orientedRate * 100).toFixed(6);
  const funding = opportunity.strategy === "cross_perp_funding";
  const edgeLabel = funding ? t("arb.history.netFunding") : opportunity.strategy === "spot_perp_basis" ? t("arb.basis") : t("arb.priceSpread");
  const source = opportunity.strategyFamily === "tradfi_perpetual" ? long.classificationSource || short.classificationSource : undefined;
  return (
    <div className="shrink-0 border-b border-line bg-panel">
      <div className="flex h-row items-center gap-2 border-b border-line px-3">
        <h2 className="shrink-0 whitespace-nowrap text-title font-semibold text-text">{opportunityDisplaySymbol(opportunity)}</h2>
        <span className="flex min-w-0 items-center gap-1.5 overflow-hidden text-meta">
          <OpportunityVenueState leg={long} />
          <span className="shrink-0 text-up">{long.product === "spot" ? t("arb.buySpot") : t("arb.longPerp")}</span>
          <span className="shrink-0 text-dim" aria-hidden>→</span>
          <OpportunityVenueState leg={short} />
          <span className="shrink-0 text-down">{short.product === "spot" ? t("arb.product.spot") : t("arb.sellPerp")}</span>
        </span>
        {source ? <span className="ml-auto max-w-48 truncate text-meta text-dim">{t("arb.classificationSource")}: {source}</span> : null}
      </div>
      <div className="grid grid-cols-3 divide-x divide-line">
        <SnapshotMetric label={long.product === "spot" ? t("arb.snapshot.spot") : t("arb.snapshot.long")} value={funding ? percentOf(long.dailyRate) : long.referencePrice} suffix={funding ? "%/d" : ""} directional={funding} />
        <SnapshotMetric label={edgeLabel} value={edge} suffix={edge === null ? "" : funding ? "%/d" : "%"} directional />
        <SnapshotMetric label={short.product === "spot" ? t("arb.snapshot.spot") : t("arb.snapshot.short")} value={funding ? percentOf(short.dailyRate) : short.referencePrice} suffix={funding ? "%/d" : ""} directional={funding} />
      </div>
    </div>
  );
}

function OpportunityVenueState({ leg }: { leg: ArbitrageOpportunityLeg }) {
  const t = useT();
  let tone: StateTone = "ok";
  let state: string | undefined;
  if (leg.venueStatus === "unavailable") { tone = "danger"; state = t("funding.venue.down"); }
  else if (leg.stale || leg.venueStatus === "degraded") { tone = "warn"; state = t("funding.venue.stale"); }
  const observedAt = leg.fundingObservedAt ?? leg.priceObservedAt;
  return <VenueBadge venue={leg.venue} tone={tone} status={state} detail={observedAt ? t("funding.venue.updated", { time: formatTimestamp(observedAt) }) : t("funding.venue.neverUpdated")} className="max-w-36 shrink" />;
}

function SnapshotMetric({ label, value, suffix, directional = false }: { label: string; value: string | null | undefined; suffix: string; directional?: boolean }) {
  return <div className="px-3 py-2.5"><p className="text-meta text-dim">{label}</p><Num value={value ?? undefined} decimals={4} grouped={!suffix.includes("%")} signed={suffix.includes("%")} directional={directional} suffix={suffix} className="mt-1 text-metric" /></div>;
}

function opportunityPrimaryRate(opportunity: ArbitrageOpportunity): string | undefined {
  return opportunity.strategy === "cross_perp_funding" ? opportunity.spreadDaily : opportunity.strategy === "spot_perp_basis" ? opportunity.basisRate : opportunity.priceSpread;
}

function opportunityPercent(opportunity: ArbitrageOpportunity): string | null { return percentOf(opportunityPrimaryRate(opportunity)); }

function opportunityCarryPercent(opportunity: ArbitrageOpportunity): string | null {
  if (opportunity.strategy === "cross_perp_funding") return percentOf(opportunity.spreadDaily);
  if (opportunity.strategy === "spot_perp_basis") return percentOf(opportunity.short.dailyRate);
  const long = numberOf(opportunity.long.dailyRate);
  const short = numberOf(opportunity.short.dailyRate);
  return long !== null && short !== null ? ((short - long) * 100).toFixed(6) : null;
}

function opportunityOpenInterest(opportunity: ArbitrageOpportunity): number | null {
  return opportunityOpenInterestNotional(opportunity);
}

function compactNotional(value: number): string {
  if (value >= 1_000_000_000) return `${(value / 1_000_000_000).toFixed(1)}B`;
  if (value >= 1_000_000) return `${(value / 1_000_000).toFixed(1)}M`;
  if (value >= 1_000) return `${(value / 1_000).toFixed(1)}K`;
  return value.toFixed(0);
}

function opportunityDisplaySymbol(opportunity: Pick<ArbitrageOpportunity, "baseAsset" | "quoteAsset" | "symbol">): string {
  return opportunity.baseAsset && opportunity.quoteAsset
    ? `${opportunity.baseAsset}/${opportunity.quoteAsset}`
    : opportunity.symbol.replace("-", "/");
}

function percentOf(value: string | undefined): string | null {
  const parsed = numberOf(value);
  return parsed === null ? null : (parsed * 100).toFixed(6);
}

function numberOf(value: string | undefined): number | null {
  const parsed = Number.parseFloat(value ?? "");
  return Number.isFinite(parsed) ? parsed : null;
}
