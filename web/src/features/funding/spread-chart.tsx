import type { UseQueryResult } from "@tanstack/react-query";
import {
  LineSeries,
  LineStyle,
  createChart,
  type IChartApi,
  type ISeriesApi,
  type LineData,
  type UTCTimestamp,
} from "lightweight-charts";
import { useEffect, useMemo, useRef, useState } from "react";

import type { Metered } from "@/api/endpoints";
import { describeError, isApiError } from "@/api/errors";
import {
  FUNDING_HISTORY_INSTRUMENT_UNKNOWN,
  FUNDING_HISTORY_UNSUPPORTED,
  FUNDING_WINDOWS,
  type FundingHistoryPoint,
  type FundingHistorySeries,
  type FundingWindow,
} from "@/api/types";
import { Segmented, type SegmentedOption } from "@/components/ui/segmented";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useFundingHistory } from "@/features/funding/queries";
import { readChartPalette } from "@/lib/chart-palette";
import { cn } from "@/lib/utils";

/* ---------------------------------------------------------------------------
 * Both legs' settled funding, read back from the venues themselves.
 *
 * Every point here has already been paid. That is what makes the chart worth
 * reading before opening a hedge: the board says what the two venues are
 * quoting for the next settlement, and one quote apiece is not enough to tell a
 * spread that has held for a week from one that opened this morning.
 *
 * The history is fetched per venue and per underlying because no venue offers a
 * bulk history call — which is also why this lives on a chosen pair rather than
 * on the matrix, where it would be hundreds of underlyings times ten venues.
 * Two calls here; the matrix would be thousands.
 *
 * Both legs are drawn as a daily rate, normalised from each venue's own funding
 * interval, because that is the only way two venues on different settlement
 * clocks belong on one axis — an eight-hourly 0.01% and an hourly 0.01% are not
 * the same rate, and drawn raw they would cross and diverge for no reason but
 * the clock. The unit matches the board and the ticket.
 *
 * The two lines are coloured the way sides are coloured everywhere else — the
 * long leg up-green, the short leg down-red — and the legend names each leg's
 * venue rather than leaving the colour to carry the meaning alone.
 *
 * A leg that cannot be drawn is said out loud. One board column publishes no
 * history at all, and a venue that answers nothing looks exactly like a venue
 * whose funding was flat if the chart simply omits it. The other leg renders
 * regardless: half a spread is still worth seeing, as long as it is labelled as
 * half.
 * ------------------------------------------------------------------------- */

const WINDOW_OPTIONS: SegmentedOption<FundingWindow>[] = FUNDING_WINDOWS.map((value) => ({
  value,
  label: value,
}));

/** Why a leg has no line, in the board's own vocabulary for the same condition. */
interface LegNote {
  tone: StateTone;
  /** Shown beside the venue in the legend. */
  label: string;
  /** The sentence, in the footer and to assistive technology. */
  detail: string;
}

interface LegView {
  points: LineData[];
  note: LegNote | null;
}

export function SpreadChart({
  symbol,
  longVenue,
  longEnvironment,
  shortVenue,
  shortEnvironment,
}: {
  /** The underlying both legs quote, e.g. `BTC-USDT`. */
  symbol: string;
  longVenue: string;
  longEnvironment: string;
  shortVenue: string;
  shortEnvironment: string;
}) {
  const [historyWindow, setHistoryWindow] = useState<FundingWindow>("7d");

  // Two queries rather than one paired read: a venue that publishes no history
  // fails permanently, and its leg has to be able to say so beside a leg that
  // drew. Coupled, the missing half would take the whole chart down.
  const longHistory = useFundingHistory({
    venue: longVenue,
    environment: longEnvironment,
    symbol,
    window: historyWindow,
  });
  const shortHistory = useFundingHistory({
    venue: shortVenue,
    environment: shortEnvironment,
    symbol,
    window: historyWindow,
  });

  const longLeg = useMemo(
    () => readLeg(longHistory),
    [longHistory.data, longHistory.error, longHistory.isPending],
  );
  const shortLeg = useMemo(
    () => readLeg(shortHistory),
    [shortHistory.data, shortHistory.error, shortHistory.isPending],
  );

  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const longRef = useRef<ISeriesApi<"Line"> | null>(null);
  const shortRef = useRef<ISeriesApi<"Line"> | null>(null);

  // Built once, for the same reason as the price chart: the library holds
  // resolved colours, and with a single scheme they never move.
  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;

    const palette = readChartPalette();
    const chart = createChart(container, {
      layout: {
        background: { color: "transparent" },
        textColor: palette.dim,
        fontSize: 11,
        attributionLogo: false,
      },
      grid: {
        vertLines: { visible: false },
        horzLines: { color: palette.line },
      },
      rightPriceScale: { borderColor: palette.line },
      // No seconds: the densest funding schedule on the board settles hourly, so
      // a seconds field would be four characters of noise on every label.
      timeScale: { borderColor: palette.line, timeVisible: true, secondsVisible: false },
      crosshair: {
        vertLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
        horzLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
      },
      autoSize: true,
    });

    const format = {
      // Percent per day, which is the unit the board and the ticket both quote.
      // Four decimals because a hedge worth opening can live in the fourth.
      type: "custom" as const,
      formatter: (value: number) => `${value.toFixed(4)}%`,
      minMove: 0.0001,
    };

    const long = chart.addSeries(LineSeries, {
      color: palette.up,
      lineWidth: 2,
      priceLineVisible: false,
      priceFormat: format,
    });
    const short = chart.addSeries(LineSeries, {
      color: palette.down,
      lineWidth: 2,
      priceLineVisible: false,
      priceFormat: format,
    });

    // Zero is the only meaningful reference on this chart: which side of it a
    // leg sits on decides who paid whom at that settlement.
    long.createPriceLine({
      price: 0,
      color: palette.lineStrong,
      lineWidth: 1,
      lineStyle: LineStyle.Dashed,
      axisLabelVisible: false,
      title: "",
    });

    chartRef.current = chart;
    longRef.current = long;
    shortRef.current = short;

    return () => {
      chart.remove();
      chartRef.current = null;
      longRef.current = null;
      shortRef.current = null;
    };
  }, []);

  useEffect(() => {
    const long = longRef.current;
    const short = shortRef.current;
    if (!long || !short) return;

    long.setData(longLeg.points);
    short.setData(shortLeg.points);
    // Re-fit on every change, unlike the price chart. The data only changes when
    // the pair or the window changes, and both of those replace the span
    // outright — there is nothing an operator could have panned to that a fit
    // would take away.
    chartRef.current?.timeScale().fitContent();
  }, [longLeg.points, shortLeg.points]);

  const drew = longLeg.points.length > 0 || shortLeg.points.length > 0;
  const pending = longHistory.isPending || shortHistory.isPending;

  const notes: string[] = [];
  if (longLeg.note) notes.push(`${longVenue}: ${longLeg.note.detail}`);
  if (shortLeg.note) notes.push(`${shortVenue}: ${shortLeg.note.detail}`);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-row shrink-0 items-center justify-between gap-3 border-b border-line px-3">
        <h2 className="shrink-0 text-base font-medium text-text">Funding history</h2>
        <div className="flex min-w-0 items-center gap-3 overflow-x-auto">
          <div className="flex items-center gap-3 text-meta text-dim">
            <LegendLeg side="Long" venue={longVenue} tone="up" note={longLeg.note} />
            <LegendLeg side="Short" venue={shortVenue} tone="down" note={shortLeg.note} />
          </div>
          <Segmented
            name="funding-window"
            label="History window"
            value={historyWindow}
            options={WINDOW_OPTIONS}
            onChange={setHistoryWindow}
            className="w-auto shrink-0"
          />
        </div>
      </div>

      <div className="relative min-h-0 flex-1">
        <div
          ref={containerRef}
          className="absolute inset-0"
          role="img"
          aria-label={`Settled daily funding rate for ${symbol} over the last ${historyWindow}, ${longVenue} against ${shortVenue}`}
        />
        {drew ? null : (
          <div className="absolute inset-0 flex flex-col items-center justify-center gap-1 bg-panel px-6 text-center">
            <p className="text-base text-dim">{pending ? "Reading settled funding" : "Neither leg has a history to draw"}</p>
            <p className="text-meta text-dim">
              {pending ? `The last ${historyWindow} from ${longVenue} and ${shortVenue}.` : notes.join(" ")}
            </p>
          </div>
        )}
      </div>

      <div className="shrink-0 border-t border-line px-3 py-1.5 text-meta text-dim">
        <p>
          Each settlement, normalised to a daily rate from that venue&rsquo;s own funding interval — the same unit as
          the board. Read back from the venues; the rate being quoted now is on the ticket.
        </p>
        {drew && notes.length > 0 ? (
          <p className="text-warn" role="status">
            {notes.join(" ")}
          </p>
        ) : null}
      </div>
    </div>
  );
}

/** One legend entry: a swatch nobody has to see, a venue, and a reason if there is one. */
function LegendLeg({
  side,
  venue,
  tone,
  note,
}: {
  side: string;
  venue: string;
  tone: "up" | "down";
  note: LegNote | null;
}) {
  return (
    <span className="flex shrink-0 items-center gap-1.5 whitespace-nowrap">
      <span aria-hidden className={cn("h-0.5 w-3 rounded-full", tone === "up" ? "bg-up" : "bg-down")} />
      {side} {venue}
      {note ? <StateBadge tone={note.tone} label={note.label} detail={note.detail} /> : null}
    </span>
  );
}

/**
 * A leg's query, read as either a line or a reason there isn't one.
 *
 * The two permanent codes are separated from every other failure deliberately.
 * Both are facts rather than moments — the server answers them with
 * `retryable: false` so the shared policy stops asking — and telling an operator
 * "the read failed" about an answer that cannot change would send them to reload
 * a page that will say the same thing.
 *
 * They are also kept apart from each other, because one is a claim about the
 * venue and one is a claim about the symbol. Collapsing them would print "this
 * venue publishes no funding history" under a venue that publishes it for
 * hundreds of other pairs, which is both false and easy for the operator to
 * check.
 */
function readLeg(query: UseQueryResult<Metered<FundingHistorySeries>>): LegView {
  if (query.isPending) {
    return { points: [], note: null };
  }
  if (query.isError) {
    const code = isApiError(query.error) ? query.error.code : "";
    if (code === FUNDING_HISTORY_UNSUPPORTED) {
      return {
        points: [],
        note: {
          tone: "neutral",
          label: "no history",
          detail: "This venue publishes no funding history, so this leg cannot be drawn.",
        },
      };
    }
    if (code === FUNDING_HISTORY_INSTRUMENT_UNKNOWN) {
      return {
        points: [],
        note: {
          tone: "neutral",
          label: "not listed",
          detail: "This venue does not list this instrument, so this leg cannot be drawn.",
        },
      };
    }
    return {
      points: [],
      note: { tone: "danger", label: "unavailable", detail: `The history read failed. ${describeError(query.error)}` },
    };
  }

  const points = toDailySeries(query.data?.value.points);
  if (points.length === 0) {
    return {
      points,
      note: {
        tone: "warn",
        label: "no settlements",
        detail: "This venue settled no funding on this symbol during the window.",
      },
    };
  }
  return { points, note: null };
}

/**
 * Settled rates as a daily percent.
 *
 * A point without an interval is dropped rather than drawn: the rate it carries
 * is for one settlement of unknown length, and there is no honest way to put it
 * on an axis measured in days. At most one point per series is in this state —
 * the oldest, whose interval would have to be measured against a settlement the
 * venue did not return — so the cost is the left edge of the window, not a gap
 * in the middle.
 *
 * The server guarantees the points arrive strictly ascending, which is what the
 * library requires; dropping some of them cannot disturb that.
 */
function toDailySeries(points: FundingHistoryPoint[] | undefined): LineData[] {
  if (!points) return [];
  const data: LineData[] = [];
  for (const point of points) {
    const rate = Number.parseFloat(point.rate);
    const at = Date.parse(point.fundingTime);
    const hours = point.intervalHours;
    if (!Number.isFinite(rate) || Number.isNaN(at) || !hours) continue;
    data.push({ time: (at / 1000) as UTCTimestamp, value: ((rate * 24) / hours) * 100 });
  }
  return data;
}
