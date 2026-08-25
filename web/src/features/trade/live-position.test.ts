import { describe, expect, it } from "vitest";

import type { AccountPosition } from "@/api/types";

import { liquidationPrice, positionMarkPrice, positionUnrealizedPnl } from "./live-position";

const position: AccountPosition = {
  accountId: "acct-a",
  instrumentId: "binance:live:perp:USDT:BTC-USDT",
  product: "perp",
  settlementAsset: "USDT",
  side: "buy",
  quantity: "0.001",
  entryPrice: "76889.6",
  markPrice: "78000",
  unrealizedPnl: "1.1104",
};

describe("live position values", () => {
  it("reprices a linear long with arbitrary-precision decimal arithmetic", () => {
    expect(positionUnrealizedPnl(position, "78338.9")).toEqual({ value: "1.4493", estimated: true });
  });

  it("uses the position side for an unsigned short quantity", () => {
    expect(positionUnrealizedPnl({ ...position, side: "sell", quantity: "0.2", entryPrice: "100" }, "90"))
      .toEqual({ value: "2", estimated: true });
  });

  it("falls back to the venue value for inverse positions or invalid marks", () => {
    expect(positionUnrealizedPnl({ ...position, settlementAsset: "BTC" }, "78338.9"))
      .toEqual({ value: "1.1104", estimated: false });
    expect(positionUnrealizedPnl(position, "0")).toEqual({ value: "1.1104", estimated: false });
  });

  it("uses only positive streaming marks and liquidation prices", () => {
    expect(positionMarkPrice(position, "0")).toEqual({ value: "78000", estimated: false });
    expect(positionMarkPrice(position, "78338.9")).toEqual({ value: "78338.9", estimated: true });
    expect(liquidationPrice({ ...position, liquidationPrice: "0" })).toBeUndefined();
    expect(liquidationPrice({ ...position, liquidationPrice: "65000" })).toBe("65000");
  });

  it("retains a stale mark for display but stops calculating pseudo-live PnL", () => {
    expect(positionMarkPrice(position, "78338.9", true)).toEqual({ value: "78338.9", estimated: true, stale: true });
    expect(positionUnrealizedPnl(position, "78338.9", true)).toEqual({ value: "", estimated: true, stale: true });
  });
});
