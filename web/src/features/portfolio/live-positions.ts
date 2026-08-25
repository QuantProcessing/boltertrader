import type { AccountPosition, FundingBoardSnapshot, PositionSettlementSummary } from "@/api/types";
import { positionMarkPrice, positionUnrealizedPnl } from "@/features/trade/live-position";

interface PublicMark {
  price?: string;
  stale: boolean;
}

/** Reprice each venue position with the mark belonging to its canonical ID. */
export function applyFundingBoardMarks(positions: AccountPosition[], board?: FundingBoardSnapshot): AccountPosition[] {
  const marks = fundingBoardMarks(board);
  return positions.map((position) => {
    const mark = marks.get(position.instrumentId);
    if (!mark?.price) return position;
    const displayedMark = positionMarkPrice(position, mark.price, mark.stale);
    const pnl = positionUnrealizedPnl(position, mark.price, mark.stale);
    return {
      ...position,
      markPrice: displayedMark.value,
      markPriceSource: "venue_public",
      unrealizedPnl: pnl.value,
      unrealizedPnlSource: pnl.stale ? "unavailable" : pnl.estimated ? "estimated" : position.unrealizedPnlSource,
    };
  });
}

export function summarizeLivePositions(positions: AccountPosition[]): PositionSettlementSummary[] {
  const byAsset = new Map<string, { count: number; live: number; stale: number; total: number; excluded: number }>();
  for (const position of positions) {
    const asset = position.unrealizedPnlCurrency || position.settlementAsset || "—";
    const current = byAsset.get(asset) ?? { count: 0, live: 0, stale: 0, total: 0, excluded: 0 };
    current.count += 1;
    if (position.freshness === "live") current.live += 1;
    else current.stale += 1;
    const pnl = Number(position.unrealizedPnl);
    if (position.unrealizedPnl === "" || !Number.isFinite(pnl)) current.excluded += 1;
    else current.total += pnl;
    byAsset.set(asset, current);
  }
  return [...byAsset.entries()].map(([settlementAsset, value]) => ({
    settlementAsset,
    positionCount: value.count,
    liveCount: value.live,
    staleCount: value.stale,
    unrealizedPnl: value.excluded === value.count ? undefined : String(value.total),
    excludedPnlCount: value.excluded,
  }));
}

function fundingBoardMarks(board?: FundingBoardSnapshot): Map<string, PublicMark> {
  const marks = new Map<string, PublicMark>();
  for (const venue of board?.venues ?? []) {
    for (const row of venue.rows) {
      if (!row.instrumentId) continue;
      marks.set(row.instrumentId, {
        price: row.markPrice,
        stale: venue.unavailable || row.missingFields?.includes("mark") === true || row.staleFields?.includes("mark") === true,
      });
    }
  }
  return marks;
}
