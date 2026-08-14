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

    const palette = readPalette();
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

    const palette = readPalette();
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

interface Palette {
  dim: string;
  line: string;
  lineStrong: string;
  raised: string;
  up: string;
  down: string;
  upFaint: string;
  downFaint: string;
}

/**
 * Resolves the design tokens to concrete colours.
 *
 * Read from the live computed style rather than hard-coded, so the chart cannot
 * drift from `theme.css` — there is exactly one definition of "up" in this
 * product and it is not in this file.
 */
function readPalette(): Palette {
  const styles = getComputedStyle(document.documentElement);
  const token = (name: string) => srgb(styles.getPropertyValue(name).trim());
  const up = token("--bt-up");
  const down = token("--bt-down");
  return {
    dim: token("--bt-dim"),
    line: token("--bt-line"),
    lineStrong: token("--bt-line-strong"),
    raised: token("--bt-raised"),
    up,
    down,
    // Volume bars sit behind the price and must not compete with it.
    upFaint: fade(up, 0.45),
    downFaint: fade(down, 0.45),
  };
}

/**
 * Hands the browser a colour and takes back the sRGB bytes it would paint.
 *
 * The library parses colours with its own parser, which knows hex, `rgb()` and
 * `hsl()` and nothing newer — handed the `oklch()` the tokens are written in it
 * throws, and a throw inside `createChart` takes the whole route down with it.
 * Reading a painted pixel back is the only conversion that cannot fall behind
 * the colour syntax: `fillStyle` echoes `oklch()` straight back, but a pixel is
 * always eight bits per channel.
 */
let swatch: CanvasRenderingContext2D | null | undefined;

function channels(color: string): [number, number, number, number] | null {
  swatch ??= document.createElement("canvas").getContext("2d", { willReadFrequently: true });
  if (!swatch) return null;
  swatch.clearRect(0, 0, 1, 1);
  // Assigning an unparseable value is a no-op, so a colour this browser does
  // not understand paints the seed rather than whatever the last token was.
  swatch.fillStyle = "#000000";
  swatch.fillStyle = color;
  swatch.fillRect(0, 0, 1, 1);
  const [red = 0, green = 0, blue = 0, alpha = 255] = swatch.getImageData(0, 0, 1, 1).data;
  return [red, green, blue, alpha];
}

function srgb(color: string): string {
  const parts = channels(color);
  if (!parts) return color;
  const [red, green, blue, alpha] = parts;
  return alpha === 255
    ? `rgb(${red}, ${green}, ${blue})`
    : `rgba(${red}, ${green}, ${blue}, ${(alpha / 255).toFixed(3)})`;
}

/** Alpha applied here rather than with `color-mix()`, for the same reason. */
function fade(color: string, alpha: number): string {
  const parts = channels(color);
  if (!parts) return color;
  const [red, green, blue] = parts;
  return `rgba(${red}, ${green}, ${blue}, ${alpha})`;
}
