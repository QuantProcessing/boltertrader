import type { ArbitrageOpportunity, ArbitrageOpportunityLeg, ArbitrageStrategy } from "@/api/types";
import type { Key } from "@/lib/i18n";

/** The four tasks an operator can choose, independent of the three engine ids. */
export type ArbitrageView =
  | "crypto-basis"
  | "crypto-funding"
  | "tradfi-funding"
  | "tradfi-spread";

export type ArbitragePath =
  | "/arbitrage/crypto/basis"
  | "/arbitrage/crypto/funding"
  | "/arbitrage/tradfi/funding"
  | "/arbitrage/tradfi/perp-spread";

export interface ArbitrageViewConfig {
  id: ArbitrageView;
  path: ArbitragePath;
  family: "crypto_continuous" | "tradfi_perpetual";
  strategy: ArbitrageStrategy;
  familyLabel: Key;
  label: Key;
}

export const ARBITRAGE_VIEWS: readonly ArbitrageViewConfig[] = [
  {
    id: "crypto-basis",
    path: "/arbitrage/crypto/basis",
    family: "crypto_continuous",
    strategy: "spot_perp_basis",
    familyLabel: "arb.family.cryptoContinuous",
    label: "arb.view.cryptoBasis",
  },
  {
    id: "crypto-funding",
    path: "/arbitrage/crypto/funding",
    family: "crypto_continuous",
    strategy: "cross_perp_funding",
    familyLabel: "arb.family.cryptoContinuous",
    label: "arb.view.cryptoFunding",
  },
  {
    id: "tradfi-funding",
    path: "/arbitrage/tradfi/funding",
    family: "tradfi_perpetual",
    strategy: "cross_perp_funding",
    familyLabel: "arb.family.tradfiPerpetual",
    label: "arb.view.tradfiFunding",
  },
  {
    id: "tradfi-spread",
    path: "/arbitrage/tradfi/perp-spread",
    family: "tradfi_perpetual",
    strategy: "cross_perp_spread",
    familyLabel: "arb.family.tradfiPerpetual",
    label: "arb.view.tradfiSpread",
  },
] as const;

export function arbitrageView(id: ArbitrageView): ArbitrageViewConfig {
  return ARBITRAGE_VIEWS.find((view) => view.id === id) as ArbitrageViewConfig;
}

export function matchesArbitrageView(view: ArbitrageView, opportunity: ArbitrageOpportunity): boolean {
  const config = arbitrageView(view);
  return opportunity.strategyFamily === config.family && opportunity.strategy === config.strategy;
}

export function viewForOpportunity(opportunity: ArbitrageOpportunity): ArbitrageView | null {
  return ARBITRAGE_VIEWS.find(
    (view) => opportunity.strategyFamily === view.family && opportunity.strategy === view.strategy,
  )?.id ?? null;
}

/**
 * The edge for the legs exactly as the operator has oriented them.
 *
 * Opportunity fields carry the server's ranked direction. They are correct for
 * the list, but become stale the moment an operator swaps two perpetual legs.
 * The snapshot and ticket must therefore derive their sign from the visible
 * legs instead of repeating the ranked value.
 */
export function orientedEdgeRate(
  strategy: ArbitrageStrategy,
  long: ArbitrageOpportunityLeg,
  short: ArbitrageOpportunityLeg,
): number | null {
  if (strategy === "cross_perp_funding") {
    return difference(short.dailyRate, long.dailyRate);
  }

  const longPrice = finite(long.referencePrice);
  const shortPrice = finite(short.referencePrice);
  if (longPrice === null || shortPrice === null || longPrice <= 0 || shortPrice <= 0) return null;
  const denominator = strategy === "spot_perp_basis" ? longPrice : (longPrice + shortPrice) / 2;
  return (shortPrice - longPrice) / denominator;
}

function difference(minuend: string | undefined, subtrahend: string | undefined): number | null {
  const left = finite(minuend);
  const right = finite(subtrahend);
  return left === null || right === null ? null : left - right;
}

function finite(value: string | undefined): number | null {
  const parsed = Number.parseFloat(value ?? "");
  return Number.isFinite(parsed) ? parsed : null;
}
