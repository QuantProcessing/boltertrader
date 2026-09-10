import type { AccountPosition, PositionSettlementSummary } from "@/api/types";
import {
  positionUnrealizedPnl,
  addDecimalValues,
} from "@/features/trade/live-position";

interface ReferenceQuote {
  price?: string;
  stale: boolean;
}

/** Index estimates keep their value when delayed, with an explicit stale marker. */
export function applyIndexPrices(
  positions: AccountPosition[],
  prices: Record<string, ReferenceQuote>,
): AccountPosition[] {
  return positions.map((position) => {
    const index = prices[position.instrumentId];
    if (!index?.price) return position;
    const pnl = positionUnrealizedPnl(position, index.price, index.stale);
    return {
      ...position,
      indexPrice: index.price,
      indexPriceStale: index.stale,
      unrealizedPnl: pnl.value,
      unrealizedPnlSource: pnl.estimated
        ? "estimated"
        : position.unrealizedPnlSource,
    };
  });
}

export function summarizeLivePositions(
  positions: AccountPosition[],
): PositionSettlementSummary[] {
  const byAsset = new Map<
    string,
    {
      count: number;
      live: number;
      stale: number;
      total: string[];
      excluded: number;
    }
  >();
  for (const position of positions) {
    const asset =
      position.unrealizedPnlCurrency || position.settlementAsset || "—";
    const current = byAsset.get(asset) ?? {
      count: 0,
      live: 0,
      stale: 0,
      total: [],
      excluded: 0,
    };
    current.count += 1;
    if (position.freshness === "live") current.live += 1;
    else current.stale += 1;
    const pnl = Number(position.unrealizedPnl);
    if (position.unrealizedPnl === "" || !Number.isFinite(pnl))
      current.excluded += 1;
    else current.total.push(position.unrealizedPnl);
    byAsset.set(asset, current);
  }
  return [...byAsset.entries()].map(([settlementAsset, value]) => ({
    settlementAsset,
    positionCount: value.count,
    liveCount: value.live,
    staleCount: value.stale,
    unrealizedPnl:
      value.excluded === value.count
        ? undefined
        : addDecimalValues(value.total),
    excludedPnlCount: value.excluded,
  }));
}
