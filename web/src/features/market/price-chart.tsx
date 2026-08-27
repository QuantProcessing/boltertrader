import {
  CandlestickSeries,
  HistogramSeries,
  createChart,
  type CandlestickData,
  type HistogramData,
  type IChartApi,
  type IPriceLine,
  type ISeriesApi,
  LineStyle,
  MismatchDirection,
  type MouseEventHandler,
  type Time,
  type UTCTimestamp,
} from "lightweight-charts";
import { useEffect, useRef } from "react";

import { CANDLE_INTERVALS, type AccountPosition, type Candle, type CandleInterval, type Instrument } from "@/api/types";
import { Num } from "@/components/ui/num";
import type { LiveMarkPrice } from "@/features/market/stream";
import { readChartPalette } from "@/lib/chart-palette";
import { useT } from "@/lib/i18n";
import { cn } from "@/lib/utils";

import { precisionOf } from "./precision";

/* ---------------------------------------------------------------------------
 * The price chart.
 *
 * This file is the one sanctioned place where a decimal string becomes a
 * `number`. Everywhere else that would be a lie about precision; here it is the
 * unavoidable cost of drawing — a canvas takes floats, and a price rendered to
 * a pixel has already lost more than a double ever will. Nothing computed in
 * this file leaves it.
 *
 * The chart instance is imperative and lives outside React's render: creating
 * it once and feeding it data on change is both what the library expects and
 * what keeps a two-second poll from remounting a canvas.
 * ------------------------------------------------------------------------- */

const INITIAL_VISIBLE_BARS = 170;

export function PriceChart({
  candles,
  liveCandle,
  mark,
  instrument,
  positions = [],
  interval,
  onIntervalChange,
}: {
  candles: Candle[] | undefined;
  /** The venue's forming bar, delivered by the public WebSocket. */
  liveCandle?: Candle | null;
  /** Public mark-price stream used by PnL and rendered independently from the last traded candle. */
  mark?: LiveMarkPrice | null;
  instrument: Instrument | null;
  /** Current selected-account positions; entry and venue liquidation prices are drawn. */
  positions?: AccountPosition[];
  interval: CandleInterval;
  onIntervalChange: (next: CandleInterval) => void;
}) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const priceRef = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volumeRef = useRef<ISeriesApi<"Histogram"> | null>(null);
  const liveLineRef = useRef<IPriceLine | null>(null);
  const markLineRef = useRef<IPriceLine | null>(null);
  const positionLinesRef = useRef(new Map<string, IPriceLine>());
  const fittedRef = useRef("");
  const t = useT();
  const latestHistory = candles?.at(-1);
  const latest = isSameOrNewerCandle(liveCandle, latestHistory) ? liveCandle : latestHistory;
  const currentLine = currentCandlePriceLine(latest);
  const markLine = markPriceLine(mark);
  const { price, quantity } = precisionOf(instrument);

  // Built once. The library takes resolved colours rather than CSS variables,
  // so a chart normally has to be torn down and rebuilt whenever the palette
  // moves — which, with a single scheme, it never does.
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
        vertLines: { color: palette.line },
        horzLines: { color: palette.line },
      },
      rightPriceScale: { borderColor: palette.line },
      timeScale: {
        borderColor: palette.line,
        timeVisible: true,
        secondsVisible: false,
        rightOffset: 6,
        barSpacing: 7,
        minBarSpacing: 3,
        fixLeftEdge: true,
      },
      crosshair: {
        vertLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
        horzLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
      },
      autoSize: true,
    });

    const price = chart.addSeries(CandlestickSeries, {
      upColor: palette.up,
      downColor: palette.down,
      borderVisible: false,
      wickUpColor: palette.up,
      wickDownColor: palette.down,
      // Candles keep their historical close, while the terminal's one live
      // price label comes from the shared book midpoint below.
      lastValueVisible: false,
      priceLineVisible: false,
    });

    // An overlay rather than a second pane: volume is context for the bar above
    // it, and giving it a pane of its own would halve the price chart to say so.
    const volume = chart.addSeries(HistogramSeries, {
      priceScaleId: "",
      priceFormat: { type: "volume" },
      color: palette.lineStrong,
    });
    volume.priceScale().applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });

    // The built-in Magnet mode snaps the horizontal price to candle data, but
    // it still lets the vertical line sit in the empty logical range after the
    // latest bar. That makes the crosshair look like it names a future candle.
    // Pull whitespace positions back onto the nearest real candle instead.
    const snapCrosshairToCandle: MouseEventHandler<Time> = (param) => {
      const direction = crosshairWhitespaceDirection(
        param.time !== undefined,
        param.logical,
        param.point !== undefined,
      );
      if (direction === null || param.logical === undefined) return;
      const candle = price.dataByIndex(param.logical, direction);
      if (!candle || !("close" in candle)) return;
      chart.setCrosshairPosition(candle.close, candle.time, price);
    };
    chart.subscribeCrosshairMove(snapCrosshairToCandle);

    chartRef.current = chart;
    priceRef.current = price;
    volumeRef.current = volume;

    return () => {
      chart.unsubscribeCrosshairMove(snapCrosshairToCandle);
      chart.remove();
      chartRef.current = null;
      priceRef.current = null;
      volumeRef.current = null;
      liveLineRef.current = null;
      markLineRef.current = null;
      positionLinesRef.current.clear();
    };
  }, []);

  useEffect(() => {
    const price = priceRef.current;
    const volume = volumeRef.current;
    if (!price || !volume || !candles) return;

    const palette = readChartPalette();
    const bars: CandlestickData[] = [];
    const volumes: HistogramData[] = [];

    for (const candle of candles) {
      const time = toChartTime(candle.openTime);
      if (time === null) continue;
      const open = Number.parseFloat(candle.open);
      const close = Number.parseFloat(candle.close);
      const high = Number.parseFloat(candle.high);
      const low = Number.parseFloat(candle.low);
      const size = Number.parseFloat(candle.volume);
      if (!Number.isFinite(open) || !Number.isFinite(close) || !Number.isFinite(high) || !Number.isFinite(low)) {
        continue;
      }
      bars.push({ time, open, high, low, close });
      volumes.push({
        time,
        value: Number.isFinite(size) ? size : 0,
        color: close >= open ? palette.upFaint : palette.downFaint,
      });
    }

    price.setData(bars);
    volume.setData(volumes);
    const fitKey = `${instrument?.venue ?? ""}:${instrument?.instrumentId ?? ""}:${interval}`;
    if (bars.length > 0 && fittedRef.current !== fitKey) {
      fittedRef.current = fitKey;
      chartRef.current?.timeScale().setVisibleLogicalRange({
        from: Math.max(-0.5, bars.length - INITIAL_VISIBLE_BARS),
        to: bars.length + 6,
      });
    }
  }, [candles, instrument?.instrumentId, instrument?.venue, interval]);

  // Forming bars are hot data. `update` replaces the bar when its timestamp is
  // unchanged and appends it when a new interval starts, which is exactly the
  // WebSocket contract and avoids rebuilding the complete series per tick.
  useEffect(() => {
    const price = priceRef.current;
    const volume = volumeRef.current;
    if (!price || !volume || !liveCandle || !isSameOrNewerCandle(liveCandle, latestHistory)) return;

    const time = toChartTime(liveCandle.openTime);
    if (time === null) return;
    const open = Number.parseFloat(liveCandle.open);
    const close = Number.parseFloat(liveCandle.close);
    const high = Number.parseFloat(liveCandle.high);
    const low = Number.parseFloat(liveCandle.low);
    const size = Number.parseFloat(liveCandle.volume);
    if (!Number.isFinite(open) || !Number.isFinite(close) || !Number.isFinite(high) || !Number.isFinite(low)) return;

    const palette = readChartPalette();
    price.update({ time, open, high, low, close });
    volume.update({
      time,
      value: Number.isFinite(size) ? size : 0,
      color: close >= open ? palette.upFaint : palette.downFaint,
    });
  }, [latestHistory, liveCandle]);

  useEffect(() => {
    const series = priceRef.current;
    if (!series) return;
    if (!currentLine) {
      if (liveLineRef.current) series.removePriceLine(liveLineRef.current);
      liveLineRef.current = null;
      return;
    }
    const palette = readChartPalette();
    const options = {
      // The close is always one edge of the candle body: the top for an up
      // candle and the bottom for a down candle. Using the book midpoint here
      // detached the line from the bar it was visually describing.
      price: currentLine.price,
      color: currentLine.direction === "down" ? palette.down : palette.up,
      lineWidth: 1 as const,
      lineStyle: LineStyle.Dotted,
      axisLabelVisible: true,
      title: "",
    };
    if (liveLineRef.current) liveLineRef.current.applyOptions(options);
    else liveLineRef.current = series.createPriceLine(options);
  }, [currentLine?.direction, currentLine?.price]);

  useEffect(() => {
    const series = priceRef.current;
    if (!series) return;
    if (!markLine) {
      if (markLineRef.current) series.removePriceLine(markLineRef.current);
      markLineRef.current = null;
      return;
    }
    const palette = readChartPalette();
    const options = {
      price: markLine.price,
      color: markLine.stale ? palette.dim : palette.muted,
      lineWidth: 1 as const,
      lineStyle: LineStyle.Dashed,
      axisLabelVisible: true,
      title: [t("chart.markPrice"), markLine.stale ? t("chart.position.stale") : ""].filter(Boolean).join(" · "),
    };
    if (markLineRef.current) markLineRef.current.applyOptions(options);
    else markLineRef.current = series.createPriceLine(options);
  }, [markLine?.price, markLine?.stale, t]);

  useEffect(() => {
    const series = priceRef.current;
    if (!series) return;
    const palette = readChartPalette();
    const next = positionLineDescriptors(positions);
    const keys = new Set(next.map((line) => line.key));

    for (const [key, line] of positionLinesRef.current) {
      if (keys.has(key)) continue;
      series.removePriceLine(line);
      positionLinesRef.current.delete(key);
    }

    for (const descriptor of next) {
      const sideColor = descriptor.side === "sell" ? palette.down : palette.up;
      const options = {
        price: descriptor.price,
        color: descriptor.stale ? palette.dim : descriptor.kind === "liquidation" ? palette.down : sideColor,
        lineWidth: 1 as const,
        lineStyle: descriptor.kind === "liquidation" ? LineStyle.Dashed : LineStyle.Solid,
        axisLabelVisible: true,
        title: [
          descriptor.stale ? t("chart.position.stale") : "",
          t(descriptor.kind === "liquidation" ? "chart.position.liquidation" : descriptor.side === "sell" ? "chart.position.short" : "chart.position.long"),
          descriptor.kind === "entry" ? descriptor.quantity : "",
          descriptor.accountLabel,
        ].filter(Boolean).join(" · "),
      };
      const existing = positionLinesRef.current.get(descriptor.key);
      if (existing) existing.applyOptions(options);
      else positionLinesRef.current.set(descriptor.key, series.createPriceLine(options));
    }
  }, [positions, t]);

  useEffect(() => {
    const series = priceRef.current;
    const increment = instrument?.rules.priceIncrement;
    const minMove = typeof increment === "string" ? Number.parseFloat(increment) : Number.NaN;
    if (!series || price === undefined || !Number.isFinite(minMove) || minMove <= 0) return;
    // The price line and the book already share one midpoint; binding the
    // series to the venue tick also makes their visible decimal width agree.
    series.applyOptions({ priceFormat: { type: "price", precision: price, minMove } });
  }, [instrument?.rules.priceIncrement, price]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-row shrink-0 items-center justify-between gap-3 border-b border-line px-3">
        <div className="flex min-w-0 items-baseline gap-2">
          <h2 className="text-title font-semibold text-text">{t("chart.price")}</h2>
          <span className="truncate text-meta text-dim" data-numeric="">
            {instrument ? `${instrument.symbol} · ${instrument.venue}` : "—"}
          </span>
        </div>
        <div className="flex items-center rounded-card bg-fill p-0.5" role="radiogroup" aria-label={t("chart.interval")}>
          {CANDLE_INTERVALS.map((option) => (
            <button
              key={option}
              type="button"
              role="radio"
              aria-checked={interval === option}
              onClick={() => onIntervalChange(option)}
              className={cn(
                "h-[26px] rounded-control px-2 text-meta font-medium transition-colors",
                interval === option ? "bg-raised text-text" : "text-dim hover:text-muted",
              )}
            >
              {option}
            </button>
          ))}
        </div>
      </div>
      <div className="flex h-7 shrink-0 items-center gap-3 px-3 text-meta text-dim">
        <span className="font-medium text-muted" data-numeric="">
          {instrument?.baseAsset ?? "—"} · {interval}
        </span>
        <LegendValue label="O" value={latest?.open} decimals={price} />
        <LegendValue label="H" value={latest?.high} decimals={price} />
        <LegendValue label="L" value={latest?.low} decimals={price} />
        <LegendValue label="C" value={latest?.close} decimals={price} />
        <LegendValue label="V" value={latest?.volume} decimals={quantity} />
      </div>
      <div
        ref={containerRef}
        className="min-h-0 flex-1"
        role="img"
        aria-label={t("chart.label", { interval })}
      />
    </div>
  );
}

export interface PositionLineDescriptor {
  key: string;
  price: number;
  side: "buy" | "sell";
  quantity: string;
  kind: "entry" | "liquidation";
  accountLabel: string;
  stale: boolean;
}

export interface CurrentCandlePriceLine {
  price: number;
  direction: "up" | "down";
}

export interface MarkPriceLine {
  price: number;
  stale: boolean;
}

/** The current-price line belongs to the forming candle's closing body edge. */
export function currentCandlePriceLine(candle: Candle | null | undefined): CurrentCandlePriceLine | null {
  if (!candle) return null;
  const open = Number.parseFloat(candle.open);
  const close = Number.parseFloat(candle.close);
  if (!Number.isFinite(open) || !Number.isFinite(close)) return null;
  return { price: close, direction: close < open ? "down" : "up" };
}

/** Mark is a separate reference line; zero and malformed values remain absent. */
export function markPriceLine(mark: Pick<LiveMarkPrice, "price" | "stale"> | null | undefined): MarkPriceLine | null {
  const price = Number.parseFloat(mark?.price ?? "");
  if (!Number.isFinite(price) || price <= 0) return null;
  return { price, stale: mark?.stale ?? false };
}

/** Position overlays exclude flat, unavailable rows and all order history by construction. */
export function positionLineDescriptors(positions: AccountPosition[]): PositionLineDescriptor[] {
  const descriptors: PositionLineDescriptor[] = [];
  for (const position of positions) {
    const quantity = Number.parseFloat(position.quantity);
    if (!Number.isFinite(quantity) || quantity === 0 || position.freshness === "unavailable") continue;
    const side = position.side ?? (quantity < 0 ? "sell" : "buy");
    const identity = `${position.accountId}:${position.routeId ?? ""}:${position.instrumentId}:${position.positionSlot ?? "net"}`;
    const common = {
      side,
      quantity: position.quantity.replace(/^-/, ""),
      accountLabel: position.accountLabel || position.accountId,
      stale: position.freshness === "stale",
    };
    const entry = Number.parseFloat(position.entryPrice);
    if (Number.isFinite(entry) && entry > 0) descriptors.push({ key: `${identity}:entry`, price: entry, kind: "entry", ...common });
    const liquidation = Number.parseFloat(position.liquidationPrice ?? "");
    if (Number.isFinite(liquidation) && liquidation > 0) descriptors.push({ key: `${identity}:liquidation`, price: liquidation, kind: "liquidation", ...common });
  }
  return descriptors;
}

function isSameOrNewerCandle(candidate: Candle | null | undefined, reference: Candle | undefined): candidate is Candle {
  if (!candidate) return false;
  if (!reference) return true;
  const candidateTime = Date.parse(candidate.openTime);
  const referenceTime = Date.parse(reference.openTime);
  return Number.isFinite(candidateTime) && Number.isFinite(referenceTime) && candidateTime >= referenceTime;
}

/** Which edge candle should own a crosshair currently sitting in whitespace. */
export function crosshairWhitespaceDirection(
  hasTime: boolean,
  logical: number | undefined,
  hasPoint: boolean,
): MismatchDirection | null {
  if (hasTime || !hasPoint || logical === undefined || !Number.isFinite(logical)) return null;
  return logical < 0 ? MismatchDirection.NearestRight : MismatchDirection.NearestLeft;
}

function LegendValue({ label, value, decimals }: { label: string; value: string | undefined; decimals: number | undefined }) {
  return (
    <span className="flex items-baseline gap-1">
      <span>{label}</span>
      <Num value={value} decimals={decimals} className="text-muted" />
    </span>
  );
}

/** RFC3339 in, seconds since epoch out. Null when the server sent something else. */
function toChartTime(iso: string): UTCTimestamp | null {
  const at = Date.parse(iso);
  return Number.isNaN(at) ? null : ((at / 1000) as UTCTimestamp);
}
