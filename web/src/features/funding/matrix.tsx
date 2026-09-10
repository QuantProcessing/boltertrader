import * as Popover from "@radix-ui/react-popover";
import type { ColumnDef, SortingState } from "@tanstack/react-table";
import { ChevronDown, SearchX } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import type { FundingBoardRow, FundingBoardSnapshot } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { Segmented } from "@/components/ui/segmented";
import { StateBadge } from "@/components/ui/state-badge";
import { useT, type Translate } from "@/lib/i18n";
import { formatTimestamp } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The cross-venue funding matrix.
 *
 * Rows are instruments, columns are venues, and the point of the whole layout
 * is the horizontal read: the same perpetual quoted by ten exchanges at once,
 * so the spread between the best and worst payer is visible without arithmetic.
 * That is also why a venue that failed is drawn as a marked column rather than
 * dropped — a spread computed across a column that silently vanished is a
 * spread against a venue the operator never chose to exclude.
 *
 * Every control here filters client-side, and not as an optimisation: the board
 * route takes no query parameters at all (the server rejects a query string
 * outright), because there is one board and every visitor asks for it
 * identically. One cached read serves the page; the narrowing is ours.
 * ------------------------------------------------------------------------- */

/* --- Units --------------------------------------------------------------- */

export type FundingUnit = "period" | "24h" | "7d" | "30d" | "apr";

/** Days of accrual each unit represents. `period` is the venue's own interval. */
const UNIT_DAYS: Record<FundingUnit, number | null> = {
  period: null,
  "24h": 1,
  "7d": 7,
  "30d": 30,
  apr: 365,
};

/**
 * Places to show. A per-interval rate is a four-decimal figure; an annualised
 * one is a percentage in the tens, and four places there is noise dressed as
 * precision.
 */
const UNIT_DECIMALS: Record<FundingUnit, number> = {
  period: 4,
  "24h": 3,
  "7d": 2,
  "30d": 2,
  apr: 2,
};

/**
 * The unit selector's options are built inside the component, not here: only
 * `Period` is a word, but a module-level array is evaluated once at import and
 * would freeze whichever locale was active then.
 */

/**
 * The one place on this screen that turns a decimal string into a number.
 *
 * Same licence the instrument strip and the chart adapter carry, and for the
 * same reason: a rate rendered as "10.95%" is a pixel, not a value. Nothing
 * computed here is sent back to the server or typed into an order.
 *
 * Returns null rather than a zero when the conversion cannot be made honestly:
 * every unit except `period` needs the venue's funding interval, and two of the
 * some venues publish a bare rate with no interval at all. A row without one can
 * be shown per-period but cannot be shown per-year.
 */
function normalizedPercent(row: FundingBoardRow, unit: FundingUnit): number | null {
  if (row.rate === undefined) return null;
  const rate = Number.parseFloat(row.rate);
  if (!Number.isFinite(rate)) return null;
  if (unit === "period") return rate * 100;
  const days = UNIT_DAYS[unit];
  if (days === null) return null;
  if (row.intervalHours === undefined || row.intervalHours <= 0) return null;
  return rate * (24 / row.intervalHours) * days * 100;
}

/**
 * A last-known value remains useful for diagnosis, but it is not an executable
 * input. Keep this gate separate from formatting so a stale cell can still be
 * rendered while every ranking and spread calculation rejects it.
 */
export function actionableFundingPercent(row: FundingBoardRow, unit: FundingUnit): number | null {
  if (row.staleFields?.includes("funding") || row.missingFields?.includes("funding")) return null;
  return normalizedPercent(row, unit);
}

/** Sorting and threshold comparison only; the displayed string stays the server's. */
function magnitude(value: string | undefined): number | null {
  if (value === undefined) return null;
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : null;
}

/**
 * Search hits either spelling of the row.
 *
 * The label is one venue's symbol and the key is the pair, and on okx those are
 * not the same string — typing `btc-usdt` should find the row whether the label
 * happens to read `BTC-USDT-SWAP` or `BTC-USDT`.
 */
function matches(row: MatrixRow, needle: string): boolean {
  return row.symbol.toLowerCase().includes(needle) || row.key.toLowerCase().includes(needle);
}

/* --- The pivot ----------------------------------------------------------- */

export interface BoardColumn {
  /** Route profile id: unique per column, which venue alone is not. */
  key: string;
  venue: string;
  label: string;
  settlement?: string;
  stale: boolean;
  unavailable: boolean;
  status: "live" | "degraded" | "unavailable";
  lastSuccessAt?: string;
}

export interface MatrixRow {
  /** The pair this row is: base and quote, which is what the venues agree on. */
  key: string;
  /** A venue spelling retained only for searching legacy rows. */
  symbol: string;
  baseAsset?: string;
  cells: Record<string, FundingBoardRow>;
  venueCount: number;
  /** The largest quote-notional open interest any venue published. */
  openInterest?: string;
  openInterestVenue?: string;
  openInterestAsset?: string;
  openInterestValue: number | null;
}

interface Spread {
  value: number;
  /** The higher-rate venue in a funding comparison. */
  short: BoardColumn;
  /** The lower-rate venue in a funding comparison. */
  long: BoardColumn;
}

interface ExtremeRate {
  value: number;
  absolute: number;
  column: BoardColumn;
}

/**
 * Columns first, because two profiles can share a venue.
 *
 * There are eleven perpetual route profiles and some venues appear twice
 * under different settlement assets. Keying by venue would silently merge two
 * different books into one column; keying by route profile and disambiguating
 * the *label* only when it is actually ambiguous keeps the common case short.
 */
function pivotColumns(snapshot: FundingBoardSnapshot): BoardColumn[] {
  const venueCounts = new Map<string, number>();
  for (const venue of snapshot.venues) {
    venueCounts.set(venue.venue, (venueCounts.get(venue.venue) ?? 0) + 1);
  }
  return snapshot.venues.map((venue) => ({
    key: venue.routeProfileId,
    venue: venue.venue,
    label:
      (venueCounts.get(venue.venue) ?? 0) > 1 && venue.settlement
        ? `${venue.venue} ${venue.settlement}`
        : venue.venue,
    settlement: venue.settlement,
    stale: venue.stale,
    unavailable: venue.unavailable,
    status: venue.status,
    lastSuccessAt: venue.lastSuccessAt,
  }));
}

function venueState(column: BoardColumn, t: Translate) {
  const updated = column.lastSuccessAt
    ? t("funding.venue.updated", { time: formatTimestamp(column.lastSuccessAt) })
    : t("funding.venue.neverUpdated");
  if (column.unavailable || column.status === "unavailable") {
    return {
      tone: "danger" as const,
      label: t("funding.venue.down"),
      detail: `${t("funding.venue.down.detail")} ${updated}`,
    };
  }
  if (column.stale || column.status === "degraded") {
    return {
      tone: "warn" as const,
      label: t("funding.venue.stale"),
      detail: `${t("funding.venue.stale.detail")} ${updated}`,
    };
  }
  return {
    tone: "ok" as const,
    label: t("funding.venue.connected"),
    detail: updated,
  };
}

/**
 * What makes two venues' rows the same instrument.
 *
 * Base and quote, not the symbol: a venue's symbol is its own spelling, and okx
 * spells its USDT perpetuals `BTC-USDT-SWAP` where everyone else writes
 * `BTC-USDT`. That suffix is deliberate and load-bearing upstream — okx files
 * its catalog under the same `instId`, so canonicalising it at the source would
 * put the rate under a symbol no other okx read uses — which leaves the pivot as
 * the place to reconcile it. Keyed on the symbol, okx matched nothing and was
 * absent from all 654 cross-venue rows while binance, bybit and lighter were
 * present; keyed on the pair, it lands in the column beside them.
 *
 * Grouping by both assets keeps lighter apart on purpose — its book settles in USDC, so
 * `BTC-USDC` is a different instrument from `BTC-USDT` and gets its own row.
 *
 * Falls back to the symbol when a venue publishes no assets, which is the only
 * identity such a row has.
 */
function rowKey(row: FundingBoardRow): string {
  if (row.baseAsset === undefined || row.quoteAsset === undefined) return row.symbol;
  return `${row.baseAsset}-${row.quoteAsset}`;
}

/**
 * The board, one row per instrument and one cell per venue that quoted it.
 *
 * Exported for its own test. What `rowKey` reconciles is invisible from the
 * screen it serves: okx is unreachable from some networks, and a venue that is
 * merely absent looks exactly like a venue that is present but keyed wrong — so
 * the regression this fixes could return without anything on the page changing.
 */
export function pivotRows(snapshot: FundingBoardSnapshot): MatrixRow[] {
  const byPair = new Map<string, MatrixRow>();
  for (const venue of snapshot.venues) {
    for (const row of venue.rows ?? []) {
      const key = rowKey(row);
      let entry = byPair.get(key);
      if (entry === undefined) {
        entry = {
          key,
          symbol: row.symbol,
          baseAsset: row.baseAsset,
          cells: {},
          venueCount: 0,
          openInterestValue: null,
        };
        byPair.set(key, entry);
      }
      entry.cells[venue.routeProfileId] = row;
      entry.venueCount += 1;
      entry.baseAsset ??= row.baseAsset;

      // Open interest is only comparable after the Gateway converts venue-native
      // base units/contracts into quote-stablecoin notional. The row uses the
      // largest published venue value as a liquidity qualifier, not a partial
      // cross-venue total; the cell names its source.
      const size = magnitude(row.openInterestNotional);
      if (size !== null && (entry.openInterestValue === null || size > entry.openInterestValue)) {
        entry.openInterestValue = size;
        entry.openInterest = row.openInterestNotional;
        entry.openInterestVenue = venue.venue;
        entry.openInterestAsset = row.openInterestNotionalAsset;
      }
    }
  }
  return [...byPair.values()];
}

/**
 * The widest gap between two venues on one instrument, in the chosen unit.
 *
 * Funding is paid by longs to shorts when the rate is positive, so the hedge
 * sells the venue quoting the highest rate and buys the one quoting the lowest;
 * the difference is what the delta-neutral pair earns per period. Venues the
 * operator has filtered out are excluded, which is the point of the chips: a
 * spread against a venue you cannot trade is not an opportunity.
 */
function spreadOf(row: MatrixRow, columns: BoardColumn[], unit: FundingUnit): Spread | null {
  let high: { column: BoardColumn; value: number } | null = null;
  let low: { column: BoardColumn; value: number } | null = null;
  for (const column of columns) {
    const cell = row.cells[column.key];
    if (cell === undefined) continue;
    const value = actionableFundingPercent(cell, unit);
    if (value === null) continue;
    if (high === null || value > high.value) high = { column, value };
    if (low === null || value < low.value) low = { column, value };
  }
  if (high === null || low === null || high.column.key === low.column.key) return null;
  return { value: high.value - low.value, short: high.column, long: low.column };
}

function extremeRateOf(row: MatrixRow, columns: BoardColumn[], unit: FundingUnit): ExtremeRate | null {
  let extreme: ExtremeRate | null = null;
  for (const column of columns) {
    const cell = row.cells[column.key];
    if (cell === undefined) continue;
    const value = actionableFundingPercent(cell, unit);
    if (value === null) continue;
    const absolute = Math.abs(value);
    if (extreme === null || absolute > extreme.absolute) extreme = { value, absolute, column };
  }
  return extreme;
}

/* --- The clock ----------------------------------------------------------- */

/**
 * A settlement countdown shown to the minute needs a coarse clock.
 *
 * Ten venue columns across a few hundred instruments is a few thousand
 * countdowns; ticking those every second to move a display that only changes
 * once a minute would spend a frame budget on nothing. Ten seconds bounds the
 * lag on a minute boundary to ten seconds, and funding settles hourly — nobody
 * is racing the last second of a funding interval the way they race a preview
 * expiry.
 */
function useCoarseNow(periodMs = 10_000): number {
  const [now, setNow] = useState(() => Date.now());
  useEffect(() => {
    const timer = window.setInterval(() => setNow(Date.now()), periodMs);
    return () => window.clearInterval(timer);
  }, [periodMs]);
  return now;
}

/**
 * Not a component, so it takes `t` rather than calling `useT()` itself. Only
 * the zero case is a word; the hours and minutes are numerals with unit letters,
 * which are the same in every language this ships in.
 */
function formatCountdown(milliseconds: number, t: Translate): string {
  if (milliseconds <= 0) return t("funding.due");
  const minutes = Math.floor(milliseconds / 60_000);
  if (minutes >= 60) {
    return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
  }
  return minutes >= 1 ? `${minutes}m` : "<1m";
}

/**
 * The soonest settlement among the venues currently on screen, as a timestamp.
 *
 * Hidden columns are excluded for the same reason they are excluded from the
 * spread: a countdown to a venue the operator has filtered out is not a fact
 * about the row they are looking at. Null when no venue on the row publishes a
 * settlement time — two of the ten do not.
 */
function nextSettlement(row: MatrixRow, columns: BoardColumn[]): number | null {
  let soonest: number | null = null;
  for (const column of columns) {
    const at = row.cells[column.key]?.nextFundingTime;
    if (at === undefined) continue;
    const time = new Date(at).getTime();
    if (!Number.isFinite(time)) continue;
    if (soonest === null || time < soonest) soonest = time;
  }
  return soonest;
}

/* --- The board ----------------------------------------------------------- */

export function FundingMatrix({
  snapshot,
  loading,
}: {
  snapshot: FundingBoardSnapshot | undefined;
  loading: boolean;
}) {
  const t = useT();
  const [unit, setUnit] = useState<FundingUnit>("24h");
  const [search, setSearch] = useState("");
  const [minOpenInterest, setMinOpenInterest] = useState("");
  const [crossVenueOnly, setCrossVenueOnly] = useState(false);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());
  const [sorting, setSorting] = useState<SortingState>([
    { id: "extremeRate", desc: true },
  ]);
  const now = useCoarseNow();

  const columns = useMemo(() => (snapshot ? pivotColumns(snapshot) : []), [snapshot]);
  const allRows = useMemo(() => (snapshot ? pivotRows(snapshot) : []), [snapshot]);

  const visible = useMemo(() => columns.filter((column) => !hidden.has(column.key)), [columns, hidden]);

  const threshold = magnitude(minOpenInterest.trim() === "" ? undefined : minOpenInterest.trim());
  const needle = search.trim().toLowerCase();

  const rows = useMemo(() => {
    const kept: MatrixEntry[] = [];
    for (const row of allRows) {
      const quoted = visible.filter((column) => row.cells[column.key] !== undefined).length;
      if (quoted === 0) continue;
      if (crossVenueOnly && quoted < 2) continue;
      // The key as well as the symbol: they differ wherever a venue spells the
      // pair its own way, and someone typing `btc-usdt` means the pair.
      if (needle !== "" && !matches(row, needle)) continue;
      // A threshold can only judge a row whose open interest is known. Rows
      // from venues that publish none are kept rather than quietly
      // dropped, because dropping them would read as "these pairs are illiquid"
      // when the truth is "this venue does not say".
      if (threshold !== null && row.openInterestValue !== null && row.openInterestValue < threshold) continue;
      kept.push({ row, spread: spreadOf(row, visible, unit), extreme: extremeRateOf(row, visible, unit) });
    }
    return kept;
  }, [allRows, visible, crossVenueOnly, needle, threshold, unit]);

  const withoutOpenInterest = rows.filter((entry) => entry.row.openInterestValue === null).length;

  // `t` is deliberately out of the dependency array: `useT()` returns a fresh
  // closure every render, so keying on it would defeat the memo entirely. The
  // language store re-renders this component when the locale changes, and the
  // headers are read from `tableColumns` on that render.
  const tableColumns = useMemo(
    () => buildColumns(visible, unit, now, t),
    [visible, unit, now],
  );

  // Built here rather than at module scope, for the reason given above the
  // conversion helpers. Not memoised, because keying on `t` would never hit.
  const unitOptions: { value: FundingUnit; label: string }[] = [
    { value: "period", label: t("funding.unit.period") },
    // The rest are unit codes, the same in every language.
    { value: "24h", label: "24h" },
    { value: "7d", label: "7d" },
    { value: "30d", label: "30d" },
    { value: "apr", label: "APR" },
  ];

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      {/* One bar, not two. The venue chips used to have a row of their own, so
          two 44px bars stood between the top of the page and the first rate on
          a screen whose whole purpose is the horizontal read across venues.
          They are a filter that is set once and then left alone — a popover is
          where a control like that belongs, and the trigger still says how many
          venues are switched off so the state is never hidden. */}
      <div className="flex shrink-0 flex-wrap items-end gap-x-3 gap-y-2 border-b border-line px-3 py-2">
        <label className="flex flex-col gap-1">
          <span className="th">{t("funding.search")}</span>
          <input
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="BTC"
            className={cn(inputClass, "w-40")}
          />
        </label>

        <label className="flex flex-col gap-1">
          <span className="th">{t("funding.minOpenInterest")}</span>
          <input
            type="text"
            inputMode="decimal"
            value={minOpenInterest}
            onChange={(event) => setMinOpenInterest(event.target.value)}
            placeholder="0"
            className={cn(inputClass, "w-32")}
          />
        </label>

        <Segmented
          name="funding-unit"
          label={t("funding.rateUnit")}
          value={unit}
          options={unitOptions}
          onChange={setUnit}
          className="w-72"
        />

        <VenueFilter
          columns={columns}
          hidden={hidden}
          onToggle={(key) =>
            setHidden((current) => {
              const next = new Set(current);
              if (next.has(key)) next.delete(key);
              else next.add(key);
              return next;
            })
          }
          onShowAll={() => setHidden(new Set())}
        />

        <label className="flex h-[30px] items-center gap-2 text-base text-muted">
          <input
            type="checkbox"
            checked={crossVenueOnly}
            onChange={(event) => setCrossVenueOnly(event.target.checked)}
            className="size-3.5 accent-accent"
          />
          {t("funding.crossVenueOnly")}
        </label>

        <p className="ml-auto text-meta text-dim" role="status">
          {t("funding.count", { shown: rows.length, total: allRows.length })}
          {withoutOpenInterest > 0
            ? ` · ${t("funding.withoutOpenInterest", { count: withoutOpenInterest })}`
            : ""}
        </p>
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <DataTable
          columns={tableColumns}
          data={rows}
          getRowId={(entry) => entry.row.key}
          caption={t("funding.caption")}
          loading={loading}
          loadingRows={12}
          sorting={sorting}
          onSortingChange={setSorting}
          empty={
            <EmptyState
              icon={SearchX}
              title={t("funding.empty.filtered")}
              detail={
                allRows.length === 0
                  ? t("funding.empty.allDown")
                  : t("funding.empty.filtered.detail")
              }
            />
          }
        />
      </div>
    </div>
  );
}

/**
 * The venue filter, and what its trigger has to say when it is closed.
 *
 * A filter folded into a popover can hide the fact that it is filtering, which
 * on this board would mean a spread quietly computed across fewer venues than
 * the operator thinks. So the trigger carries the count of what is switched off,
 * and a venue that is down or stale pushes that state onto the trigger too —
 * those are conditions of the data, not preferences, and they are the reason a
 * column might be worth excluding in the first place.
 */
function VenueFilter({
  columns,
  hidden,
  onToggle,
  onShowAll,
}: {
  columns: BoardColumn[];
  hidden: ReadonlySet<string>;
  onToggle: (key: string) => void;
  onShowAll: () => void;
}) {
  const t = useT();
  const off = columns.filter((column) => hidden.has(column.key)).length;
  const degraded = columns.filter(
    (column) => !hidden.has(column.key) && (column.unavailable || column.stale),
  ).length;

  return (
    <Popover.Root>
      <Popover.Trigger
        className={cn(
          "flex h-[30px] shrink-0 items-center gap-1.5 rounded-control border border-line-strong px-2 text-base text-text",
          "hover:bg-raised focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
          "pointer-coarse:min-h-bar",
        )}
      >
        {t("funding.venues")}
        <span className="text-meta text-dim" data-numeric="">
          {columns.length - off}/{columns.length}
        </span>
        {degraded > 0 ? (
          <StateBadge
            tone="warn"
            label={String(degraded)}
            detail={
              degraded === 1
                ? t("funding.degraded.one", { count: degraded })
                : t("funding.degraded.many", { count: degraded })
            }
          />
        ) : null}
        <ChevronDown className="size-4 shrink-0 text-faint" aria-hidden="true" />
      </Popover.Trigger>

      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={6}
          className="z-40 w-72 rounded-modal border border-line-strong bg-overlay p-1 shadow-overlay"
        >
          <div className="flex h-row items-center justify-between gap-2 px-2">
            <span className="th">{t("funding.columns")}</span>
            <button
              type="button"
              onClick={onShowAll}
              disabled={off === 0}
              className="rounded-control text-meta text-dim hover:text-text disabled:opacity-50 disabled:hover:text-dim"
            >
              {t("funding.showAll")}
            </button>
          </div>
          <div className="flex max-h-80 flex-col overflow-y-auto">
            {columns.map((column) => (
              <VenueChip
                key={column.key}
                column={column}
                active={!hidden.has(column.key)}
                onToggle={() => onToggle(column.key)}
              />
            ))}
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

/**
 * A venue toggle.
 *
 * `aria-pressed` rather than a checkbox: this is a filter that acts
 * immediately, not a value submitted later, and the pressed state is what
 * assistive technology announces on activation. A venue the Gateway could not
 * reach still gets a row — knowing the column exists and is down is different
 * from the column never having been there.
 */
function VenueChip({
  column,
  active,
  onToggle,
}: {
  column: BoardColumn;
  active: boolean;
  onToggle: () => void;
}) {
  const t = useT();
  const status = venueState(column, t);

  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onToggle}
      className={cn(
        "flex h-row shrink-0 items-center gap-2 rounded-control px-2 text-base transition-colors",
        "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
        active ? "text-text hover:bg-raised" : "text-dim hover:bg-raised hover:text-muted",
      )}
    >
      {/* The tick is the pressed state made visible; `aria-pressed` above is the
          same fact for anything that does not read pixels. */}
      <span
        aria-hidden="true"
        className={cn(
          "size-3.5 shrink-0 rounded-seam border",
          active ? "border-accent bg-accent" : "border-line-strong",
        )}
      />
      <span className="min-w-0 flex-1 truncate text-left">{column.label}</span>
      {status.tone === "ok" ? null : <StateBadge tone={status.tone} label={status.label} detail={status.detail} />}
    </button>
  );
}

/* --- Columns ------------------------------------------------------------- */

type MatrixEntry = { row: MatrixRow; spread: Spread | null; extreme: ExtremeRate | null };

/** Not a component, so it takes `t` rather than calling `useT()` itself. */
function buildColumns(
  visible: BoardColumn[],
  unit: FundingUnit,
  now: number,
  t: Translate,
): ColumnDef<MatrixEntry, unknown>[] {
  const decimals = UNIT_DECIMALS[unit];

  const instrument: ColumnDef<MatrixEntry, unknown> = {
    id: "symbol",
    header: t("col.instrument"),
    accessorFn: (entry) => entry.row.key,
    cell: (context) => {
      const { row } = context.row.original;
      return (
        <div className="flex flex-col leading-tight">
          <span className="truncate font-medium text-text">{row.key}</span>
          <span className="text-micro text-dim">
            {row.venueCount === 1
              ? t("funding.venueCount.one", { count: row.venueCount })
              : t("funding.venueCount.many", { count: row.venueCount })}
          </span>
        </div>
      );
    },
    // Pinned: this table is ten venue columns wide before the spread, so the
    // horizontal read it exists for scrolls the instrument off the left edge
    // and leaves a row of rates naming nothing.
    meta: { width: "168px", sticky: true },
  };

  /**
   * When the row next settles.
   *
   * Its own column rather than a second line under each rate: the countdown was
   * repeated in all ten venue cells, where it doubled the height of every one of
   * them to say something that is very nearly the same in each — funding settles
   * hourly almost everywhere, so ten copies of "42m" framed the number the
   * column is actually read for. Shown once, it is the soonest settlement across
   * the venues still on screen, which is the one that matters: it is when the
   * board next changes.
   */
  const settles: ColumnDef<MatrixEntry, unknown> = {
    id: "settles",
    header: t("col.settles"),
    accessorFn: (entry) => nextSettlement(entry.row, visible) ?? Number.POSITIVE_INFINITY,
    cell: (context) => {
      const at = nextSettlement(context.row.original.row, visible);
      if (at === null) return <span className="text-dim">—</span>;
      return (
        <span data-numeric="" className="text-muted">
          {formatCountdown(at - now, t)}
        </span>
      );
    },
    meta: { numeric: true, width: "80px" },
  };

  const openInterest: ColumnDef<MatrixEntry, unknown> = {
    id: "openInterest",
    header: t("funding.openInterestNotional"),
    accessorFn: (entry) => entry.row.openInterestValue ?? undefined,
    cell: (context) => {
      const { row } = context.row.original;
      if (row.openInterest === undefined) {
        return <span className="text-dim" title={t("funding.noOpenInterest")}>—</span>;
      }
      return (
        <span title={t("funding.openInterestFrom", {
          venue: row.openInterestVenue ?? "",
          asset: row.openInterestAsset ?? "U",
        })}>
          <Num value={row.openInterest} decimals={0} />
        </span>
      );
    },
    // Missing OI stays last in both directions. It is absence of a comparable
    // value, not the smallest possible market, so an ascending sort must not
    // promote it above venues that actually published a notional.
    sortUndefined: "last",
    sortingFn: (left, right) => {
      const a = left.original.row.openInterestValue;
      const b = right.original.row.openInterestValue;
      if (a !== null && b !== null) return a - b;
      return (left.original.extreme?.absolute ?? -1) - (right.original.extreme?.absolute ?? -1);
    },
    meta: { numeric: true, width: "128px" },
  };

  const extremeRate: ColumnDef<MatrixEntry, unknown> = {
    id: "extremeRate",
    header: t("funding.extremeRate"),
    accessorFn: (entry) => entry.extreme?.absolute ?? undefined,
    cell: (context) => {
      const extreme = context.row.original.extreme;
      if (extreme === null) return <span className="text-dim">—</span>;
      return (
        <div className="flex flex-col items-end leading-tight">
          <Num value={extreme.value.toFixed(decimals)} decimals={decimals} signed directional suffix="%" />
          <span className="text-micro text-dim">{extreme.column.label}</span>
        </div>
      );
    },
    sortUndefined: "last",
    meta: { numeric: true, width: "116px" },
  };

  const venues: ColumnDef<MatrixEntry, unknown>[] = visible.map((column) => ({
    id: `venue:${column.key}`,
    header: () => (
      <span className="inline-flex items-center gap-1.5">
        {column.label}
        {venueState(column, t).tone === "ok" ? null : <StateBadge {...venueState(column, t)} />}
      </span>
    ),
    accessorFn: (entry) => {
      const cell = entry.row.cells[column.key];
      if (cell === undefined) return Number.NEGATIVE_INFINITY;
      return actionableFundingPercent(cell, unit) ?? Number.NEGATIVE_INFINITY;
    },
    cell: (context) => {
      const cell = context.row.original.row.cells[column.key];
      if (cell === undefined) {
        return <span className="text-dim">—</span>;
      }
      const percent = normalizedPercent(cell, unit);
      const stale = cell.staleFields?.includes("funding") ?? false;
      const missing = cell.missingFields?.includes("funding") ?? false;
      if (missing) {
        return <span className="text-dim" title={t("funding.missingValue")}>—</span>;
      }
      if (percent === null) {
        return (
          <span className="text-dim" title={t("funding.noInterval")}>
            n/a
          </span>
        );
      }
      return (
        <span className={cn(stale && "opacity-60")} title={stale ? t("funding.staleValue") : undefined}>
          <Num value={percent.toFixed(decimals)} decimals={decimals} signed directional suffix="%" />
        </span>
      );
    },
    meta: { numeric: true, width: "96px" },
  }));

  const spread: ColumnDef<MatrixEntry, unknown> = {
    id: "spread",
    header: t("funding.maxSpread"),
    accessorFn: (entry) => entry.spread?.value ?? Number.NEGATIVE_INFINITY,
    cell: (context) => {
      const { spread: best } = context.row.original;
      if (best === null) {
        return <span className="text-dim">—</span>;
      }
      return (
        // The legs are the whole content of this cell, not decoration: a spread
        // without the two venues that make it is a number nobody can act on.
        <div className="flex flex-col items-end leading-tight">
          <Num value={best.value.toFixed(decimals)} decimals={decimals} className="text-text" suffix="%" />
          <span className="text-micro text-dim">
            {t("funding.legs", { short: best.short.label, long: best.long.label })}
          </span>
        </div>
      );
    },
    meta: { numeric: true, width: "176px" },
  };

  return [instrument, extremeRate, spread, openInterest, settles, ...venues];
}
