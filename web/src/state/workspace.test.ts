import { describe, expect, it } from "vitest";

import type { Instrument } from "@/api/types";
import { fallbackInstrument } from "./workspace";

const instrument = (id: string): Instrument => {
  const [venue = "", product = "perp", symbol = ""] = id.split(":");
  const [base = "", quote = ""] = symbol.split("-");
  return { id, venue, product: product as Instrument["product"], symbol, base, quote, tickSize: "0.1", stepSize: "0.001", minQty: "0.001" };
};

const catalog = ["okx:perp:ETH-USDT-SWAP", "okx:perp:BTC-USDT-SWAP", "hyperliquid:perp:BTC-USDC", "gate:spot:BTC-USDT", "lighter:perp:SOL-USDC"].map(instrument);

describe("fallbackInstrument", () => {
  it("keeps an instrument that is in the catalog", () => {
    expect(fallbackInstrument("okx:perp:BTC-USDT-SWAP", catalog)).toBeUndefined();
  });

  it("waits for the catalog before switching", () => {
    expect(fallbackInstrument("binance:perp:BTC-USDT", [])).toBeUndefined();
  });

  it("switches to the same base and product on a reachable venue", () => {
    expect(fallbackInstrument("binance:perp:BTC-USDT", catalog)?.id).toBe("okx:perp:BTC-USDT-SWAP");
    expect(fallbackInstrument("binance:spot:BTC-USDT", catalog)?.id).toBe("gate:spot:BTC-USDT");
  });

  it("falls back to a BTC perp, then to anything", () => {
    expect(fallbackInstrument("binance:perp:DOGE-USDT", catalog)?.id).toBe("okx:perp:BTC-USDT-SWAP");
    expect(fallbackInstrument("binance:perp:DOGE-USDT", [instrument("lighter:perp:SOL-USDC")])?.id).toBe("lighter:perp:SOL-USDC");
  });
});
