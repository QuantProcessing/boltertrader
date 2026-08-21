import { AreaSeries, createChart, type IChartApi, type ISeriesApi, type LineData } from "lightweight-charts";
import { useEffect, useRef } from "react";

import { fadeColor, readChartPalette } from "@/lib/chart-palette";

export function HistoryNavigator({ points, label, tone = "neutral" }: { points: LineData[]; label: string; tone?: "neutral" | "up" | "down" }) {
  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const seriesRef = useRef<ISeriesApi<"Area"> | null>(null);

  useEffect(() => {
    const container = containerRef.current;
    if (!container) return;
    const palette = readChartPalette();
    const line = tone === "up" ? palette.up : tone === "down" ? palette.down : palette.muted;
    const chart = createChart(container, {
      autoSize: true,
      layout: { background: { color: "transparent" }, textColor: palette.dim, attributionLogo: false },
      grid: { vertLines: { visible: false }, horzLines: { visible: false } },
      leftPriceScale: { visible: false },
      rightPriceScale: { visible: false },
      timeScale: { visible: false, borderVisible: false },
      crosshair: { mode: 0 },
      handleScroll: false,
      handleScale: false,
    });
    const series = chart.addSeries(AreaSeries, {
      lineColor: line,
      lineWidth: 1,
      topColor: tone === "up" ? palette.upFaint : tone === "down" ? palette.downFaint : fadeColor(palette.muted, 0.18),
      bottomColor: "transparent",
      priceLineVisible: false,
      lastValueVisible: false,
    });
    chartRef.current = chart;
    seriesRef.current = series;
    return () => {
      chart.remove();
      chartRef.current = null;
      seriesRef.current = null;
    };
  }, [tone]);

  useEffect(() => {
    seriesRef.current?.setData(points);
    chartRef.current?.timeScale().fitContent();
  }, [points]);

  return (
    <div className="h-[58px] shrink-0 border-t border-line px-3 py-1.5">
      <div ref={containerRef} className="h-full rounded-control border border-line-strong bg-fill" role="img" aria-label={label} />
    </div>
  );
}
