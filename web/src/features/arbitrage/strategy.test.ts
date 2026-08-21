import { describe, expect, it } from "vitest";

import type { ArbitrageOpportunity, ArbitrageOpportunityLeg } from "@/api/types";
import { matchesArbitrageView, orientedEdgeRate, viewForOpportunity } from "./strategy";

function opportunity(
  strategyFamily: ArbitrageOpportunity["strategyFamily"],
  strategy: ArbitrageOpportunity["strategy"],
): ArbitrageOpportunity {
  return { strategyFamily, strategy } as ArbitrageOpportunity;
}

describe("arbitrage strategy views", () => {
  it("splits the shared funding engine into crypto and TradFi user tasks", () => {
    const crypto = opportunity("crypto_continuous", "cross_perp_funding");
    const tradfi = opportunity("tradfi_perpetual", "cross_perp_funding");

    expect(matchesArbitrageView("crypto-funding", crypto)).toBe(true);
    expect(matchesArbitrageView("crypto-funding", tradfi)).toBe(false);
    expect(matchesArbitrageView("tradfi-funding", tradfi)).toBe(true);
  });

  it("never routes unclassified assets into an executable strategy workspace", () => {
    expect(viewForOpportunity(opportunity("unclassified", "cross_perp_funding"))).toBeNull();
  });

  it("maps basis and TradFi price spread to distinct views", () => {
    expect(viewForOpportunity(opportunity("crypto_continuous", "spot_perp_basis"))).toBe("crypto-basis");
    expect(viewForOpportunity(opportunity("tradfi_perpetual", "cross_perp_spread"))).toBe("tradfi-spread");
  });

  it("reverses funding and price edges when the visible legs are swapped", () => {
    const low = { dailyRate: "0.0001", referencePrice: "100" } as ArbitrageOpportunityLeg;
    const high = { dailyRate: "0.0003", referencePrice: "101" } as ArbitrageOpportunityLeg;

    expect(orientedEdgeRate("cross_perp_funding", low, high)).toBeCloseTo(0.0002);
    expect(orientedEdgeRate("cross_perp_funding", high, low)).toBeCloseTo(-0.0002);
    expect(orientedEdgeRate("cross_perp_spread", low, high)).toBeCloseTo(1 / 100.5);
    expect(orientedEdgeRate("cross_perp_spread", high, low)).toBeCloseTo(-1 / 100.5);
  });
});
