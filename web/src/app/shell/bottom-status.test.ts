import { describe, expect, it } from "vitest";

import type { FundingBoardVenue } from "@/api/types";
import { summarizeVenues } from "./bottom-status";

function profile(venue: string, status: FundingBoardVenue["status"], overrides: Partial<FundingBoardVenue> = {}): FundingBoardVenue {
  return {
    venue,
    environment: "live",
    product: "perp",
    routeProfileId: `${venue}.${status}`,
    stale: status === "degraded",
    unavailable: status === "unavailable",
    status,
    rows: [],
    ...overrides,
  };
}

describe("summarizeVenues", () => {
  it("collapses route profiles into a conservative venue state", () => {
    const summary = summarizeVenues([
      profile("binance", "live"),
      profile("binance", "degraded"),
      profile("bybit", "live"),
      profile("okx", "unavailable"),
      profile("okx", "unavailable"),
    ]);

    expect(summary).toEqual({
      live: ["bybit"],
      degraded: ["binance"],
      unavailable: ["okx"],
      total: 3,
    });
  });
});
