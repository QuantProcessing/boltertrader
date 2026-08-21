import { describe, expect, it } from "vitest";
import type { UTCTimestamp } from "lightweight-charts";

import type { CandlePage } from "@/api/types";
import { differenceSeries, priceSpreadSeries } from "./strategy-history";

function page(closes: Array<[string, string]>): CandlePage {
  return {
    venue: "venue",
    environment: "live",
    product: "perp",
    instrumentId: "instrument",
    interval: "1h",
    observedAt: "2026-08-20T00:00:00Z",
    candles: closes.map(([openTime, close]) => ({
      openTime,
      closeTime: openTime,
      open: close,
      high: close,
      low: close,
      close,
      volume: "1",
      complete: true,
    })),
  };
}

describe("arbitrage history projections", () => {
  it("computes price spread only where both venues have a synchronized close", () => {
    const long = page([
      ["2026-08-20T00:00:00Z", "100"],
      ["2026-08-20T01:00:00Z", "110"],
    ]);
    const short = page([
      ["2026-08-20T00:00:00Z", "101"],
      ["2026-08-20T02:00:00Z", "112"],
    ]);

    expect(priceSpreadSeries(long, short)).toEqual([
      { time: 1787184000, value: 1 },
    ]);
  });

  it("forward-fills both rates at each settlement before subtracting long from short", () => {
    const hour = 60 * 60;
    expect(differenceSeries(
      [{ time: 100 as UTCTimestamp, value: 0.01 }, { time: (100 + hour * 2) as UTCTimestamp, value: 0.02 }],
      [{ time: (100 + hour) as UTCTimestamp, value: 0.05 }],
    )).toEqual([
      { time: 100 + hour, value: 0.04 },
      { time: 100 + hour * 2, value: 0.030000000000000002 },
    ]);
  });
});
