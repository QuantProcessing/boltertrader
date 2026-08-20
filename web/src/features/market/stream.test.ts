import { describe, expect, it } from "vitest";

import type { Candle } from "@/api/types";
import { applyBookEvent, mergeLiveCandle } from "@/features/market/stream";

describe("public market stream reducers", () => {
  it("applies a one-sided order-book delta without erasing the other side", () => {
    const current = {
      bids: [{ price: "100", quantity: "2" }],
      asks: [{ price: "101", quantity: "3" }],
      observedAt: "2026-08-20T00:00:00Z",
    };
    const next = applyBookEvent(current, {
      kind: "order_book",
      eventType: "delta",
      instrumentId: "binance:live:perp:USDT:BTC-USDT",
      observedAt: "2026-08-20T00:00:01Z",
      bids: [{ price: "100", quantity: "0" }, { price: "99.5", quantity: "4" }],
    });
    expect(next?.bids).toEqual([{ price: "99.5", quantity: "4" }]);
    expect(next?.asks).toEqual(current.asks);
  });

  it("replaces the forming candle and keeps chronological order", () => {
    const candle = (openTime: string, close: string): Candle => ({
      openTime,
      closeTime: openTime,
      open: "1",
      high: "2",
      low: "1",
      close,
      volume: "3",
      complete: false,
    });
    const merged = mergeLiveCandle(
      [candle("2026-08-20T00:00:00Z", "1"), candle("2026-08-20T00:01:00Z", "2")],
      candle("2026-08-20T00:01:00Z", "2.5"),
      2,
    );
    expect(merged?.map((entry) => entry.close)).toEqual(["1", "2.5"]);
  });
});
