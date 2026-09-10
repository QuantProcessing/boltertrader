import { describe, expect, it } from "vitest";

import type { AccountPosition } from "@/api/types";

import { closePositionOrderRequest, positionQuantityPercent } from "./close-position";

function position(change: Partial<AccountPosition> = {}): AccountPosition {
  return {
    accountId: "acct-binance",
    instrumentId: "binance:live:perp:USDT:BTC-USDT",
    quantity: "0.050",
    entryPrice: "78000",
    markPrice: "78100",
    unrealizedPnl: "5",
    ...change,
  };
}

describe("closePositionOrderRequest", () => {
  it("turns a long account position into a full reduce-only market sell", () => {
    expect(closePositionOrderRequest(position({ side: "buy" }))).toEqual({
      accountId: "acct-binance",
      instrumentId: "binance:live:perp:USDT:BTC-USDT",
      side: "sell",
      orderType: "market",
      quantity: "0.05",
      limitPrice: "",
      timeInForce: "IOC",
      reduceOnly: true,
      leverage: 0,
    });
  });

  it("uses signed quantity when a venue omits the side", () => {
    expect(closePositionOrderRequest(position({ side: undefined, quantity: "-0.0300" }))?.side).toBe("buy");
    expect(closePositionOrderRequest(position({ side: undefined, quantity: "-0.0300" }))?.quantity).toBe("0.03");
  });

  it("does not manufacture a close for zero or malformed quantity", () => {
    expect(closePositionOrderRequest(position({ quantity: "0.000" }))).toBeNull();
    expect(closePositionOrderRequest(position({ quantity: "not-a-decimal" }))).toBeNull();
  });

  it("builds a partial reduce-only limit close without a preview id", () => {
    expect(closePositionOrderRequest(position({ side: "sell", quantity: "0.05" }), {
      quantity: "0.02",
      orderType: "limit",
      limitPrice: "78125.5",
    })).toEqual({
      accountId: "acct-binance",
      instrumentId: "binance:live:perp:USDT:BTC-USDT",
      side: "buy",
      orderType: "limit",
      quantity: "0.02",
      limitPrice: "78125.5",
      timeInForce: "GTC",
      reduceOnly: true,
      leverage: 0,
    });
  });

  it("rejects an oversized close locally", () => {
    expect(closePositionOrderRequest(position(), { quantity: "0.051" })).toBeNull();
  });
});

describe("positionQuantityPercent", () => {
  it("derives a bounded slider value without floating point", () => {
    expect(positionQuantityPercent("0.025", "0.05")).toBe(50);
    expect(positionQuantityPercent("0.050", "0.05")).toBe(100);
    expect(positionQuantityPercent("0.075", "0.05")).toBe(100);
  });
});
