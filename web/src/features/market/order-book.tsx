import { useMemo, useState } from "react";

import type { DepthLevel, Instrument, MarketFact } from "@/api/types";
import { Num } from "@/components/ui/num";
import { Segmented } from "@/components/ui/segmented";
import { useT, type Key } from "@/lib/i18n";
import { cn } from "@/lib/utils";

import { decimalsOf, snapPrice, stepLabel } from "./precision";

/* ---------------------------------------------------------------------------
 * The order book.
 *
 * The depth bars are the second sanctioned float in the product, after the
 * chart: a bar's width is a pixel count. The price and quantity *text* is still
 * the untouched string from the server, and clicking a row hands that same
 * string to the ticket — the number is never the thing that gets traded.
 *
 * Aggregation does not break that rule. Grouping levels onto a coarser grid is
 * string truncation (`precision.ts`), so a grouped row's price is a prefix of a
 * price the venue quoted and is still a string when it reaches the ticket. What
 * is summed is the quantity, and only into the same float the bars already use.
 *
 * How many places to show is the venue's answer, not ours: `priceIncrement` and
 * `quantityIncrement` ride along on every instrument. Before they were read,
 * this column rendered binance's `64142.51000000` in full and collided with the
 * size beside it — 256px cannot hold sixteen digits three times over.
 * ------------------------------------------------------------------------- */

/** Rows per side when both are shown. One side alone gets twice as many. */
const DEPTH_ROWS = 12;

/** The grids offered, as multiples of the instrument's own tick. */
const STEPS = [1, 10, 100] as const;

/** Three columns, price widest: it is the longest number and the clickable one. */
const COLUMNS = "grid-cols-[minmax(0,1.1fr)_minmax(0,1fr)_minmax(0,1fr)]";

type View = "both" | "bid" | "ask";

/**
 * Annotated rather than `as const`, because only two of the three carry a tone
 * and a const tuple would make `tone` unreachable on the union of its members.
 * The label is a key, not a string: this array outlives any one render, and the
 * dictionary lookup belongs where the locale is known.
 */
const VIEWS: ReadonlyArray<{ value: View; labelKey: Key; tone?: "buy" | "sell" }> = [
  { value: "both", labelKey: "book.side.all" },
  { value: "bid", labelKey: "book.side.bids", tone: "buy" },
  { value: "ask", labelKey: "book.side.asks", tone: "sell" },
];

export function OrderBook({
  fact,
  instrument,
  quoteAsset,
  baseAsset,
  onPickPrice,
}: {
  fact: MarketFact | undefined;
  /** Carries the venue's tick and lot sizes; null before a market is chosen. */
  instrument: Instrument | null;
  quoteAsset?: string;
  baseAsset?: string;
  onPickPrice: (price: string) => void;
}) {
  const [step, setStep] = useState<number>(1);
  const [view, setView] = useState<View>("both");
  const t = useT();

  // The venue's own grid, and how far it can be coarsened before the price
  // stops being a price. A missing rule falls back to what the quotes
  // themselves carry, which is what this file did before rules were read.
  const tick = decimalsOf(instrument?.rules.priceIncrement);
  const lot = decimalsOf(instrument?.rules.quantityIncrement);
  const quoted = tick ?? quotedDecimals(fact);
  const decimals = quoted - exponent(step);

  const asks = useMemo(
    () => cumulate(aggregate(fact?.asks ?? [], decimals, true)),
    [fact?.asks, decimals],
  );
  const bids = useMemo(
    () => cumulate(aggregate(fact?.bids ?? [], decimals, false)),
    [fact?.bids, decimals],
  );

  const scale = Math.max(asks.at(-1)?.total ?? 0, bids.at(-1)?.total ?? 0);
  const spread = fact ? spreadOf(fact, quoted) : null;
  const rows = view === "both" ? DEPTH_ROWS : DEPTH_ROWS * 2;

  return (
    <section aria-label={t("book.label")} className="flex min-h-0 flex-1 flex-col">
      {/* Which side, and at what grid. The panel needs no title: a red-over-green
          ladder under Price/Size/Total is not mistakable for anything else, and
          256px of width is better spent on the numbers. */}
      <div className="flex h-row shrink-0 items-center gap-2 border-b border-line px-3">
        <Segmented
          name="depth-view"
          label={t("book.side")}
          value={view}
          // Built inline rather than memoised: `useT` returns a fresh closure
          // every render, so a `[t]` dependency would never hit.
          options={VIEWS.map((entry) => ({ value: entry.value, label: t(entry.labelKey), tone: entry.tone }))}
          onChange={setView}
          className="flex-1"
        />
        <div role="group" aria-label={t("book.grouping")} className="flex shrink-0 items-center gap-0.5">
          {STEPS.map((multiple) => {
            const label = stepLabel(quoted - exponent(multiple));
            return (
              <button
                key={multiple}
                type="button"
                onClick={() => setStep(multiple)}
                aria-pressed={step === multiple}
                className={cn(
                  "rounded-control px-1.5 py-0.5 text-micro tabular-nums",
                  "focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
                  step === multiple
                    ? "bg-fill-strong text-text"
                    : "text-dim hover:bg-fill hover:text-muted",
                )}
              >
                {label}
              </button>
            );
          })}
        </div>
      </div>

      <div className={cn("th grid shrink-0 gap-2 px-3 py-1", COLUMNS)}>
        <span>
          {t("book.price")}
          {quoteAsset ? ` ${quoteAsset}` : ""}
        </span>
        <span className="text-right">
          {t("book.size")}
          {baseAsset ? ` ${baseAsset}` : ""}
        </span>
        <span className="text-right">{t("book.total")}</span>
      </div>

      {view === "bid" ? null : (
        <div className="flex min-h-0 flex-1 flex-col justify-end overflow-hidden">
          {/* Reversed so the best ask is adjacent to the spread, not at the top
              of the screen where it would be furthest from the price it is
              closest to. */}
          {asks
            .slice(0, rows)
            .reverse()
            .map((level) => (
              <Row
                key={`ask-${level.price}`}
                level={level}
                scale={scale}
                side="ask"
                decimals={decimals}
                lot={lot}
                onPick={onPickPrice}
              />
            ))}
        </div>
      )}

      {/* The spread, absolute and in basis points. The absolute figure alone
          says nothing on a five-figure price — 3.0 is wide on ETH and tight on
          BTC — so the relative one is what makes it readable across markets. */}
      <div className="flex h-row shrink-0 items-baseline justify-between gap-2 border-y border-line px-3">
        <Num value={fact?.bestAsk} decimals={quoted} className="text-title font-medium text-down" />
        {spread === null ? (
          <span className="text-meta text-dim">—</span>
        ) : (
          <span className="flex items-baseline gap-1 text-meta text-dim">
            <span className="th">{t("book.spread")}</span>
            <span data-numeric="" className="text-muted">
              {spread.absolute}
            </span>
            <span data-numeric="">{spread.bps} bp</span>
          </span>
        )}
      </div>

      {view === "ask" ? null : (
        <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
          {bids.slice(0, rows).map((level) => (
            <Row
              key={`bid-${level.price}`}
              level={level}
              scale={scale}
              side="bid"
              decimals={decimals}
              lot={lot}
              onPick={onPickPrice}
            />
          ))}
        </div>
      )}

      {fact ? null : (
        <div className="flex flex-1 items-center justify-center text-meta text-dim">{t("book.waiting")}</div>
      )}
    </section>
  );
}

interface CumulativeLevel extends DepthLevel {
  total: number;
  totalLabel: string;
}

function Row({
  level,
  scale,
  side,
  decimals,
  lot,
  onPick,
}: {
  level: CumulativeLevel;
  scale: number;
  side: "bid" | "ask";
  decimals: number;
  lot: number | undefined;
  onPick: (price: string) => void;
}) {
  const width = scale > 0 ? Math.min(100, (level.total / scale) * 100) : 0;
  return (
    <button
      type="button"
      onClick={() => onPick(level.price)}
      className={cn(
        "relative grid h-depth w-full shrink-0 items-center gap-2 px-3 text-left",
        "hover:bg-raised focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent",
        COLUMNS,
      )}
    >
      <span
        aria-hidden="true"
        style={{ width: `${width}%` }}
        className={cn("absolute inset-y-0 right-0", side === "bid" ? "bg-up/12" : "bg-down/12")}
      />
      <Num
        value={level.price}
        decimals={Math.max(0, decimals)}
        className={cn("relative", side === "bid" ? "text-up" : "text-down")}
      />
      <Num value={level.quantity} decimals={lot} className="relative text-right text-muted" />
      <span data-numeric="" className="relative text-right text-dim">
        {level.totalLabel}
      </span>
    </button>
  );
}

/**
 * Collapses levels onto a coarser price grid.
 *
 * Bids snap down and asks snap up, so every level inside a group is at least as
 * good as the price the group advertises and no group ever quotes into the
 * spread. A grid the price has no digits left for is not a grid: `snapPrice`
 * says so by returning null, and the levels pass through ungrouped rather than
 * collapsing the whole book onto one row.
 */
function aggregate(levels: DepthLevel[], decimals: number, up: boolean): DepthLevel[] {
  if (levels.length === 0) return levels;

  const groups: DepthLevel[] = [];
  const index = new Map<string, number>();

  for (const level of levels) {
    const price = snapPrice(level.price, decimals, up);
    if (price === null) return levels;

    const at = index.get(price);
    if (at === undefined) {
      index.set(price, groups.length);
      groups.push({ ...level, price });
      continue;
    }
    const group = groups[at]!;
    groups[at] = { ...group, quantity: addQuantity(group.quantity, level.quantity) };
  }
  return groups;
}

/**
 * Sums two quantity strings at the wider of their scales.
 *
 * The third sanctioned float, and the narrowest: a grouped size is a number
 * that exists only because we grouped, so there is no server string it could
 * have preserved. Both inputs are multiples of the venue's lot size, so the sum
 * is too, and stating it at the wider input scale cannot invent a digit.
 */
function addQuantity(left: string, right: string): string {
  const places = Math.max(fractionDigits(left), fractionDigits(right));
  const sum = Number.parseFloat(left) + Number.parseFloat(right);
  return Number.isFinite(sum) ? sum.toFixed(places) : left;
}

/**
 * Running depth. See the file header on why the float is acceptable: `total` is
 * consumed only as a bar width and as its own rounded label, never as an order
 * quantity.
 *
 * It is stated by magnitude rather than at the venue's lot size, which is the
 * one place in this file where the venue's own precision is deliberately not
 * followed: the lot is what an order has to be a multiple of, and a running
 * total is never an order. BTC's 0.00001 lot would put five places on a
 * six-figure cumulative sum and take the width from the two columns that need
 * it.
 */
function cumulate(levels: DepthLevel[]): CumulativeLevel[] {
  let running = 0;
  return levels.map((level) => {
    const parsed = Number.parseFloat(level.quantity);
    running += Number.isFinite(parsed) ? parsed : 0;
    return {
      ...level,
      total: running,
      totalLabel: running.toFixed(running >= 1000 ? 0 : running >= 1 ? 2 : 4),
    };
  });
}

interface Spread {
  absolute: string;
  bps: string;
}

/** Best ask minus best bid, absolute and relative to the mid. */
function spreadOf(fact: MarketFact, decimals: number): Spread | null {
  const ask = Number.parseFloat(fact.bestAsk);
  const bid = Number.parseFloat(fact.bestBid);
  if (!Number.isFinite(ask) || !Number.isFinite(bid)) return null;

  const mid = (ask + bid) / 2;
  return {
    absolute: (ask - bid).toFixed(decimals),
    bps: mid > 0 ? significant(((ask - bid) / mid) * 10_000) : "—",
  };
}

/**
 * Two significant figures, never an exponent.
 *
 * A fixed number of places cannot serve this: BTC's one-tick spread is 0.0016
 * bp and reads as `0.0` at one place, while an illiquid perp's is three digits
 * and reads as `120.0`. Both are the same statement — "this is how tight it is"
 * — and both have to survive it.
 */
function significant(value: number): string {
  if (!Number.isFinite(value) || value === 0) return "0";
  const places = Math.min(6, Math.max(0, 1 - Math.floor(Math.log10(Math.abs(value)))));
  return value.toFixed(places);
}

/** The places the quotes themselves carry, for an instrument with no rules. */
function quotedDecimals(fact: MarketFact | undefined): number {
  if (!fact) return 2;
  return Math.max(fractionDigits(fact.bestAsk), fractionDigits(fact.bestBid));
}

/** `1` → 0, `10` → 1, `100` → 2. The grid multiplier, as places to give up. */
function exponent(multiple: number): number {
  return Math.round(Math.log10(multiple));
}

function fractionDigits(value: string): number {
  const dot = value.indexOf(".");
  return dot < 0 ? 0 : value.length - dot - 1;
}
