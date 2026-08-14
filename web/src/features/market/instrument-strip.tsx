import * as Popover from "@radix-ui/react-popover";
import { Command } from "cmdk";
import { Check, ChevronDown } from "lucide-react";
import { useMemo, useState } from "react";

import type { CandlePage, Instrument, MarketFact, RateLimitStatus } from "@/api/types";
import { Metric } from "@/components/ui/panel";
import { Num } from "@/components/ui/num";
import { formatDecimal } from "@/lib/format";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * The instrument strip.
 *
 * One row across the top of the terminal: which market is on screen, and the
 * handful of numbers an operator checks before touching the ticket. It is the
 * only place the instrument changes, so the selector lives here rather than in
 * a sidebar the chart and the book would each have to listen to.
 *
 * "Today" means the current UTC day, because that is the bar the venue returns
 * for a `1d` interval — not a rolling 24 hours. Labelling it "24h" would be a
 * quietly wrong claim about data we did not ask for.
 * ------------------------------------------------------------------------- */

export function InstrumentStrip({
  instruments,
  instrument,
  onSelect,
  fact,
  today,
  budget,
  loading,
}: {
  instruments: Instrument[];
  instrument: Instrument | null;
  onSelect: (next: Instrument) => void;
  fact: MarketFact | undefined;
  /** A `1d` page of exactly one bar: the session's open, high, low and volume. */
  today: CandlePage | undefined;
  /** The venue's remaining allowance for this account, from the last read. */
  budget: RateLimitStatus | undefined;
  loading: boolean;
}) {
  const bar = today?.candles.at(-1);
  const last = bar?.close ?? fact?.markPrice ?? fact?.bestBid;
  const change = bar ? percentChange(bar.open, bar.close) : null;

  return (
    <div className="flex h-14 shrink-0 items-center gap-6 overflow-x-auto border-b border-line bg-panel px-3">
      <InstrumentPicker instruments={instruments} instrument={instrument} onSelect={onSelect} loading={loading} />

      <Metric
        label="Last"
        value={<Num value={last} />}
        unit={instrument?.quoteAsset}
        tone={change === null ? undefined : change >= 0 ? "up" : "down"}
        className="w-36"
      />
      <Metric
        label="Today"
        value={change === null ? "—" : `${change >= 0 ? "+" : "−"}${Math.abs(change).toFixed(2)}%`}
        tone={change === null ? undefined : change >= 0 ? "up" : "down"}
        className="w-24"
      />

      <Readout label="Open" value={bar?.open} />
      <Readout label="High" value={bar?.high} />
      <Readout label="Low" value={bar?.low} />
      <Readout label="Volume" value={bar?.volume} decimals={2} />
      <Readout label="Mark" value={fact?.markPrice} />
      {fact?.fundingRate ? <Readout label="Funding" value={fact.fundingRate} decimals={6} /> : null}
      {fact?.openInterest ? <Readout label="Open interest" value={fact.openInterest} decimals={2} /> : null}

      {/* The budget being spent belongs to the venue, not to us. Showing what is
          left is the only warning an operator gets before a poll starts coming
          back rate-limited. */}
      {budget ? (
        <div className="ml-auto shrink-0 pl-6 text-right">
          <div className="th">Venue budget</div>
          <div className="mt-0.5 text-base text-muted" data-numeric="">
            {budget.remaining}
            <span className="text-dim">/{budget.limit}</span>
          </div>
        </div>
      ) : null}
    </div>
  );
}

function Readout({ label, value, decimals }: { label: string; value?: string; decimals?: number }) {
  return (
    <div className="shrink-0">
      <div className="th">{label}</div>
      <div className="mt-0.5 text-base text-text" data-numeric="">
        {formatDecimal(value, { decimals })}
      </div>
    </div>
  );
}

/**
 * The one place in the product that turns two decimal strings into a number.
 *
 * Same licence the chart adapter has, and for the same reason: a percentage
 * shown to two places is a pixel, not a value. Nothing computed here is ever
 * sent back to the server or typed into the ticket — those paths carry the
 * original strings untouched.
 */
function percentChange(from: string, to: string): number | null {
  const open = Number.parseFloat(from);
  const close = Number.parseFloat(to);
  if (!Number.isFinite(open) || !Number.isFinite(close) || open === 0) return null;
  return ((close - open) / open) * 100;
}

/* --- The selector -------------------------------------------------------- */

/**
 * A catalog can run to thousands of instruments, so filtering is ours rather
 * than cmdk's and the result is capped: rendering three thousand rows to show
 * the ten that match costs a frame on every keystroke.
 */
const VISIBLE_MATCHES = 60;

function InstrumentPicker({
  instruments,
  instrument,
  onSelect,
  loading,
}: {
  instruments: Instrument[];
  instrument: Instrument | null;
  onSelect: (next: Instrument) => void;
  loading: boolean;
}) {
  const [open, setOpen] = useState(false);
  const [search, setSearch] = useState("");

  const matches = useMemo(() => {
    const needle = search.trim().toLowerCase();
    const hits = needle
      ? instruments.filter(
          (candidate) =>
            candidate.instrumentId.toLowerCase().includes(needle) ||
            candidate.symbol.toLowerCase().includes(needle) ||
            candidate.venue.toLowerCase().includes(needle),
        )
      : instruments;
    return hits.slice(0, VISIBLE_MATCHES);
  }, [instruments, search]);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        className={cn(
          "flex h-10 shrink-0 items-center gap-2 rounded-md border border-line-strong px-2.5 text-left",
          "hover:bg-raised focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
        )}
        disabled={loading && instruments.length === 0}
      >
        <span className="min-w-0">
          <span className="block truncate text-title font-medium text-text">
            {instrument?.instrumentId ?? (loading ? "Loading…" : "Select instrument")}
          </span>
          <span className="block truncate text-micro tracking-wide text-dim uppercase">
            {instrument ? `${instrument.venue} · ${instrument.product} · ${instrument.environment}` : "catalog"}
          </span>
        </span>
        <ChevronDown className="size-4 shrink-0 text-dim" aria-hidden="true" />
      </Popover.Trigger>

      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={6}
          className="z-40 w-96 overflow-hidden rounded-lg border border-line-strong bg-panel shadow-[var(--bt-shadow)]"
        >
          <Command shouldFilter={false} label="Instruments">
            <Command.Input
              value={search}
              onValueChange={setSearch}
              placeholder="Filter by symbol or venue…"
              className="h-bar w-full border-b border-line bg-transparent px-3 text-base text-text placeholder:text-dim focus:outline-none"
            />
            <Command.List className="max-h-80 overflow-y-auto p-1">
              <Command.Empty className="px-2.5 py-6 text-center text-base text-muted">
                No instrument matches.
              </Command.Empty>
              {matches.map((candidate) => (
                <Command.Item
                  key={`${candidate.venue}:${candidate.environment}:${candidate.instrumentId}`}
                  value={`${candidate.venue}:${candidate.environment}:${candidate.instrumentId}`}
                  onSelect={() => {
                    onSelect(candidate);
                    setOpen(false);
                  }}
                  className="flex h-row cursor-pointer items-center gap-2 rounded-sm px-2.5 text-base text-muted data-[selected=true]:bg-raised data-[selected=true]:text-text"
                >
                  <Check
                    className={cn(
                      "size-3.5 shrink-0",
                      candidate.instrumentId === instrument?.instrumentId && candidate.venue === instrument?.venue
                        ? "text-accent"
                        : "opacity-0",
                    )}
                    aria-hidden="true"
                  />
                  <span className="flex-1 truncate">{candidate.instrumentId}</span>
                  <span className="shrink-0 text-micro tracking-wide text-dim uppercase">
                    {candidate.venue} · {candidate.product}
                  </span>
                  {/* An instrument the catalog marks not-ready still lists: the
                      operator should see it exists and is unavailable, rather
                      than search for something that seems to have vanished. */}
                  {candidate.ready ? null : <span className="shrink-0 text-micro text-warn">halted</span>}
                </Command.Item>
              ))}
            </Command.List>
          </Command>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
