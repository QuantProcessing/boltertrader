import {
  CandlestickSeries,
  HistogramSeries,
  createChart,
  type CandlestickData,
  type HistogramData,
  type IChartApi,
  type ISeriesApi,
  type UTCTimestamp,
} from "lightweight-charts";
import { useEffect, useRef } from "react";

import { CANDLE_INTERVALS, type Candle, type CandleInterval } from "@/api/types";
import { Segmented } from "@/components/ui/segmented";
import { readChartPalette } from "@/lib/chart-palette";
import { useTheme } from "@/app/theme";

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

const INTERVAL_OPTIONS = CANDLE_INTERVALS.map((interval) => ({ value: interval, label: interval }));

export function PriceChart({
  candles,
  interval,
  onIntervalChange,
}: {
  candles: Candle[] | undefined;
  interval: CandleInterval;
  onIntervalChange: (next: CandleInterval) => void;
}) {
  const { resolved } = useTheme();
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const priceRef = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volumeRef = useRef<ISeriesApi<"Histogram"> | null>(null);

  // Rebuilt when the theme flips, because the library takes resolved colours
  // rather than CSS variables and will not repaint on a class change.
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
      timeScale: { borderColor: palette.line, timeVisible: true, secondsVisible: false },
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
    });

    // An overlay rather than a second pane: volume is context for the bar above
    // it, and giving it a pane of its own would halve the price chart to say so.
    const volume = chart.addSeries(HistogramSeries, {
      priceScaleId: "",
      priceFormat: { type: "volume" },
      color: palette.lineStrong,
    });
    volume.priceScale().applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });

    chartRef.current = chart;
    priceRef.current = price;
    volumeRef.current = volume;

    return () => {
      chart.remove();
      chartRef.current = null;
      priceRef.current = null;
      volumeRef.current = null;
    };
  }, [resolved]);

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
  }, [candles]);

  // Only on an instrument or interval change. Re-fitting on every poll would
  // undo whatever the operator had panned or zoomed to two seconds earlier.
  useEffect(() => {
    chartRef.current?.timeScale().fitContent();
  }, [interval, candles?.[0]?.openTime]);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex h-row shrink-0 items-center justify-between gap-3 border-b border-line px-3">
        <h2 className="text-base font-medium text-text">Price</h2>
        <Segmented
          name="interval"
          label="Candle interval"
          value={interval}
          options={INTERVAL_OPTIONS}
          onChange={onIntervalChange}
          className="w-auto"
        />
      </div>
      <div ref={containerRef} className="min-h-0 flex-1" role="img" aria-label={`Candlestick chart, ${interval} bars`} />
    </div>
  );
}

/** RFC3339 in, seconds since epoch out. Null when the server sent something else. */
function toChartTime(iso: string): UTCTimestamp | null {
  const at = Date.parse(iso);
  return Number.isNaN(at) ? null : ((at / 1000) as UTCTimestamp);
}
