import { useMemo } from "react";

import type { DepthLevel, MarketFact } from "@/api/types";
import { formatDecimal } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The order book.
 *
 * Asks descend from the top, bids descend from the middle, and the spread sits
 * between them — the arrangement every venue uses, so an operator's eye already
 * knows where the touch is. Clicking a level writes its price into the ticket,
 * which is the whole reason the book is beside the ticket rather than under the
 * chart.
 *
 * The depth bars are the second sanctioned float in the product, after the
 * chart: a bar's width is a pixel count. The price and quantity *text* is still
 * the untouched string from the server, and clicking a row hands that same
 * string to the ticket — the number is never the thing that gets traded.
 * ------------------------------------------------------------------------- */

const DEPTH_ROWS = 12;

export function OrderBook({
  fact,
  quoteAsset,
  baseAsset,
  onPickPrice,
}: {
  fact: MarketFact | undefined;
  quoteAsset?: string;
  baseAsset?: string;
  onPickPrice: (price: string) => void;
}) {
  const asks = useMemo(() => cumulate(fact?.asks ?? []), [fact?.asks]);
  const bids = useMemo(() => cumulate(fact?.bids ?? []), [fact?.bids]);
  const scale = Math.max(asks.at(-1)?.total ?? 0, bids.at(-1)?.total ?? 0);

  const spread = fact ? spreadOf(fact) : null;

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-row shrink-0 items-center justify-between border-b border-line px-3">
        <h2 className="text-base font-medium text-text">Order book</h2>
        <span className="th">
          {baseAsset ?? "size"} / {quoteAsset ?? "price"}
        </span>
      </div>

      <div className="th grid shrink-0 grid-cols-3 gap-2 px-3 py-1">
        <span>Price</span>
        <span className="text-right">Size</span>
        <span className="text-right">Total</span>
      </div>

      <div className="flex min-h-0 flex-1 flex-col justify-end overflow-hidden">
        {/* Reversed so the best ask is adjacent to the spread, not at the top
            of the screen where it would be furthest from the price it is
            closest to. */}
        {asks.slice(0, DEPTH_ROWS).reverse().map((level) => (
          <Row key={`ask-${level.price}`} level={level} scale={scale} side="ask" onPick={onPickPrice} />
        ))}
      </div>

      <div className="flex h-row shrink-0 items-center justify-between border-y border-line px-3">
        <span data-numeric="" className="text-title font-medium text-text">
          {formatDecimal(fact?.bestAsk)}
        </span>
        <span className="text-meta text-dim">
          {spread === null ? "—" : `spread ${spread}`}
        </span>
      </div>

      <div className="flex min-h-0 flex-1 flex-col overflow-hidden">
        {bids.slice(0, DEPTH_ROWS).map((level) => (
          <Row key={`bid-${level.price}`} level={level} scale={scale} side="bid" onPick={onPickPrice} />
        ))}
      </div>

      {fact ? null : (
        <div className="flex flex-1 items-center justify-center text-meta text-dim">Waiting for depth…</div>
      )}
    </div>
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
  onPick,
}: {
  level: CumulativeLevel;
  scale: number;
  side: "bid" | "ask";
  onPick: (price: string) => void;
}) {
  const width = scale > 0 ? Math.min(100, (level.total / scale) * 100) : 0;
  return (
    <button
      type="button"
      onClick={() => onPick(level.price)}
      className="relative grid h-[22px] w-full shrink-0 grid-cols-3 items-center gap-2 px-3 text-left hover:bg-raised focus-visible:outline-2 focus-visible:-outline-offset-2 focus-visible:outline-accent"
    >
      <span
        aria-hidden="true"
        style={{ width: `${width}%` }}
        className={cn("absolute inset-y-0 right-0", side === "bid" ? "bg-up/12" : "bg-down/12")}
      />
      <span data-numeric="" className={cn("relative", side === "bid" ? "text-up" : "text-down")}>
        {formatDecimal(level.price)}
      </span>
      <span data-numeric="" className="relative text-right text-muted">
        {formatDecimal(level.quantity)}
      </span>
      <span data-numeric="" className="relative text-right text-dim">
        {level.totalLabel}
      </span>
    </button>
  );
}

/**
 * Running depth. See the file header on why the float is acceptable: `total` is
 * consumed only as a bar width and as its own rounded label, never as an order
 * quantity.
 */
function cumulate(levels: DepthLevel[]): CumulativeLevel[] {
  let running = 0;
  return levels.map((level) => {
    const size = Number.parseFloat(level.quantity);
    running += Number.isFinite(size) ? size : 0;
    return { ...level, total: running, totalLabel: running.toFixed(running >= 1000 ? 0 : 3) };
  });
}

/** Best ask minus best bid, to the precision the prices themselves carry. */
function spreadOf(fact: MarketFact): string | null {
  const ask = Number.parseFloat(fact.bestAsk);
  const bid = Number.parseFloat(fact.bestBid);
  if (!Number.isFinite(ask) || !Number.isFinite(bid)) return null;
  const decimals = Math.max(fractionDigits(fact.bestAsk), fractionDigits(fact.bestBid));
  return (ask - bid).toFixed(decimals);
}

function fractionDigits(value: string): number {
  const dot = value.indexOf(".");
  return dot < 0 ? 0 : value.length - dot - 1;
}
