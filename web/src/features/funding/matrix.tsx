import { Link } from "@tanstack/react-router";
import type { ColumnDef } from "@tanstack/react-table";
import { SearchX } from "lucide-react";
import { useEffect, useMemo, useState } from "react";

import type { FundingBoardRow, FundingBoardSnapshot } from "@/api/types";
import { Button } from "@/components/ui/button";
import { DataTable } from "@/components/ui/data-table";
import { EmptyState } from "@/components/ui/empty-state";
import { inputClass } from "@/components/ui/field";
import { Num } from "@/components/ui/num";
import { Segmented } from "@/components/ui/segmented";
import { StateBadge } from "@/components/ui/state-badge";
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

const UNIT_OPTIONS: { value: FundingUnit; label: string }[] = [
  { value: "period", label: "Period" },
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
  { value: "apr", label: "APR" },
];

/**
 * The one place on this screen that turns a decimal string into a number.
 *
 * Same licence the instrument strip and the chart adapter carry, and for the
 * same reason: a rate rendered as "10.95%" is a pixel, not a value. Nothing
 * computed here is sent back to the server or typed into an order — the
 * arbitrage page is handed venue and symbol identifiers, and re-reads the rates
 * itself before anything is signed.
 *
 * Returns null rather than a zero when the conversion cannot be made honestly:
 * every unit except `period` needs the venue's funding interval, and two of the
 * ten venues publish a bare rate with no interval at all. A row without one can
 * be shown per-period but cannot be shown per-year.
 */
function normalizedPercent(row: FundingBoardRow, unit: FundingUnit): number | null {
  const rate = Number.parseFloat(row.rate);
  if (!Number.isFinite(rate)) return null;
  if (unit === "period") return rate * 100;
  const days = UNIT_DAYS[unit];
  if (days === null) return null;
  if (row.intervalHours === undefined || row.intervalHours <= 0) return null;
  return rate * (24 / row.intervalHours) * days * 100;
}

/** Sorting and threshold comparison only; the displayed string stays the server's. */
function magnitude(value: string | undefined): number | null {
  if (value === undefined) return null;
  const parsed = Number.parseFloat(value);
  return Number.isFinite(parsed) ? parsed : null;
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
}

interface MatrixRow {
  symbol: string;
  baseAsset?: string;
  cells: Record<string, FundingBoardRow>;
  venueCount: number;
  /** The largest open interest any venue published for this instrument. */
  openInterest?: string;
  openInterestVenue?: string;
  openInterestValue: number | null;
}

interface Spread {
  value: number;
  /** The higher-rate venue: longs pay there, so the hedge sells it. */
  short: BoardColumn;
  /** The lower-rate venue: the hedge buys it. */
  long: BoardColumn;
}

/**
 * Columns first, because two profiles can share a venue.
 *
 * A mode carries eleven perpetual route profiles and some venues appear twice
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
  }));
}

function pivotRows(snapshot: FundingBoardSnapshot): MatrixRow[] {
  const bySymbol = new Map<string, MatrixRow>();
  for (const venue of snapshot.venues) {
    for (const row of venue.rows) {
      let entry = bySymbol.get(row.symbol);
      if (entry === undefined) {
        entry = {
          symbol: row.symbol,
          baseAsset: row.baseAsset,
          cells: {},
          venueCount: 0,
          openInterestValue: null,
        };
        bySymbol.set(row.symbol, entry);
      }
      entry.cells[venue.routeProfileId] = row;
      entry.venueCount += 1;
      entry.baseAsset ??= row.baseAsset;

      // Seven of the ten venues leave open interest out of their bulk call, so
      // the row's figure is whichever venue did publish one — the largest, when
      // more than one did. It is a liquidity qualifier for the instrument, not
      // a total across venues, and the cell says which venue it came from.
      const size = magnitude(row.openInterest);
      if (size !== null && (entry.openInterestValue === null || size > entry.openInterestValue)) {
        entry.openInterestValue = size;
        entry.openInterest = row.openInterest;
        entry.openInterestVenue = venue.venue;
      }
    }
  }
  return [...bySymbol.values()];
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
    const value = normalizedPercent(cell, unit);
    if (value === null) continue;
    if (high === null || value > high.value) high = { column, value };
    if (low === null || value < low.value) low = { column, value };
  }
  if (high === null || low === null || high.column.key === low.column.key) return null;
  return { value: high.value - low.value, short: high.column, long: low.column };
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

function formatCountdown(milliseconds: number): string {
  if (milliseconds <= 0) return "due";
  const minutes = Math.floor(milliseconds / 60_000);
  if (minutes >= 60) {
    return `${Math.floor(minutes / 60)}h ${String(minutes % 60).padStart(2, "0")}m`;
  }
  return minutes >= 1 ? `${minutes}m` : "<1m";
}

/* --- The board ----------------------------------------------------------- */

export function FundingMatrix({
  snapshot,
  loading,
}: {
  snapshot: FundingBoardSnapshot | undefined;
  loading: boolean;
}) {
  const [unit, setUnit] = useState<FundingUnit>("apr");
  const [search, setSearch] = useState("");
  const [minOpenInterest, setMinOpenInterest] = useState("");
  const [crossVenueOnly, setCrossVenueOnly] = useState(true);
  const [hidden, setHidden] = useState<ReadonlySet<string>>(() => new Set());
  const now = useCoarseNow();

  const columns = useMemo(() => (snapshot ? pivotColumns(snapshot) : []), [snapshot]);
  const allRows = useMemo(() => (snapshot ? pivotRows(snapshot) : []), [snapshot]);

  const visible = useMemo(() => columns.filter((column) => !hidden.has(column.key)), [columns, hidden]);

  const threshold = magnitude(minOpenInterest.trim() === "" ? undefined : minOpenInterest.trim());
  const needle = search.trim().toLowerCase();

  const rows = useMemo(() => {
    const kept: { row: MatrixRow; spread: Spread | null }[] = [];
    for (const row of allRows) {
      const quoted = visible.filter((column) => row.cells[column.key] !== undefined).length;
      if (quoted === 0) continue;
      if (crossVenueOnly && quoted < 2) continue;
      if (needle !== "" && !row.symbol.toLowerCase().includes(needle)) continue;
      // A threshold can only judge a row whose open interest is known. Rows
      // from the seven venues that publish none are kept rather than quietly
      // dropped, because dropping them would read as "these pairs are illiquid"
      // when the truth is "this venue does not say".
      if (threshold !== null && row.openInterestValue !== null && row.openInterestValue < threshold) continue;
      kept.push({ row, spread: spreadOf(row, visible, unit) });
    }
    return kept;
  }, [allRows, visible, crossVenueOnly, needle, threshold, unit]);

  const withoutOpenInterest = rows.filter((entry) => entry.row.openInterestValue === null).length;

  const tableColumns = useMemo(
    () => buildColumns(visible, unit, now),
    [visible, unit, now],
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex shrink-0 flex-wrap items-end gap-x-4 gap-y-2 border-b border-line px-3 py-2">
        <label className="flex flex-col gap-1">
          <span className="th">Search</span>
          <input
            type="search"
            value={search}
            onChange={(event) => setSearch(event.target.value)}
            placeholder="BTC"
            className={cn(inputClass, "w-40")}
          />
        </label>

        <label className="flex flex-col gap-1">
          <span className="th">Min open interest</span>
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
          label="Funding rate unit"
          value={unit}
          options={UNIT_OPTIONS}
          onChange={setUnit}
          className="w-72"
        />

        <label className="flex h-[30px] items-center gap-2 text-base text-muted">
          <input
            type="checkbox"
            checked={crossVenueOnly}
            onChange={(event) => setCrossVenueOnly(event.target.checked)}
            className="size-3.5 accent-accent"
          />
          Cross-venue only
        </label>

        <p className="ml-auto text-meta text-dim" role="status">
          {rows.length} of {allRows.length} pairs
          {withoutOpenInterest > 0 ? ` · ${withoutOpenInterest} without open interest` : ""}
        </p>
      </div>

      <div className="flex shrink-0 flex-wrap items-center gap-1.5 border-b border-line px-3 py-2">
        {columns.map((column) => (
          <VenueChip
            key={column.key}
            column={column}
            active={!hidden.has(column.key)}
            onToggle={() =>
              setHidden((current) => {
                const next = new Set(current);
                if (next.has(column.key)) next.delete(column.key);
                else next.add(column.key);
                return next;
              })
            }
          />
        ))}
      </div>

      <div className="min-h-0 flex-1 overflow-y-auto">
        <DataTable
          columns={tableColumns}
          data={rows}
          getRowId={(entry) => entry.row.symbol}
          caption="Funding rates by instrument and venue"
          loading={loading}
          loadingRows={12}
          empty={
            <EmptyState
              icon={SearchX}
              title="No pairs match these filters"
              detail={
                allRows.length === 0
                  ? "The board is empty. Every venue column is unavailable."
                  : "Widen the search, lower the open-interest threshold, or turn off cross-venue only."
              }
            />
          }
        />
      </div>
    </div>
  );
}

/**
 * A venue toggle.
 *
 * `aria-pressed` rather than a checkbox: this is a filter that acts
 * immediately, not a value submitted later, and the pressed state is what
 * assistive technology announces on activation. A venue the Gateway could not
 * reach still gets a chip — knowing the column exists and is down is different
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
  return (
    <button
      type="button"
      aria-pressed={active}
      onClick={onToggle}
      className={cn(
        "inline-flex h-6 items-center gap-1.5 rounded-sm border px-2 text-meta transition-colors",
        "focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
        "pointer-coarse:min-h-bar pointer-coarse:px-3",
        active
          ? "border-line-strong bg-raised text-text"
          : "border-line text-dim hover:text-muted",
      )}
    >
      {column.label}
      {column.unavailable ? (
        <StateBadge tone="danger" label="down" detail="This venue did not answer; it has no rates on the board." />
      ) : column.stale ? (
        <StateBadge tone="warn" label="stale" detail="These rates are older than the rest of the table." />
      ) : null}
    </button>
  );
}

/* --- Columns ------------------------------------------------------------- */

type MatrixEntry = { row: MatrixRow; spread: Spread | null };

function buildColumns(
  visible: BoardColumn[],
  unit: FundingUnit,
  now: number,
): ColumnDef<MatrixEntry, unknown>[] {
  const decimals = UNIT_DECIMALS[unit];

  const instrument: ColumnDef<MatrixEntry, unknown> = {
    id: "symbol",
    header: "Instrument",
    accessorFn: (entry) => entry.row.symbol,
    cell: (context) => {
      const { row } = context.row.original;
      return (
        <div className="flex flex-col leading-tight">
          <span className="truncate font-medium text-text">{row.symbol}</span>
          <span className="text-micro text-dim">
            {row.venueCount} {row.venueCount === 1 ? "venue" : "venues"}
          </span>
        </div>
      );
    },
    meta: { width: "168px" },
  };

  const openInterest: ColumnDef<MatrixEntry, unknown> = {
    id: "openInterest",
    header: "Open interest",
    accessorFn: (entry) => entry.row.openInterestValue ?? Number.NEGATIVE_INFINITY,
    cell: (context) => {
      const { row } = context.row.original;
      if (row.openInterest === undefined) {
        return <span className="text-dim" title="No venue on this row publishes open interest in its bulk feed.">—</span>;
      }
      return (
        <span title={`Reported by ${row.openInterestVenue}`}>
          <Num value={row.openInterest} decimals={0} />
        </span>
      );
    },
    // Rows whose venues publish no open interest cannot be ranked by it, so the
    // tie-break is the thing the board exists to rank by instead. Without this
    // they would clump in an arbitrary order at one end of the sort.
    sortingFn: (left, right) => {
      const a = left.original.row.openInterestValue;
      const b = right.original.row.openInterestValue;
      if (a !== null && b !== null) return a - b;
      if (a !== null) return 1;
      if (b !== null) return -1;
      return (left.original.spread?.value ?? -1) - (right.original.spread?.value ?? -1);
    },
    meta: { numeric: true, width: "128px" },
  };

  const venues: ColumnDef<MatrixEntry, unknown>[] = visible.map((column) => ({
    id: `venue:${column.key}`,
    header: () => (
      <span className="inline-flex items-center gap-1.5">
        {column.label}
        {column.unavailable ? (
          <StateBadge tone="danger" label="down" detail="This venue did not answer; it has no rates on the board." />
        ) : column.stale ? (
          <StateBadge tone="warn" label="stale" detail="These rates are older than the rest of the table." />
        ) : null}
      </span>
    ),
    accessorFn: (entry) => {
      const cell = entry.row.cells[column.key];
      if (cell === undefined) return Number.NEGATIVE_INFINITY;
      return normalizedPercent(cell, unit) ?? Number.NEGATIVE_INFINITY;
    },
    cell: (context) => {
      const cell = context.row.original.row.cells[column.key];
      if (cell === undefined) {
        return <span className="text-dim">—</span>;
      }
      const percent = normalizedPercent(cell, unit);
      return (
        <div className="flex flex-col items-end leading-tight">
          {percent === null ? (
            <span className="text-dim" title="This venue does not publish a funding interval, so the rate cannot be annualised.">
              n/a
            </span>
          ) : (
            <Num value={percent.toFixed(decimals)} decimals={decimals} signed directional />
          )}
          <span className="text-micro text-dim">
            {cell.nextFundingTime === undefined
              ? "—"
              : formatCountdown(new Date(cell.nextFundingTime).getTime() - now)}
          </span>
        </div>
      );
    },
    meta: { numeric: true, width: "112px" },
  }));

  const spread: ColumnDef<MatrixEntry, unknown> = {
    id: "spread",
    header: "Max spread",
    accessorFn: (entry) => entry.spread?.value ?? Number.NEGATIVE_INFINITY,
    cell: (context) => {
      const { spread: best } = context.row.original;
      if (best === null) {
        return <span className="text-dim">—</span>;
      }
      return (
        // The legs are the whole content of this cell, not decoration: a spread
        // without the two venues that make it is a number nobody can act on.
        // The arbitrage page reads them back from here once it exists.
        <div className="flex flex-col items-end leading-tight">
          <Num value={best.value.toFixed(decimals)} decimals={decimals} className="text-text" />
          <span className="text-micro text-dim">
            short {best.short.label} · long {best.long.label}
          </span>
        </div>
      );
    },
    meta: { numeric: true, width: "176px" },
  };

  const hedge: ColumnDef<MatrixEntry, unknown> = {
    id: "hedge",
    header: "",
    enableSorting: false,
    cell: (context) => {
      const { row, spread: best } = context.row.original;
      // Nothing to hand over when one venue is the only one quoting this
      // underlying: the arbitrage page would open on a pair that does not exist.
      if (best === null) return null;
      return (
        // A link rather than a click handler, so the row's destination is a real
        // address: it can be opened in a new tab, and it carries the operator's
        // orientation instead of leaving the next screen to guess it.
        <Button asChild size="sm" variant="outline">
          <Link
            to="/funding/arbitrage"
            search={{ symbol: row.symbol, long: best.long.venue, short: best.short.venue }}
          >
            Hedge
          </Link>
        </Button>
      );
    },
    meta: { width: "88px" },
  };

  return [instrument, openInterest, ...venues, spread, hedge];
}
