import type { UTCTimestamp } from "lightweight-charts";
import { describe, expect, it } from "vitest";

import { fundingWindowFor, trimToArbitrageWindow } from "./history-window";

describe("arbitrage history windows", () => {
  it("uses the smallest complete server window for the rolling day", () => {
    expect(fundingWindowFor("24h")).toBe("7d");
    expect(fundingWindowFor("30d")).toBe("30d");
  });

  it("clips 24h relative to the newest returned point", () => {
    const day = 24 * 60 * 60;
    const points = [0, day, day * 2].map((time) => ({ time: time as UTCTimestamp, value: time }));
    expect(trimToArbitrageWindow(points, "24h")).toEqual(points.slice(1));
    expect(trimToArbitrageWindow(points, "7d")).toBe(points);
  });
});
