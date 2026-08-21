import type { UseQueryResult } from "@tanstack/react-query";
import {
  LineSeries,
  LineStyle,
  createChart,
  type IChartApi,
  type IPriceLine,
  type ISeriesApi,
  type LineData,
  type UTCTimestamp,
} from "lightweight-charts";
import { History } from "lucide-react";
import { useEffect, useMemo, useRef, useState } from "react";

import type { Metered } from "@/api/endpoints";
import type {
  ArbitrageOpportunity,
  ArbitrageOpportunityLeg,
  Candle,
  CandleInterval,
  CandlePage,
  FundingHistoryPoint,
  FundingHistorySeries,
} from "@/api/types";
import { EmptyState } from "@/components/ui/empty-state";
import { Num } from "@/components/ui/num";
import { Segmented } from "@/components/ui/segmented";
import { HistoryNavigator } from "@/features/arbitrage/history-navigator";
import {
  ARBITRAGE_HISTORY_WINDOWS,
  fundingWindowFor,
  trimToArbitrageWindow,
  type ArbitrageHistoryWindow,
} from "@/features/arbitrage/history-window";
import { useFundingHistory } from "@/features/funding/queries";
import { useCandles } from "@/features/market/queries";
import { readChartPalette } from "@/lib/chart-palette";
import { useT } from "@/lib/i18n";

interface HistorySpec {
  interval: CandleInterval;
  limit: number;
}

const PRICE_HISTORY: Record<ArbitrageHistoryWindow, HistorySpec> = {
  "24h": { interval: "15m", limit: 96 },
  "7d": { interval: "1h", limit: 168 },
  "30d": { interval: "4h", limit: 180 },
};

interface Point {
  time: UTCTimestamp;
  value: number;
}

/**
 * Historical context for strategies whose edge is a price relationship.
 *
 * The price line is deliberately called a close-price spread rather than an
 * executable spread: candles do not preserve both books' bid/ask at the same
 * instant. The live preview still replaces the board estimate with BBO before
 * an order can be confirmed. That distinction is the difference between a
 * useful mean-reversion reference and a fictional fill history.
 */
export function StrategyHistory({
  opportunity,
  long,
  short,
}: {
  opportunity: ArbitrageOpportunity;
  long: ArbitrageOpportunityLeg;
  short: ArbitrageOpportunityLeg;
}) {
  const t = useT();
  const [window, setWindow] = useState<ArbitrageHistoryWindow>("7d");
  const spec = PRICE_HISTORY[window];
  const fundingWindow = fundingWindowFor(window);

  const longCandles = useCandles(
    long.instrumentId ? { instrumentId: long.instrumentId, interval: spec.interval, limit: spec.limit } : null,
  );
  const shortCandles = useCandles(
    short.instrumentId ? { instrumentId: short.instrumentId, interval: spec.interval, limit: spec.limit } : null,
  );
  const longFunding = useFundingHistory(
    long.product === "perp" && long.instrumentId ? { instrumentId: long.instrumentId, window: fundingWindow } : null,
  );
  const shortFunding = useFundingHistory(
    short.product === "perp" && short.instrumentId ? { instrumentId: short.instrumentId, window: fundingWindow } : null,
  );

  const edgePoints = useMemo(
    () => trimToArbitrageWindow(priceSpreadSeries(longCandles.data?.value, shortCandles.data?.value), window),
    [longCandles.data, shortCandles.data, window],
  );
  const fundingPoints = useMemo(
    () => trimToArbitrageWindow(fundingContextSeries(opportunity.strategy, longFunding, shortFunding), window),
    [opportunity.strategy, longFunding.data, shortFunding.data, window],
  );
  const average = useMemo(() => mean(edgePoints), [edgePoints]);
  const deviation = useMemo(() => standardDeviation(edgePoints, average), [average, edgePoints]);
  const current = edgePoints.at(-1)?.value;
  const zScore = current !== undefined && average !== null && deviation !== null && deviation > 0
    ? (current - average) / deviation
    : null;

  const containerRef = useRef<HTMLDivElement | null>(null);
  const chartRef = useRef<IChartApi | null>(null);
  const edgeRef = useRef<ISeriesApi<"Line"> | null>(null);
  const fundingRef = useRef<ISeriesApi<"Line"> | null>(null);
  const averageRef = useRef<IPriceLine | null>(null);
  const upperRef = useRef<IPriceLine | null>(null);
  const lowerRef = useRef<IPriceLine | null>(null);

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
      leftPriceScale: { visible: true, borderColor: palette.line },
      timeScale: { borderColor: palette.line, timeVisible: true, secondsVisible: false },
      crosshair: {
        vertLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
        horzLine: { color: palette.lineStrong, labelBackgroundColor: palette.raised },
      },
      autoSize: true,
    });
    const percentFormat = {
      type: "custom" as const,
      formatter: (value: number) => `${value.toFixed(3)}%`,
      minMove: 0.001,
    };
    const edge = chart.addSeries(LineSeries, {
      color: palette.muted,
      lineWidth: 2,
      priceLineVisible: false,
      lastValueVisible: true,
      priceFormat: percentFormat,
      priceScaleId: "right",
    });
    const funding = chart.addSeries(LineSeries, {
      color: palette.dim,
      lineWidth: 1,
      priceLineVisible: false,
      lastValueVisible: true,
      priceFormat: {
        type: "custom" as const,
        formatter: (value: number) => `${value.toFixed(4)}%/d`,
        minMove: 0.0001,
      },
      priceScaleId: "left",
    });
    edge.createPriceLine({
      price: 0,
      color: palette.lineStrong,
      lineWidth: 1,
      lineStyle: LineStyle.Dashed,
      axisLabelVisible: false,
      title: "",
    });
    chartRef.current = chart;
    edgeRef.current = edge;
    fundingRef.current = funding;
    return () => {
      chart.remove();
      chartRef.current = null;
      edgeRef.current = null;
      fundingRef.current = null;
      averageRef.current = null;
      upperRef.current = null;
      lowerRef.current = null;
    };
  }, []);

  useEffect(() => {
    edgeRef.current?.setData(edgePoints);
    fundingRef.current?.setData(fundingPoints);
    if (edgeRef.current && average !== null) {
      const options = {
        price: average,
        color: readChartPalette().lineStrong,
        lineWidth: 1 as const,
        lineStyle: LineStyle.Dotted,
        axisLabelVisible: false,
        title: "",
      };
      if (averageRef.current) averageRef.current.applyOptions(options);
      else averageRef.current = edgeRef.current.createPriceLine(options);
      if (deviation !== null) {
        const band = (price: number) => ({
          price,
          color: readChartPalette().lineStrong,
          lineWidth: 1 as const,
          lineStyle: LineStyle.Dashed,
          axisLabelVisible: false,
          title: "",
        });
        const upper = band(average + deviation);
        const lower = band(average - deviation);
        if (upperRef.current) upperRef.current.applyOptions(upper);
        else upperRef.current = edgeRef.current.createPriceLine(upper);
        if (lowerRef.current) lowerRef.current.applyOptions(lower);
        else lowerRef.current = edgeRef.current.createPriceLine(lower);
      }
    }
    chartRef.current?.timeScale().fitContent();
  }, [average, deviation, edgePoints, fundingPoints]);

  const pending = longCandles.isPending || shortCandles.isPending;
  const noIdentity = !long.instrumentId || !short.instrumentId;
  const title = opportunity.strategy === "spot_perp_basis"
    ? t("arb.history.basis")
    : t("arb.history.priceSpread");
  const rateLabel = opportunity.strategy === "spot_perp_basis"
    ? t("arb.history.shortFunding")
    : t("arb.history.netFunding");

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <div className="flex min-h-row shrink-0 flex-wrap items-center justify-between gap-2 border-b border-line px-3 py-1.5">
        <div className="min-w-0">
          <h2 className="text-base font-medium text-text">{title}</h2>
          <div className="mt-0.5 flex flex-wrap items-center gap-x-3 gap-y-1 text-meta text-dim">
            <Legend label={t("arb.history.closeSpread")} />
            <Legend label={rateLabel} dim />
            <span>{t("arb.history.mean")}: <Num value={average?.toFixed(3)} suffix="%" /></span>
            <span>{t("arb.history.stddev")}: <Num value={deviation?.toFixed(3)} suffix="%" /></span>
            <span>{t("arb.history.zscore")}: <Num value={zScore?.toFixed(2)} /></span>
            <span>{t("arb.history.latest")}: <Num value={current?.toFixed(3)} suffix="%" /></span>
          </div>
        </div>
        <Segmented
          name="price-spread-window"
          label={t("funding.window")}
          value={window}
          options={ARBITRAGE_HISTORY_WINDOWS}
          onChange={setWindow}
          className="w-auto shrink-0"
        />
      </div>

      <div className="relative min-h-0 flex-1 overflow-hidden">
        <div ref={containerRef} className="absolute inset-0" role="img" aria-label={title} />
        {edgePoints.length === 0 && fundingPoints.length === 0 && !pending ? (
          <EmptyState
            icon={History}
            title={t("arb.history.unavailable")}
            detail={noIdentity ? t("arb.history.needInstrument") : t("arb.history.unavailable.detail")}
            className="absolute inset-0 bg-panel"
          />
        ) : null}
      </div>
      {edgePoints.length > 0 || fundingPoints.length > 0 ? (
        <HistoryNavigator points={edgePoints.length > 0 ? edgePoints : fundingPoints} label={t("arb.history.navigator")} />
      ) : null}
      {edgePoints.length === 0 && fundingPoints.length > 0 ? (
        <p className="shrink-0 border-t border-line px-3 py-1.5 text-meta text-warn">{t("arb.history.fundingOnly")}</p>
      ) : null}
    </div>
  );
}

function Legend({ label, dim = false }: { label: string; dim?: boolean }) {
  return (
    <span className="flex items-center gap-1.5">
      <span aria-hidden className={dim ? "h-px w-3 bg-dim" : "h-0.5 w-3 bg-muted"} />
      {label}
    </span>
  );
}

export function priceSpreadSeries(longPage: CandlePage | undefined, shortPage: CandlePage | undefined): Point[] {
  if (!longPage || !shortPage) return [];
  const shortCloses = new Map(shortPage.candles.map((candle) => [candle.openTime, finiteClose(candle)]));
  const points: Point[] = [];
  for (const candle of longPage.candles) {
    const longClose = finiteClose(candle);
    const shortClose = shortCloses.get(candle.openTime);
    const at = Date.parse(candle.openTime);
    if (longClose === null || shortClose === null || shortClose === undefined || longClose === 0 || Number.isNaN(at)) continue;
    points.push({ time: (at / 1000) as UTCTimestamp, value: ((shortClose - longClose) / longClose) * 100 });
  }
  return points;
}

function finiteClose(candle: Candle): number | null {
  const close = Number.parseFloat(candle.close);
  return Number.isFinite(close) ? close : null;
}

function fundingContextSeries(
  strategy: ArbitrageOpportunity["strategy"],
  long: UseQueryResult<Metered<FundingHistorySeries>>,
  short: UseQueryResult<Metered<FundingHistorySeries>>,
): Point[] {
  const shortPoints = dailyFundingPoints(short.data?.value.points);
  if (strategy === "spot_perp_basis") return shortPoints;
  return differenceSeries(dailyFundingPoints(long.data?.value.points), shortPoints);
}

function dailyFundingPoints(points: FundingHistoryPoint[] | undefined): Point[] {
  if (!points) return [];
  const result: Point[] = [];
  for (const point of points) {
    const rate = Number.parseFloat(point.rate);
    const at = Date.parse(point.fundingTime);
    if (!Number.isFinite(rate) || Number.isNaN(at) || !point.intervalHours) continue;
    result.push({ time: (at / 1000) as UTCTimestamp, value: ((rate * 24) / point.intervalHours) * 100 });
  }
  return result;
}

/** Short rate minus long rate, forward-filled at every settlement event. */
export function differenceSeries(long: Point[], short: Point[]): Point[] {
  const events = [
    ...long.map((point) => ({ ...point, leg: "long" as const })),
    ...short.map((point) => ({ ...point, leg: "short" as const })),
  ].sort((left, right) => Number(left.time) - Number(right.time));
  let longValue: number | undefined;
  let shortValue: number | undefined;
  const result: Point[] = [];
  for (const event of events) {
    if (event.leg === "long") longValue = event.value;
    else shortValue = event.value;
    if (longValue !== undefined && shortValue !== undefined) {
      const previous = result.at(-1);
      const next = { time: event.time, value: shortValue - longValue };
      if (previous?.time === next.time) result[result.length - 1] = next;
      else result.push(next);
    }
  }
  return result;
}

function mean(points: LineData[]): number | null {
  if (points.length === 0) return null;
  const total = points.reduce((sum, point) => sum + ("value" in point ? point.value : 0), 0);
  return total / points.length;
}

function standardDeviation(points: LineData[], average: number | null): number | null {
  if (points.length < 2 || average === null) return null;
  const variance = points.reduce((sum, point) => {
    const value = "value" in point ? point.value : average;
    return sum + (value - average) ** 2;
  }, 0) / points.length;
  return Math.sqrt(variance);
}
