import { MismatchDirection } from "lightweight-charts";
import { describe, expect, it } from "vitest";

import { crosshairWhitespaceDirection, positionLineDescriptors } from "@/features/market/price-chart";

describe("price chart crosshair", () => {
  it("snaps right-side whitespace to the latest candle", () => {
    expect(crosshairWhitespaceDirection(false, 306.4, true)).toBe(MismatchDirection.NearestLeft);
  });

  it("snaps left-side whitespace to the first candle", () => {
    expect(crosshairWhitespaceDirection(false, -2.1, true)).toBe(MismatchDirection.NearestRight);
  });

  it("leaves a crosshair that already has candle data alone", () => {
    expect(crosshairWhitespaceDirection(true, 42, true)).toBeNull();
  });

  it("does nothing after the pointer leaves the chart", () => {
    expect(crosshairWhitespaceDirection(false, 42, false)).toBeNull();
  });
});

describe("position overlays", () => {
  it("draws entry lines only for open positions and preserves direction", () => {
    expect(positionLineDescriptors([
      { accountId: "a", instrumentId: "BTC-USDT", side: "buy", quantity: "0.001", entryPrice: "77000", markPrice: "77100", unrealizedPnl: "0.1" },
      { accountId: "a", instrumentId: "ETH-USDT", side: "sell", quantity: "-0.2", entryPrice: "3500", markPrice: "3490", unrealizedPnl: "2" },
      { accountId: "a", instrumentId: "SOL-USDT", side: "buy", quantity: "0", entryPrice: "180", markPrice: "181", unrealizedPnl: "0" },
    ])).toEqual([
      { key: "a:BTC-USDT:buy", price: 77000, side: "buy", quantity: "0.001" },
      { key: "a:ETH-USDT:sell", price: 3500, side: "sell", quantity: "0.2" },
    ]);
  });
});
