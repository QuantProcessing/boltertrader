import type { ArbitrageOpportunity } from "@/api/types";

export type ArbitrageSortKey = "edge" | "carry" | "openInterest";
export type ArbitrageSortDirection = "asc" | "desc";

export interface ArbitrageSort {
  key: ArbitrageSortKey;
  direction: ArbitrageSortDirection;
}

/** Missing metrics remain last in both directions; absence is not zero. */
export function sortArbitrageOpportunities(
  items: ArbitrageOpportunity[],
  sort: ArbitrageSort,
): ArbitrageOpportunity[] {
  return [...items].sort((left, right) => {
    const a = opportunitySortValue(left, sort.key);
    const b = opportunitySortValue(right, sort.key);
    if (a === null && b !== null) return 1;
    if (a !== null && b === null) return -1;
    if (a !== null && b !== null && a !== b) {
      return sort.direction === "desc" ? b - a : a - b;
    }
    if (left.executable !== right.executable) return Number(right.executable) - Number(left.executable);
    return left.symbol.localeCompare(right.symbol);
  });
}

export function opportunitySortValue(
  opportunity: ArbitrageOpportunity,
  key: ArbitrageSortKey,
): number | null {
  if (key === "edge") return numberOf(primaryRate(opportunity));
  if (key === "carry") {
    if (opportunity.strategy === "cross_perp_funding") return numberOf(opportunity.spreadApr);
    if (opportunity.strategy === "spot_perp_basis") return numberOf(opportunity.short.dailyRate);
    const long = numberOf(opportunity.long.dailyRate);
    const short = numberOf(opportunity.short.dailyRate);
    return long === null || short === null ? null : short - long;
  }
  return opportunityOpenInterestNotional(opportunity);
}

/**
 * Capacity is constrained by every perpetual leg in the hedge.
 *
 * A spot leg has no perpetual OI by definition, so spot-perp basis uses the
 * perpetual leg alone. Perp-perp strategies require both values; one published
 * value cannot stand in for an unknown hedge capacity on the other venue.
 */
export function opportunityOpenInterestNotional(opportunity: ArbitrageOpportunity): number | null {
  const perpetualLegs = [opportunity.long, opportunity.short].filter((leg) => leg.product === "perp");
  if (perpetualLegs.length === 0) return null;
  const values = perpetualLegs.map((leg) => numberOf(leg.openInterestNotional));
  if (values.some((value) => value === null)) return null;
  return Math.min(...(values as number[]));
}

function primaryRate(opportunity: ArbitrageOpportunity): string | undefined {
  if (opportunity.strategy === "cross_perp_funding") return opportunity.spreadDaily;
  if (opportunity.strategy === "spot_perp_basis") return opportunity.basisRate;
  return opportunity.priceSpread;
}

function numberOf(value: string | undefined): number | null {
  const parsed = Number.parseFloat(value ?? "");
  return Number.isFinite(parsed) ? parsed : null;
}
