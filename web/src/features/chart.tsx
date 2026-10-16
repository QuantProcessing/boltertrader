import { CandlestickSeries, HistogramSeries, createChart, type IChartApi, type ISeriesApi, type UTCTimestamp } from "lightweight-charts";
import { useCallback, useEffect, useRef, useState } from "react";

import { api } from "@/api/client";
import type { Candle } from "@/api/types";
import { decimals } from "@/lib/format";
import { useCandleStream, useInstrument } from "@/state/hooks";
import { useWorkspace } from "@/state/workspace";
import { cx } from "@/ui/primitives";

const INTERVALS = ["1m", "5m", "15m", "1h", "4h", "1d"] as const;
const UP = "#1fa67d";
const DOWN = "#ed7088";

export function ChartPanel() {
  const { instrumentId } = useWorkspace();
  const instrument = useInstrument(instrumentId);
  const [interval, setIntervalValue] = useState<string>(() => localStorage.getItem("bolter.interval") ?? "15m");
  const host = useRef<HTMLDivElement>(null);
  const chart = useRef<IChartApi | null>(null);
  const candles = useRef<ISeriesApi<"Candlestick"> | null>(null);
  const volume = useRef<ISeriesApi<"Histogram"> | null>(null);
  const [status, setStatus] = useState<"loading" | "ready" | "error">("loading");

  useEffect(() => {
    if (!host.current) return;
    const instance = createChart(host.current, {
      autoSize: true,
      layout: { background: { color: "#1b2429" }, textColor: "#949e9c", fontSize: 11, fontFamily: "Inter Variable, system-ui", attributionLogo: false },
      grid: { vertLines: { color: "#22302f" }, horzLines: { color: "#22302f" } },
      rightPriceScale: { borderColor: "#273035" },
      timeScale: { borderColor: "#273035", timeVisible: true, secondsVisible: false },
      crosshair: { mode: 0 },
    });
    candles.current = instance.addSeries(CandlestickSeries, {
      upColor: UP, downColor: DOWN, borderUpColor: UP, borderDownColor: DOWN, wickUpColor: UP, wickDownColor: DOWN,
    });
    volume.current = instance.addSeries(HistogramSeries, { priceFormat: { type: "volume" }, priceScaleId: "volume" });
    instance.priceScale("volume").applyOptions({ scaleMargins: { top: 0.82, bottom: 0 } });
    chart.current = instance;
    return () => {
      instance.remove();
      chart.current = null;
    };
  }, []);

  useEffect(() => {
    if (!candles.current || !instrument) return;
    const precision = decimals(instrument.tickSize);
    candles.current.applyOptions({ priceFormat: { type: "price", precision, minMove: Number(instrument.tickSize) } });
  }, [instrument]);

  useEffect(() => {
    const controller = new AbortController();
    setStatus("loading");
    candles.current?.setData([]);
    volume.current?.setData([]);
    api.candles(instrumentId, interval, undefined, controller.signal).then((rows) => {
      candles.current?.setData(rows.map(toBar));
      volume.current?.setData(rows.map(toVolume));
      chart.current?.timeScale().scrollToRealTime();
      setStatus("ready");
    }).catch((error) => {
      if (!controller.signal.aborted) {
        console.warn("candles", error);
        setStatus("error");
      }
    });
    return () => controller.abort();
  }, [instrumentId, interval]);

  const onCandle = useCallback((candle: Candle) => {
    try {
      candles.current?.update(toBar(candle));
      volume.current?.update(toVolume(candle));
    } catch {
      // An out-of-order update (older than the last bar) is ignored.
    }
  }, []);
  useCandleStream(instrumentId, interval, onCandle);

  return (
    <div className="flex min-h-0 flex-1 flex-col bg-surface">
      <div className="flex h-9 shrink-0 items-center gap-1 border-b border-line px-2">
        {INTERVALS.map((value) => (
          <button
            key={value}
            onClick={() => { setIntervalValue(value); localStorage.setItem("bolter.interval", value); }}
            className={cx("rounded px-2 py-1", value === interval ? "text-accent" : "text-muted hover:text-text")}
          >
            {value}
          </button>
        ))}
        {status === "error" && <span className="ml-auto text-down">Chart data unavailable</span>}
      </div>
      <div className="relative min-h-0 flex-1">
        <div ref={host} className="absolute inset-0" />
        {status === "loading" && <div className="absolute inset-0 flex items-center justify-center text-muted">Loading chart…</div>}
      </div>
    </div>
  );
}

function toBar(candle: Candle) {
  return { time: Math.floor(candle.t / 1000) as UTCTimestamp, open: Number(candle.o), high: Number(candle.h), low: Number(candle.l), close: Number(candle.c) };
}

function toVolume(candle: Candle) {
  return { time: Math.floor(candle.t / 1000) as UTCTimestamp, value: Number(candle.v), color: Number(candle.c) >= Number(candle.o) ? "rgba(31,166,125,0.45)" : "rgba(237,112,136,0.45)" };
}
