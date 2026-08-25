import { describe, expect, it } from "vitest";

import type { VenueConnectivity } from "@/api/endpoints";

import { summarizeVenues } from "./bottom-status";

function venue(name: string, state: VenueConnectivity["state"]): VenueConnectivity {
  return { venue: name, state, latencyMs: 12, ewmaLatencyMs: 14, consecutiveFailures: 0 };
}

describe("summarizeVenues", () => {
  it("groups the independent venue probes without consulting market feeds", () => {
    expect(summarizeVenues([
      venue("binance", "online"), venue("bybit", "delayed"), venue("okx", "offline"), venue("gate", "connecting"),
    ])).toEqual({
      online: ["binance"], delayed: ["bybit"], connecting: ["gate"], offline: ["okx"], total: 4,
    });
  });
});
