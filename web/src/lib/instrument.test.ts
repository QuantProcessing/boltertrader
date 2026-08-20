import { describe, expect, it } from "vitest";

import { instrumentIdentity, instrumentMeta, instrumentSymbol } from "./instrument";

describe("instrument display identity", () => {
  it("turns a perpetual canonical id into a symbol and readable context", () => {
    const value = "binance:live:perp:USDT:BTC-USDT";

    expect(instrumentIdentity(value)).toEqual({
      raw: value,
      symbol: "BTC-USDT",
      venue: "binance",
      environment: "live",
      product: "perp",
      settlement: "USDT",
    });
    expect(instrumentSymbol(value)).toBe("BTC-USDT");
    expect(instrumentMeta(value)).toBe("Binance · Perp");
  });

  it("handles spot ids without inventing a settlement field", () => {
    expect(instrumentIdentity("binance:live:spot:ETH-USDT").settlement).toBeUndefined();
    expect(instrumentMeta("binance:live:spot:ETH-USDT")).toBe("Binance · Spot");
  });

  it("hides venue contract suffixes from the user without changing the raw id", () => {
    const value = "okx:live:perp:USDT:BTC-USDT-SWAP";
    expect(instrumentSymbol(value)).toBe("BTC-USDT");
    expect(instrumentIdentity(value).raw).toBe(value);
  });

  it("leaves a plain symbol readable", () => {
    expect(instrumentSymbol("BTC-USDT")).toBe("BTC-USDT");
    expect(instrumentMeta("BTC-USDT")).toBeNull();
  });

  it("humanizes unknown venue and product names without losing the raw id", () => {
    const value = "new_venue:prod:dated_future:BTC-USD";
    expect(instrumentMeta(value)).toBe("New Venue · Dated Future");
    expect(instrumentIdentity(value).raw).toBe(value);
  });
});
