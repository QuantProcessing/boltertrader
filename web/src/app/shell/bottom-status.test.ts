import { describe, expect, it } from "vitest";

import type { FundingBoardVenue } from "@/api/types";
import { summarizeVenues, venueConnectionDetails } from "./bottom-status";

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

  it("explains each venue and puts exceptions first", () => {
    const details = venueConnectionDetails([
      profile("binance", "live", { lastSuccessAt: "2026-08-24T12:00:10Z" }),
      profile("bybit", "live", { settlement: "USDT", lastSuccessAt: "2026-08-24T12:00:08Z" }),
      profile("bybit", "degraded", { settlement: "USDC", stale: false, reconnects: 2, lastSuccessAt: "2026-08-24T12:00:09Z" }),
      profile("okx", "unavailable"),
    ]);

    expect(details).toEqual([
      expect.objectContaining({ venue: "okx", state: "unavailable", unavailableProfiles: 1 }),
      expect.objectContaining({ venue: "bybit", state: "degraded", reconnectingProfiles: 1, reconnects: 2, lastSuccessAt: "2026-08-24T12:00:09Z" }),
      expect.objectContaining({ venue: "binance", state: "live", profiles: 1 }),
    ]);
  });
});
