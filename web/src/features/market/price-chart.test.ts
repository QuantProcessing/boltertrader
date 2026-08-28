import { MismatchDirection } from "lightweight-charts";
import { describe, expect, it } from "vitest";

import { chartTradeRules, crosshairWhitespaceDirection, currentCandlePriceLine, markPriceLine, positionLineDescriptors } from "@/features/market/price-chart";

describe("chart trading rules", () => {
  it("presents explicit catalog limits with the asset they constrain", () => {
    expect(chartTradeRules({
      instrumentId: "binance:perp:BTC-USDT",
      venue: "binance",
      environment: "live",
      product: "perp",
      settlement: "USDT",
      symbol: "BTC-USDT",
      baseAsset: "BTC",
      quoteAsset: "USDT",
      marketFamily: "venue_perpetual",
      assetClass: "crypto",
      ready: true,
      rules: {
        minQuantity: "0.001",
        minNotional: "100",
        quantityIncrement: "0.001",
        priceIncrement: "0.1",
      },
    })).toEqual([
      { key: "minQuantity", value: "0.001 BTC" },
      { key: "minNotional", value: "100 USDT" },
      { key: "quantityIncrement", value: "0.001 BTC" },
      { key: "priceIncrement", value: "0.1 USDT" },
    ]);
  });

  it("does not invent zero values for missing rules", () => {
    expect(chartTradeRules({
      instrumentId: "binance:perp:BTC-USDT",
      venue: "binance",
      environment: "live",
      product: "perp",
      symbol: "BTC-USDT",
      baseAsset: "BTC",
      quoteAsset: "USDT",
      marketFamily: "venue_perpetual",
      assetClass: "crypto",
      ready: true,
      rules: { minQuantity: "0.001" },
    })).toEqual([{ key: "minQuantity", value: "0.001 BTC" }]);
    expect(chartTradeRules(null)).toEqual([]);
  });
});

describe("current candle price line", () => {
  const candle = (open: string, close: string) => ({
    openTime: "2026-08-24T12:00:00Z",
    closeTime: "2026-08-24T12:15:00Z",
    open,
    high: "110",
    low: "90",
    close,
    volume: "1",
    complete: false,
  });

  it("uses the upper body edge and up color direction for a rising candle", () => {
    expect(currentCandlePriceLine(candle("100", "108"))).toEqual({ price: 108, direction: "up" });
  });

  it("uses the lower body edge and down color direction for a falling candle", () => {
    expect(currentCandlePriceLine(candle("108", "97"))).toEqual({ price: 97, direction: "down" });
  });

  it("rejects invalid candle values", () => {
    expect(currentCandlePriceLine(candle("bad", "97"))).toBeNull();
  });
});

describe("mark-price overlay", () => {
  it("keeps the public mark separate from the last traded candle", () => {
    expect(markPriceLine({ price: "78338.9", stale: false })).toEqual({ price: 78338.9, stale: false });
  });

  it("retains a stale mark as an explicitly stale reference line", () => {
    expect(markPriceLine({ price: "78338.9", stale: true })).toEqual({ price: 78338.9, stale: true });
  });

  it("does not draw unavailable or zero marks", () => {
    expect(markPriceLine(null)).toBeNull();
    expect(markPriceLine({ price: "0", stale: false })).toBeNull();
    expect(markPriceLine({ price: "bad", stale: false })).toBeNull();
  });
});

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
  it("draws entry and venue liquidation lines only for open positions", () => {
    expect(positionLineDescriptors([
      { accountId: "a", routeId: "perp", instrumentId: "BTC-USDT", positionSlot: "long", accountLabel: "main", side: "buy", quantity: "0.001", entryPrice: "77000", markPrice: "77100", unrealizedPnl: "0.1", liquidationPrice: "63000", freshness: "live" },
      { accountId: "a", routeId: "perp", instrumentId: "ETH-USDT", positionSlot: "short", side: "sell", quantity: "-0.2", entryPrice: "3500", markPrice: "3490", unrealizedPnl: "2", freshness: "stale" },
      { accountId: "a", instrumentId: "SOL-USDT", side: "buy", quantity: "0", entryPrice: "180", markPrice: "181", unrealizedPnl: "0" },
    ])).toEqual([
      { key: "a:perp:BTC-USDT:long:entry", price: 77000, side: "buy", quantity: "0.001", kind: "entry", accountLabel: "main", stale: false },
      { key: "a:perp:BTC-USDT:long:liquidation", price: 63000, side: "buy", quantity: "0.001", kind: "liquidation", accountLabel: "main", stale: false },
      { key: "a:perp:ETH-USDT:short:entry", price: 3500, side: "sell", quantity: "0.2", kind: "entry", accountLabel: "a", stale: true },
    ]);
  });

  it("does not render unavailable last-known positions", () => {
    expect(positionLineDescriptors([
      { accountId: "a", instrumentId: "BTC-USDT", quantity: "1", entryPrice: "77000", markPrice: "", unrealizedPnl: "", freshness: "unavailable" },
    ])).toEqual([]);
  });
});
