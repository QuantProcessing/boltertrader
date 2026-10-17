import * as Popover from "@radix-ui/react-popover";
import { useEffect, useMemo, useRef, useState } from "react";

import type { Instrument } from "@/api/types";
import { compact, countdown, pct, price, signed, venueLabel } from "@/lib/format";
import { useInstrument, useInstruments, useLastPrice, useNow, useStats, useTicker } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";

export { venueLabel } from "@/lib/format";

export function MarketHeader() {
  const { instrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const perp = instrument?.product === "perp";
  const stats = useStats(instrumentId).data;
  const ticker = useTicker(instrumentId, perp);
  const last = useLastPrice(instrumentId);
  const now = useNow();

  const open = Number(stats?.open24h);
  const change = last && open ? Number(last) - open : undefined;
  const funding = ticker?.funding ?? stats?.funding;
  const nextFunding = ticker?.nextFunding ?? stats?.nextFunding;
  const tick = instrument?.tickSize;

  return (
    <div className="flex h-14 shrink-0 items-center gap-6 overflow-x-auto bg-surface px-3">
      <InstrumentPicker />
      <Stat label="Price" value={price(last, tick)} />
      {perp && <Stat label="Mark" hint="Price used for margining and liquidation" value={price(ticker?.mark ?? stats?.mark, tick)} />}
      {perp && ticker?.index && <Stat label="Index" hint="Spot index price" value={price(ticker.index, tick)} />}
      <Stat
        label="24h Change"
        value={change === undefined ? "—" : `${signed(change, tick ? Math.min(8, tick.split(".")[1]?.length ?? 0) : 2)} / ${pct(change / open)}`}
        tone={change === undefined ? undefined : change >= 0 ? "up" : "down"}
      />
      <Stat label="24h Volume" value={stats ? compact(Number(stats.volume24h) * Number(last ?? 0)) + " " + (instrument?.quote ?? "") : "—"} />
      {perp && ticker?.oi && <Stat label="Open Interest" value={compact(ticker.oi) + " " + (instrument?.base ?? "")} />}
      {perp && (
        <div className="shrink-0">
          <div className="dotted text-muted" title="Current funding rate / time to next settlement">Funding / Countdown</div>
          <div className="mt-1 flex gap-3">
            <span className={Number(funding) >= 0 ? "text-up" : "text-down"}>{funding ? (Number(funding) * 100).toFixed(4) + "%" : "—"}</span>
            <span>{countdown(nextFunding, now)}</span>
          </div>
        </div>
      )}
    </div>
  );
}

function Stat({ label, value, tone, hint }: { label: string; value: string; tone?: "up" | "down"; hint?: string }) {
  return (
    <div className="shrink-0">
      <div className={cx("text-muted", hint && "dotted")} title={hint}>{label}</div>
      <div className={cx("mt-1", tone === "up" && "text-up", tone === "down" && "text-down")}>{value}</div>
    </div>
  );
}

type Category = "all" | "perp" | "spot" | string;

function InstrumentPicker() {
  const { instrumentId, setInstrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const { data: instruments = [] } = useInstruments();
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");
  const [category, setCategory] = useState<Category>("all");
  const [cursor, setCursor] = useState(0);
  const listRef = useRef<HTMLDivElement>(null);

  useEffect(() => {
    const onKey = (event: KeyboardEvent) => {
      if ((event.metaKey || event.ctrlKey) && event.key.toLowerCase() === "k") {
        event.preventDefault();
        setOpen(true);
      }
    };
    window.addEventListener("keydown", onKey);
    return () => window.removeEventListener("keydown", onKey);
  }, []);

  const venues = useMemo(() => [...new Set(instruments.map((item) => item.venue))], [instruments]);
  const filtered = useMemo(() => {
    const term = search.trim().toUpperCase().replace(/[\s/]+/g, "-");
    const result = instruments.filter((item) => {
      if (category === "perp" || category === "spot") {
        if (item.product !== category) return false;
      } else if (category !== "all" && item.venue !== category) return false;
      return !term || item.symbol.includes(term) || item.base.toUpperCase().startsWith(term);
    });
    // Exact base matches first, then perps, then by symbol.
    const currentVenue = instrument?.venue;
    result.sort((a, b) => rank(a, term, currentVenue) - rank(b, term, currentVenue) || a.symbol.localeCompare(b.symbol));
    return result.slice(0, 200);
  }, [instruments, search, category, instrument?.venue]);

  useEffect(() => setCursor(0), [search, category]);
  useEffect(() => {
    listRef.current?.querySelector(`[data-index="${cursor}"]`)?.scrollIntoView({ block: "nearest" });
  }, [cursor]);

  const choose = (item: Instrument | undefined) => {
    if (!item) return;
    setInstrumentId(item.id);
    setOpen(false);
    setSearch("");
  };

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger className="flex shrink-0 items-center gap-2 text-left">
        <span className="text-lg whitespace-nowrap">{instrument?.symbol ?? instrumentId.split(":").slice(2).join(":")}</span>
        <span className="text-muted">{open ? "▴" : "▾"}</span>
        <span className="rounded-md bg-accent-soft px-1.5 py-0.5 text-accent">{venueLabel(instrument?.venue ?? "")} · {instrument?.product === "perp" ? "Perp" : "Spot"}</span>
      </Popover.Trigger>
      <Popover.Portal>
        <Popover.Content align="start" sideOffset={8} className="z-40 w-[min(760px,calc(100vw-16px))] rounded-[10px] border border-line bg-overlay shadow-2xl">
          <div className="p-3">
            <input
              autoFocus
              value={search}
              onChange={(event) => setSearch(event.target.value)}
              onKeyDown={(event) => {
                if (event.key === "ArrowDown") { event.preventDefault(); setCursor((c) => Math.min(c + 1, filtered.length - 1)); }
                if (event.key === "ArrowUp") { event.preventDefault(); setCursor((c) => Math.max(c - 1, 0)); }
                if (event.key === "Enter") choose(filtered[cursor]);
              }}
              placeholder="Search"
              className="h-9 w-full rounded-lg border border-line-strong bg-transparent px-3 outline-none placeholder:text-muted"
            />
          </div>
          <div className="flex gap-1 overflow-x-auto px-3">
            {[["all", "All"], ["perp", "Perps"], ["spot", "Spot"], ...venues.map((v) => [v, venueLabel(v)])].map(([value, label]) => (
              <button key={value} onClick={() => setCategory(value!)} className={cx("rounded-md px-2 py-1 whitespace-nowrap", category === value ? "bg-raised text-text" : "text-muted hover:text-text")}>
                {label}
              </button>
            ))}
          </div>
          <div className="mt-2 grid grid-cols-[1fr_120px_80px] gap-2 px-3 py-1.5 text-muted">
            <span>Market</span><span>Venue</span><span className="text-right">Type</span>
          </div>
          <div ref={listRef} className="max-h-[50vh] overflow-y-auto pb-2">
            {filtered.map((item, index) => (
              <button
                key={item.id}
                data-index={index}
                onMouseEnter={() => setCursor(index)}
                onClick={() => choose(item)}
                className={cx("grid w-full grid-cols-[1fr_120px_80px] gap-2 px-3 py-1.5 text-left", index === cursor && "bg-raised", item.id === instrumentId && "text-accent")}
              >
                <span className="truncate">{item.symbol}</span>
                <span className="text-text-2">{venueLabel(item.venue)}</span>
                <span className="text-right text-muted">{item.product === "perp" ? "Perp" : "Spot"}</span>
              </button>
            ))}
            {filtered.length === 0 && <div className="px-3 py-6 text-center text-muted">No markets match</div>}
          </div>
          <div className="flex gap-4 border-t border-line px-3 py-2 text-muted">
            <span><kbd className="text-text">⌘K</kbd> Open</span>
            <span><kbd className="text-text">↑↓</kbd> Navigate</span>
            <span><kbd className="text-text">Enter</kbd> Select</span>
            <span><kbd className="text-text">Esc</kbd> Close</span>
          </div>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}

const MAJORS = ["BTC", "ETH", "SOL"];
function rank(item: Instrument, term: string, currentVenue?: string): number {
  let score = 0;
  // Stay on the venue being traded unless the user picks another.
  if (item.venue === currentVenue) score -= 50;
  if (term && item.base.toUpperCase() === term) score -= 100;
  if (term && item.quote.toUpperCase() === "USDT") score -= 1;
  const major = MAJORS.indexOf(item.base.toUpperCase());
  if (major >= 0) score -= 30 - major;
  if (item.product === "perp") score -= 10;
  if (item.symbol.includes(":")) score += 5;
  return score;
}

