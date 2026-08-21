import { describe, expect, it } from "vitest";

import type { ArbitrageOpportunity } from "@/api/types";
import { sortArbitrageOpportunities } from "@/features/arbitrage/opportunity-sort";

function opportunity(overrides: Partial<ArbitrageOpportunity> & Pick<ArbitrageOpportunity, "id" | "symbol">): ArbitrageOpportunity {
  return {
    strategy: "cross_perp_funding",
    strategyFamily: "crypto_continuous",
    baseAsset: overrides.symbol.split("-")[0] ?? "BTC",
    quoteAsset: "USDT",
    spreadDaily: "0",
    spreadApr: "0",
    executable: false,
    blockers: [],
    long: { venue: "a", product: "perp", routeProfileId: "a", instrumentId: "a", openInterestNotional: undefined },
    short: { venue: "b", product: "perp", routeProfileId: "b", instrumentId: "b", openInterestNotional: undefined },
    ...overrides,
  } as ArbitrageOpportunity;
}

describe("sortArbitrageOpportunities", () => {
  const rows = [
    opportunity({ id: "small", symbol: "AAA-USDT", spreadDaily: "0.01", spreadApr: "3.65", long: { venue: "a", product: "perp", routeProfileId: "a", instrumentId: "a", openInterestNotional: "100" } as ArbitrageOpportunity["long"], short: { venue: "b", product: "perp", routeProfileId: "b", instrumentId: "b", openInterestNotional: "120" } as ArbitrageOpportunity["short"] }),
    opportunity({ id: "large", symbol: "BBB-USDT", spreadDaily: "0.03", spreadApr: "10.95", long: { venue: "a", product: "perp", routeProfileId: "a", instrumentId: "a", openInterestNotional: "300" } as ArbitrageOpportunity["long"], short: { venue: "b", product: "perp", routeProfileId: "b", instrumentId: "b", openInterestNotional: "350" } as ArbitrageOpportunity["short"] }),
    opportunity({ id: "missing", symbol: "CCC-USDT", spreadDaily: "0.02", spreadApr: undefined }),
  ];

  it("sorts each quantitative column in either direction", () => {
    expect(sortArbitrageOpportunities(rows, { key: "edge", direction: "desc" }).map((row) => row.id)).toEqual(["large", "missing", "small"]);
    expect(sortArbitrageOpportunities(rows, { key: "carry", direction: "asc" }).map((row) => row.id)).toEqual(["small", "large", "missing"]);
    expect(sortArbitrageOpportunities(rows, { key: "openInterest", direction: "desc" }).map((row) => row.id)).toEqual(["large", "small", "missing"]);
  });

  it("always keeps missing metrics last", () => {
    expect(sortArbitrageOpportunities(rows, { key: "openInterest", direction: "asc" }).at(-1)?.id).toBe("missing");
  });

  it("requires OI from both perpetual legs", () => {
    const oneSided = opportunity({
      id: "one-sided",
      symbol: "DDD-USDT",
      long: { venue: "a", product: "perp", routeProfileId: "a", instrumentId: "a", openInterestNotional: "900" } as ArbitrageOpportunity["long"],
    });
    expect(sortArbitrageOpportunities([...rows, oneSided], { key: "openInterest", direction: "desc" }).at(-1)?.id).toBe("one-sided");
  });

  it("uses only the perpetual leg for spot-perpetual basis", () => {
    const basis = opportunity({
      id: "basis",
      symbol: "EEE-USDT",
      strategy: "spot_perp_basis",
      long: { venue: "a", product: "spot", routeProfileId: "a", instrumentId: "a" } as ArbitrageOpportunity["long"],
      short: { venue: "b", product: "perp", routeProfileId: "b", instrumentId: "b", openInterestNotional: "450" } as ArbitrageOpportunity["short"],
    });
    expect(sortArbitrageOpportunities([basis, ...rows], { key: "openInterest", direction: "desc" })[0]?.id).toBe("basis");
  });
});
