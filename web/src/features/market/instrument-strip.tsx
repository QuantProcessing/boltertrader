import * as Popover from "@radix-ui/react-popover";
import { Command } from "cmdk";
import { Check, ChevronDown, Coins } from "lucide-react";
import { useMemo, useState } from "react";

import type { CandlePage, Instrument, MarketFact } from "@/api/types";
import { formatDecimal } from "@/lib/format";
import { useT } from "@/lib/i18n";
import { instrumentDisplaySymbol } from "@/lib/instrument";
import { cn } from "@/lib/utils";

import { precisionOf } from "./precision";

/* ---------------------------------------------------------------------------
 * The instrument strip.
 *
 * One row across the top of the terminal: which market is on screen, and the
 * handful of numbers an operator checks before touching the ticket. It is the
 * only place the instrument changes, so the selector lives here rather than in
 * a sidebar the chart and the book would each have to listen to.
 *
 * Every readout is the same shape. It used to be two: `Metric` at 22px on a
 * fixed `w-36` for the price, and a 13px pair for everything else. Both halves
 * were wrong on real data — 22px of leading plus a label is the whole 44px bar
 * with nothing left over, and 36 fixed pixels is four digits short of what
 * binance quotes — so the two numbers an operator reads first were the two that
 * clipped. Emphasis is carried by weight and colour now, which cost no height,
 * and the widths are minimums.
 *
 * Prices are stated to the venue's own tick. Same rule and same source as the
 * order book: `Instrument.rules.priceIncrement`, counted through `precision.ts`.
 * Without it this bar was rendering `64 532.11000000`, which is not a claim the
 * venue made about its accuracy — it is the width binance pads its strings to.
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
  loading,
}: {
  instruments: Instrument[];
  instrument: Instrument | null;
  onSelect: (next: Instrument) => void;
  fact: MarketFact | undefined;
  /** A `1d` page of exactly one bar: the session's open, high, low and volume. */
  today: CandlePage | undefined;
  loading: boolean;
}) {
  const bar = today?.candles.at(-1);
  const last = bar?.close;
  const change = bar ? percentChange(bar.open, bar.close) : null;
  const { price } = precisionOf(instrument);
  const tone = change === null ? undefined : change >= 0 ? "up" : "down";
  const t = useT();

  return (
    <div className="instrument-strip shrink-0">
      <div className="flex shrink-0 items-center pl-3 pr-2">
        <InstrumentPicker instruments={instruments} instrument={instrument} onSelect={onSelect} loading={loading} />
      </div>

      {/* The two numbers the instrument is chosen for, ruled off from the rest.
          §11 rule 4 bars a border between rows inside a panel; this is the seam
          between two clusters of a bar, which is the same job the top nav's
          `border-l` already does beside the account menu. */}
      <Cluster fixed>
        {last ? (
          <Readout label={t("strip.last")} value={last} decimals={price} unit={instrument?.quoteAsset} tone={tone} strong />
        ) : null}
        {fact?.markPrice ? <Readout label={t("strip.mark")} value={fact.markPrice} decimals={price} /> : null}
        {fact?.indexPrice ? <Readout label={t("col.index")} value={fact.indexPrice} decimals={price} /> : null}
        {change === null ? null : (
          <Readout
            label={t("strip.today")}
            value={`${Math.abs(change).toFixed(2)}`}
            prefix={change >= 0 ? "+" : "−"}
            unit="%"
            tone={tone}
            strong
          />
        )}
      </Cluster>

      <Cluster>
        {bar?.volume ? (
          <Readout
            label={t("strip.volume")}
            value={bar.volume}
            displayValue={compactDecimal(bar.volume)}
            unit={instrument?.baseAsset}
          />
        ) : null}
        {fact?.openInterest ? (
          <Readout
            label={t("strip.openInterest")}
            value={fact.openInterest}
            displayValue={compactDecimal(fact.openInterest)}
            unit={instrument?.baseAsset}
          />
        ) : null}
      </Cluster>
    </div>
  );
}

/** Related readouts share a scrollable group on narrow screens. */
function Cluster({ children, fixed = false }: { children: React.ReactNode; fixed?: boolean }) {
  return (
    <div
      className={cn(
        "flex items-center gap-4 overflow-x-auto border-l border-line px-3",
        fixed ? "shrink-0" : "min-w-0",
      )}
    >
      {children}
    </div>
  );
}

/**
 * Label over value, at the density of a table cell.
 *
 * `strong` is the whole of the emphasis: 585 on the value and full-strength
 * text, against 400 and `--muted` for the rest. §6 binds 700 to `--text-metric`
 * and nothing else, so this cannot borrow that weight — and at 44px there is no
 * room for that size anyway.
 */
function Readout({
  label,
  value,
  displayValue,
  decimals,
  unit,
  prefix,
  tone,
  strong = false,
}: {
  label: string;
  value?: string | null;
  /** Display-only formatting for percentages, countdowns and magnitudes. */
  displayValue?: string;
  decimals?: number;
  unit?: string;
  /** A sign that belongs to the value but is not part of the decimal string. */
  prefix?: string;
  tone?: "up" | "down";
  strong?: boolean;
}) {
  return (
    <div className="min-w-0 shrink-0">
      <div className="th truncate">{label}</div>
      <div className="flex items-baseline gap-1">
        <span
          data-numeric=""
          className={cn(
            "truncate text-base",
            strong ? "font-semibold" : "",
            tone === "up" && "text-up",
            tone === "down" && "text-down",
            tone === undefined && (strong ? "text-text" : "text-muted"),
          )}
        >
          {prefix}
          {displayValue ?? formatDecimal(value ?? undefined, { decimals })}
        </span>
        {/* Dropped along with the value: a bare `USDT` next to an em dash reads
            as a fact about a price that has not arrived. */}
        {unit && value ? <span className="shrink-0 text-micro text-dim">{unit}</span> : null}
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

/** Compact display only; the original decimal string never leaves the query. */
function compactDecimal(value: string | undefined): string | undefined {
  if (!value) return undefined;
  const parsed = Number.parseFloat(value);
  if (!Number.isFinite(parsed)) return undefined;
  const absolute = Math.abs(parsed);
  const units: Array<[number, string]> = [
    [1_000_000_000, "B"],
    [1_000_000, "M"],
    [1_000, "K"],
  ];
  for (const [threshold, suffix] of units) {
    if (absolute >= threshold) return `${formatDecimal((parsed / threshold).toFixed(2))}${suffix}`;
  }
  return formatDecimal(value, { decimals: absolute >= 10 ? 2 : 4 });
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
  const t = useT();

  const groups = useMemo(() => groupByVenue(instruments, search), [instruments, search]);

  return (
    <Popover.Root open={open} onOpenChange={setOpen}>
      <Popover.Trigger
        className={cn(
          // Two text lines need an explicit 36px box. `h-8` follows the tighter
          // global spacing base and resolved below 29px, leaving the symbol and
          // venue line pressed against both borders.
          "flex h-[36px] w-[196px] shrink-0 items-center gap-2 rounded-control border border-line-strong px-2",
          "hover:bg-raised focus-visible:outline-2 focus-visible:outline-offset-2 focus-visible:outline-accent",
        )}
        disabled={loading && instruments.length === 0}
      >
        {/* The symbol leads here for the same reason it leads in the list: it is
            what an operator reads to know which market is on screen. The full
            instrument id is the identity rather than the label, so it stays
            reachable as a title instead of taking the headline. */}
        <span className="flex size-5 shrink-0 items-center justify-center rounded-full bg-fill text-muted">
          <Coins className="size-3.5" aria-hidden="true" />
        </span>
        <span className="min-w-0 flex-1 text-left" title={instrument?.instrumentId}>
          <span className="block truncate text-base font-semibold leading-4 text-text" data-numeric="">
            {instrument ? instrumentDisplaySymbol(instrument) : loading ? t("strip.loading") : t("strip.select")}
          </span>
          <span className="block truncate text-micro leading-[13px] text-dim">
            {instrument
              ? `${instrument.venue} · ${instrument.product}${instrument.settlement ? ` · ${instrument.settlement}` : ""}`
              : t("strip.catalog")}
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
          <Command shouldFilter={false} label={t("strip.instruments")}>
            <Command.Input
              value={search}
              onValueChange={setSearch}
              placeholder={t("strip.search")}
              className="h-bar w-full border-b border-line bg-transparent px-3 text-base text-text placeholder:text-dim focus:outline-none"
            />
            <Command.List className="max-h-96 overflow-y-auto p-1">
              <Command.Empty className="px-2.5 py-6 text-center text-base text-muted">
                {t("strip.empty")}
              </Command.Empty>
              {groups.map((group) => (
                <Command.Group
                  key={group.venue}
                  heading={
                    <span className="flex items-center justify-between gap-2">
                      <span>{group.venue}</span>
                      {group.hidden > 0 ? (
                        <span className="normal-case">{t("strip.more", { count: group.hidden })}</span>
                      ) : null}
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
                        {instrumentDisplaySymbol(candidate)}
                      </span>
                      <span className="th min-w-0 flex-1 truncate">
                        {candidate.product}
                        {candidate.settlement ? ` · ${candidate.settlement}` : ""}
                      </span>
                      {/* An instrument the catalog marks not-ready still lists:
                          the operator should see it exists and is unavailable,
                          rather than search for something that seems to have
                          vanished. */}
                      {candidate.ready ? null : (
                        <span className="shrink-0 text-micro text-warn">{t("strip.halted")}</span>
                      )}
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
