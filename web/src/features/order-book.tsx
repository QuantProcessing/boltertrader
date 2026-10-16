import { useMemo, useState } from "react";

import type { Level } from "@/api/types";
import { num, price, time } from "@/lib/format";
import { useBook, useInstrument, useTrades } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { Tabs } from "@/ui/primitives";

const ROWS = 12;

export function BookPanel() {
  const [tab, setTab] = useState<"book" | "trades">("book");
  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <Tabs value={tab} onChange={setTab} options={[{ value: "book", label: "Order Book" }, { value: "trades", label: "Trades" }]} className="[&>button]:flex-1" />
      {tab === "book" ? <OrderBook /> : <TradesList />}
    </div>
  );
}

function cumulate(levels: Level[]): Array<{ price: string; size: string; total: number }> {
  let total = 0;
  return levels.slice(0, ROWS).map(([p, q]) => {
    total += Number(q);
    return { price: p, size: q, total };
  });
}

function OrderBook() {
  const { instrumentId, hintLimitPrice } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const book = useBook(instrumentId);
  const tick = instrument?.tickSize;

  const { asks, bids, max, spread, spreadPct } = useMemo(() => {
    const asks = cumulate(book?.asks ?? []);
    const bids = cumulate(book?.bids ?? []);
    const max = Math.max(asks.at(-1)?.total ?? 0, bids.at(-1)?.total ?? 0) || 1;
    const bestAsk = Number(book?.asks[0]?.[0]);
    const bestBid = Number(book?.bids[0]?.[0]);
    const spread = bestAsk && bestBid ? bestAsk - bestBid : undefined;
    return { asks, bids, max, spread, spreadPct: spread && bestAsk ? (spread / bestAsk) * 100 : undefined };
  }, [book]);

  const row = (level: { price: string; size: string; total: number }, side: "ask" | "bid") => (
    <button
      key={side + level.price}
      onClick={() => hintLimitPrice(level.price)}
      className="relative grid h-6 w-full grid-cols-3 items-center px-3 text-right hover:bg-raised"
    >
      <span
        className={side === "ask" ? "absolute inset-y-0 right-0 bg-down/20" : "absolute inset-y-0 right-0 bg-long/20"}
        style={{ width: `${(level.total / max) * 100}%` }}
      />
      <span className={side === "ask" ? "relative text-left text-down" : "relative text-left text-up"}>{price(level.price, tick)}</span>
      <span className="relative">{num(level.size)}</span>
      <span className="relative">{num(level.total)}</span>
    </button>
  );

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="grid h-7 shrink-0 grid-cols-3 items-center px-3 text-right text-muted">
        <span className="text-left">Price</span>
        <span>Size ({instrument?.base ?? ""})</span>
        <span>Total ({instrument?.base ?? ""})</span>
      </div>
      {!book ? (
        <div className="flex flex-1 items-center justify-center text-muted">Loading book…</div>
      ) : (
        <div className="flex min-h-0 flex-1 flex-col justify-center overflow-hidden">
          <div className="flex flex-col-reverse">{asks.map((level) => row(level, "ask"))}</div>
          <div className="flex h-6 shrink-0 items-center justify-center gap-6 bg-raised/60">
            <span>Spread</span>
            <span>{spread === undefined ? "—" : price(spread, tick)}</span>
            <span>{spreadPct === undefined ? "" : spreadPct.toFixed(3) + "%"}</span>
          </div>
          <div className="flex flex-col">{bids.map((level) => row(level, "bid"))}</div>
        </div>
      )}
    </div>
  );
}

function TradesList() {
  const { instrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const trades = useTrades(instrumentId);
  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="grid h-7 shrink-0 grid-cols-3 items-center px-3 text-right text-muted">
        <span className="text-left">Price</span>
        <span>Size ({instrument?.base ?? ""})</span>
        <span>Time</span>
      </div>
      <div className="min-h-0 flex-1 overflow-y-auto">
        {trades.length === 0 && <div className="p-6 text-center text-muted">Waiting for trades…</div>}
        {trades.map((trade, index) => (
          <div key={index} className="grid h-6 grid-cols-3 items-center px-3 text-right">
            <span className={trade.side === "buy" ? "text-left text-up" : "text-left text-down"}>{price(trade.p, instrument?.tickSize)}</span>
            <span>{num(trade.q)}</span>
            <span className="text-muted">{time(trade.ts).slice(-8)}</span>
          </div>
        ))}
      </div>
    </div>
  );
}
