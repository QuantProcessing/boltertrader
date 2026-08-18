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
    <div className="flex h-bar shrink-0 items-center gap-6 overflow-x-auto border-b border-line bg-panel px-3">
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
 * Ranks, strongest first. Ties break on the catalog's own order — the venue's.
 *
 * A venue match admits an instrument on its own, so typing `okx` lists that
 * venue, but it ranks below every symbol match: someone typing `btc` wants BTC
 * markets, not the instruments of a venue whose name happens to contain those
 * letters.
 */
const SCORE = {
  exactSymbol: 4,
  symbolPrefix: 3,
  symbolSubstring: 2,
  instrumentId: 1,
  venue: 0,
} as const;

/**
 * How many rows the list spends in total, divided evenly among the venues that
 * matched.
 *
 * design.md §10 forbids silent truncation, not truncation: a catalog runs to
 * thousands of instruments, and rendering all of them to show the ten that match
 * costs a frame on every keystroke. What the rule is actually protecting is that
 * every venue stays on screen — so the budget is split per venue rather than
 * taken off the top, and whatever it drops is counted in the group it was
 * dropped from.
 *
 * Splitting rather than fixing the per-venue cap matters when one venue lists
 * forty things that match: alone it gets the whole budget, so nothing is out of
 * reach; sharing the screen with nine others it gets a fair slice and says so.
 */
const LIST_BUDGET = 48;
const MIN_PER_VENUE = 4;

export interface VenueGroup {
  venue: string;
  matches: Ranked[];
  /** Matched but not listed, so the count can be shown rather than dropped. */
  hidden: number;
  /** The group's strongest match, which is what orders the groups. */
  best: number;
}

/** An instrument with why it matched and where the catalog had it. */
interface Ranked {
  instrument: Instrument;
  score: number;
  /** Position in the catalog, which is the venue's own order. */
  index: number;
}

/** Separators folded out, so `btcusdt` finds `BTC-USDT`. */
function fold(value: string): string {
  return value.toLowerCase().replace(/[-_/:]/g, "");
}

function scoreOf(candidate: Instrument, needle: string, folded: string): number | null {
  const symbol = fold(candidate.symbol);
  if (symbol === folded) return SCORE.exactSymbol;
  if (symbol.startsWith(folded)) return SCORE.symbolPrefix;
  if (symbol.includes(folded)) return SCORE.symbolSubstring;
  if (candidate.instrumentId.toLowerCase().includes(needle)) return SCORE.instrumentId;
  if (candidate.venue.toLowerCase().includes(needle)) return SCORE.venue;
  return null;
}

/**
 * The catalog as a ranked list per venue.
 *
 * Grouping is the whole point rather than a presentational choice: "which venue
 * lists this" is the question the picker is asked, and a flat list answers it
 * only if you scroll far enough to prove a venue is absent. Groups are ordered
 * by their strongest match so the venue holding an exact hit leads, and within a
 * group the catalog's own order survives every tie.
 *
 * Exported for its own test: design.md §10 fixes the ranking, and a rule written
 * down in two places drifts unless one of them fails.
 */
export function groupByVenue(instruments: Instrument[], search: string): VenueGroup[] {
  const needle = search.trim().toLowerCase();
  const folded = fold(search.trim());
  const groups = new Map<string, VenueGroup>();

  instruments.forEach((candidate, index) => {
    const score = needle ? scoreOf(candidate, needle, folded) : SCORE.venue;
    if (score === null) return;
    const group = groups.get(candidate.venue) ?? { venue: candidate.venue, matches: [], hidden: 0, best: score };
    group.best = Math.max(group.best, score);
    group.matches.push({ instrument: candidate, score, index });
    groups.set(candidate.venue, group);
  });

  const ordered = [...groups.values()];
  const perVenue = Math.max(MIN_PER_VENUE, Math.floor(LIST_BUDGET / Math.max(1, ordered.length)));
  for (const group of ordered) {
    group.matches.sort((left, right) => right.score - left.score || left.index - right.index);
    if (group.matches.length > perVenue) {
      group.hidden = group.matches.length - perVenue;
      group.matches = group.matches.slice(0, perVenue);
    }
  }

  return ordered.sort((left, right) => right.best - left.best || left.venue.localeCompare(right.venue));
}

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

  const groups = useMemo(() => groupByVenue(instruments, search), [instruments, search]);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        className={cn(
          "flex h-10 shrink-0 items-center gap-2 rounded-control border border-line-strong px-2.5 text-left",
          "hover:bg-raised focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
        )}
        disabled={loading && instruments.length === 0}
      >
        {/* The symbol leads here for the same reason it leads in the list: it is
            what an operator reads to know which market is on screen. The full
            instrument id is the identity rather than the label, so it stays
            reachable as a title instead of taking the headline. */}
        <span className="min-w-0" title={instrument?.instrumentId}>
          <span className="block truncate text-title font-medium text-text" data-numeric="">
            {instrument?.symbol ?? (loading ? "Loading…" : "Select instrument")}
          </span>
          <span className="block truncate th">
            {instrument
              ? `${instrument.venue} · ${instrument.product}${instrument.settlement ? ` · ${instrument.settlement}` : ""}`
              : "catalog"}
          </span>
        </span>
        {/* `faint` rather than `dim`: §3 forbids faint on anything a human reads,
            and permits it on exactly this — a decorative affordance glyph that
            is hidden from assistive technology and carries no meaning the label
            beside it does not already carry. */}
        <ChevronDown className="size-4 shrink-0 text-faint" aria-hidden="true" />
      </Popover.Trigger>

      <Popover.Portal>
        <Popover.Content
          align="start"
          sideOffset={6}
          className="z-40 w-96 overflow-hidden rounded-modal border border-line-strong bg-overlay shadow-overlay"
        >
          <Command shouldFilter={false} label="Instruments">
            <Command.Input
              value={search}
              onValueChange={setSearch}
              placeholder="Symbol or venue — btc, okx, eth-usdt…"
              className="h-bar w-full border-b border-line bg-transparent px-3 text-base text-text placeholder:text-dim focus:outline-none"
            />
            <Command.List className="max-h-96 overflow-y-auto p-1">
              <Command.Empty className="px-2.5 py-6 text-center text-base text-muted">
                No instrument matches.
              </Command.Empty>
              {groups.map((group) => (
                <Command.Group
                  key={group.venue}
                  heading={
                    <span className="flex items-center justify-between gap-2">
                      <span>{group.venue}</span>
                      {group.hidden > 0 ? <span className="normal-case">+{group.hidden} more</span> : null}
                    </span>
                  }
                  // Pinned, because the venue is the answer being looked for and
                  // scrolling past the heading would take it off screen exactly
                  // when the list is long enough to need it.
                  className="[&_[cmdk-group-heading]]:th [&_[cmdk-group-heading]]:sticky [&_[cmdk-group-heading]]:top-0 [&_[cmdk-group-heading]]:z-10 [&_[cmdk-group-heading]]:bg-overlay [&_[cmdk-group-heading]]:px-2.5 [&_[cmdk-group-heading]]:py-1.5"
                >
                  {group.matches.map(({ instrument: candidate }) => (
                    <Command.Item
                      key={`${candidate.venue}:${candidate.environment}:${candidate.instrumentId}`}
                      value={`${candidate.venue}:${candidate.environment}:${candidate.instrumentId}`}
                      onSelect={() => {
                        onSelect(candidate);
                        setOpen(false);
                      }}
                      className="flex h-row cursor-pointer items-center gap-2 rounded-control px-2.5 text-base text-muted data-[selected=true]:bg-raised data-[selected=true]:text-text"
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
                      {/* The symbol leads, because it is what was typed. The
                          instrument id is what identifies the market, so it
                          follows rather than being dropped. */}
                      <span className="shrink-0 font-medium text-text" data-numeric="">
                        {candidate.symbol}
                      </span>
                      <span className="th min-w-0 flex-1 truncate">
                        {candidate.product}
                        {candidate.settlement ? ` · ${candidate.settlement}` : ""}
                      </span>
                      {/* An instrument the catalog marks not-ready still lists:
                          the operator should see it exists and is unavailable,
                          rather than search for something that seems to have
                          vanished. */}
                      {candidate.ready ? null : <span className="shrink-0 text-micro text-warn">halted</span>}
                    </Command.Item>
                  ))}
                </Command.Group>
              ))}
            </Command.List>
          </Command>
        </Popover.Content>
      </Popover.Portal>
    </Popover.Root>
  );
}
