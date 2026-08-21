import type { Time } from "lightweight-charts";

import type { FundingWindow } from "@/api/types";
import type { SegmentedOption } from "@/components/ui/segmented";

/**
 * The strategy workbench offers a short diagnostic window without widening the
 * public funding-history contract. Venues are still queried with the smallest
 * complete server window and the settled points are clipped locally.
 */
export type ArbitrageHistoryWindow = "24h" | FundingWindow;

export const ARBITRAGE_HISTORY_WINDOWS: SegmentedOption<ArbitrageHistoryWindow>[] = [
  { value: "24h", label: "24h" },
  { value: "7d", label: "7d" },
  { value: "30d", label: "30d" },
];

export function fundingWindowFor(window: ArbitrageHistoryWindow): FundingWindow {
  return window === "24h" ? "7d" : window;
}

/** Keep the most recent rolling day represented by the returned data. */
export function trimToArbitrageWindow<T extends { time: Time }>(
  points: T[],
  window: ArbitrageHistoryWindow,
): T[] {
  if (window !== "24h" || points.length === 0) return points;
  const latest = points.reduce((max, point) => Math.max(max, timeSeconds(point.time)), 0);
  const cutoff = latest - 24 * 60 * 60;
  return points.filter((point) => timeSeconds(point.time) >= cutoff);
}

function timeSeconds(time: Time): number {
  if (typeof time === "number") return time;
  if (typeof time === "string") return Date.parse(time) / 1000;
  return Date.UTC(time.year, time.month - 1, time.day) / 1000;
}
