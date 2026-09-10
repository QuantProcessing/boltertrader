import { describe, expect, it } from "vitest";

import type { Candle } from "@/api/types";
import { applyBookEvent, candleForSubscription, referenceStaleDelay, mergeLiveCandle, OrderBookSynchronizer } from "@/features/market/stream";

describe("public market stream reducers", () => {
  it("uses the Gateway-provided mark freshness window instead of a fixed two-second timeout", () => {
    const now = Date.parse("2026-08-20T00:00:10Z");
    expect(referenceStaleDelay("2026-08-20T00:00:00Z", 45_000, undefined, now)).toBe(35_000);
    expect(referenceStaleDelay("2026-08-20T00:00:00Z", undefined, undefined, now)).toBe(0);
  });

  it("uses Gateway age so browser clock skew cannot flash a fresh mark stale", () => {
    const browserClockOneHourAhead = Date.parse("2026-08-20T01:00:00Z");
    expect(referenceStaleDelay("2026-08-20T00:00:00Z", 5_000, 250, browserClockOneHourAhead)).toBe(4_750);
    expect(referenceStaleDelay("2026-08-20T00:00:00Z", 5_000, 5_001, browserClockOneHourAhead)).toBe(0);
  });

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

  it("ignores a late candle from the interval subscription that was released", () => {
    const candle: Candle = {
      openTime: "2026-08-20T00:00:00Z",
      closeTime: "2026-08-20T00:05:00Z",
      open: "1",
      high: "2",
      low: "1",
      close: "2",
      volume: "3",
      complete: false,
    };
    const value = { instrumentId: "btc", interval: "5m" as const, candle };
    expect(candleForSubscription(value, "btc", "5m")).toBe(candle);
    expect(candleForSubscription(value, "btc", "15m")).toBeNull();
    expect(candleForSubscription(value, "eth", "5m")).toBeNull();
  });

  it("joins buffered native deltas to the WebSocket snapshot sequence", () => {
    const sync = new OrderBookSynchronizer();
    sync.push({
      eventType: "delta", observedAt: "2026-08-20T00:00:01Z",
      sourceSequence: "11", previous: "10",
      bids: [{ price: "100", quantity: "2" }], asks: [],
    });
    const result = sync.seed({
      eventType: "snapshot", observedAt: "2026-08-20T00:00:00Z", sourceSequence: "10",
      bids: [{ price: "100", quantity: "1" }], asks: [{ price: "101", quantity: "3" }],
    });
    expect(result.needsSnapshot).toBe(false);
    expect(result.book?.bids).toEqual([{ price: "100", quantity: "2" }]);
    expect(result.book?.asks).toEqual([{ price: "101", quantity: "3" }]);
  });

  it("replaces the visible book on a self-contained full event", () => {
    const sync = new OrderBookSynchronizer();
    sync.seed({
      eventType: "snapshot", observedAt: "2026-08-20T00:00:00Z", sourceSequence: "10",
      bids: [{ price: "100", quantity: "1" }], asks: [{ price: "101", quantity: "3" }],
    });
    const result = sync.push({
      eventType: "full", observedAt: "2026-08-20T00:00:01Z", sourceSequence: "11",
      bids: [{ price: "99", quantity: "2" }], asks: [{ price: "102", quantity: "4" }],
    });
    expect(result.needsSnapshot).toBe(false);
    expect(result.book?.bids).toEqual([{ price: "99", quantity: "2" }]);
    expect(result.book?.asks).toEqual([{ price: "102", quantity: "4" }]);
  });

  it("rejects a delta gap instead of inventing a partial order book", () => {
    const sync = new OrderBookSynchronizer();
    sync.seed({
      eventType: "snapshot", observedAt: "2026-08-20T00:00:00Z", sourceSequence: "10",
      bids: [{ price: "100", quantity: "1" }], asks: [{ price: "101", quantity: "3" }],
    });
    const result = sync.push({
      eventType: "delta", observedAt: "2026-08-20T00:00:02Z",
      sourceSequence: "12", previous: "11", bids: [], asks: [],
    });
    expect(result).toEqual({ book: null, needsSnapshot: true });
  });
});
