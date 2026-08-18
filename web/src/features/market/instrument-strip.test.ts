import { describe, expect, it } from "vitest";

import type { Instrument } from "@/api/types";
import { groupByVenue } from "@/features/market/instrument-strip";

/* ---------------------------------------------------------------------------
 * The picker's ranking, which design.md §10 states as a rule.
 *
 * The defect these tests exist for: typing `btc` used to return one flat list,
 * filtered by substring and cut at sixty, so finding the OKX listing meant
 * scrolling past everything Binance lists. "Which venue" is the question the
 * picker is asked, and the answer has to be on screen.
 * ------------------------------------------------------------------------- */

const rules = { priceIncrement: "0.1", quantityIncrement: "0.001", minQuantity: "0.001", minNotional: "5" };

function instrument(venue: string, symbol: string, product = "perp", ready = true): Instrument {
  const [baseAsset = "", quoteAsset = ""] = symbol.split("-");
  return {
    instrumentId: `${venue}:live:${product}:${symbol}`,
    venue,
    environment: "live",
    product,
    symbol,
    baseAsset,
    quoteAsset,
    rules,
    ready,
  };
}

/** Binance first and in bulk, which is what buried the other venues. */
const CATALOG: Instrument[] = [
  instrument("binance", "BTC-USDT", "spot"),
  instrument("binance", "BTC-USDC"),
  instrument("binance", "BTCDOM-USDT"),
  instrument("binance", "ETH-BTC", "spot"),
  instrument("binance", "ETH-USDT"),
  instrument("binance", "BTC-USDT"),
  instrument("binance", "BTC-USDT-250926"),
  instrument("okx", "BTC-USDT"),
  instrument("okx", "ETH-USDT"),
  instrument("bybit", "BTC-USDT"),
  instrument("bybit", "BTC-PERP", "perp", false),
];

describe("groupByVenue", () => {
  it("puts every venue that lists the symbol on screen", () => {
    const groups = groupByVenue(CATALOG, "btc");
    expect(groups.map((group) => group.venue)).toEqual(["binance", "bybit", "okx"]);
  });

  it("ranks an exact symbol above a prefix", () => {
    const [binance] = groupByVenue(CATALOG, "btc-usdt");
    expect(binance?.matches.map((match) => match.instrument.symbol)).toEqual([
      // Both exact, in catalog order — the spot listing came first.
      "BTC-USDT",
      "BTC-USDT",
      // The dated future only starts with the needle.
      "BTC-USDT-250926",
    ]);
  });

  it("ranks a prefix above a substring", () => {
    const [binance] = groupByVenue(CATALOG, "btc");
    expect(binance?.matches.map((match) => match.instrument.symbol)).toEqual([
      "BTC-USDT",
      "BTC-USDC",
      "BTCDOM-USDT",
      "BTC-USDT",
      "BTC-USDT-250926",
      // Last: BTC is in the middle of this one.
      "ETH-BTC",
    ]);
  });

  it("folds separators, so btcusdt finds BTC-USDT", () => {
    const groups = groupByVenue(CATALOG, "btcusdt");
    expect(groups.flatMap((group) => group.matches.map((match) => match.instrument.symbol))).toContain("BTC-USDT");
  });

  it("ranks a symbol substring above an instrument-id-only match", () => {
    // "spot" appears in no symbol, only in the instrument id.
    const groups = groupByVenue(CATALOG, "spot");
    expect(groups).toHaveLength(1);
    expect(groups[0]?.matches.every((match) => match.instrument.product === "spot")).toBe(true);
  });

  it("admits a venue match on its own", () => {
    const groups = groupByVenue(CATALOG, "okx");
    expect(groups.map((group) => group.venue)).toEqual(["okx"]);
    expect(groups[0]?.matches).toHaveLength(2);
  });

  it("shows halted instruments rather than hiding them", () => {
    const bybit = groupByVenue(CATALOG, "bybit").at(0);
    expect(bybit?.matches.map((match) => match.instrument.ready)).toContain(false);
  });

  it("counts what it does not list, so no truncation is silent", () => {
    // Sixty BTC markets on one venue, well past the list budget.
    const many = Array.from({ length: 60 }, (_, index) => instrument("binance", `BTC${index}-USDT`));
    const [binance] = groupByVenue(many, "btc");
    expect(binance?.matches.length).toBeGreaterThan(0);
    expect(binance!.matches.length + binance!.hidden).toBe(60);
    expect(binance?.hidden).toBeGreaterThan(0);
  });

  it("splits the budget, so one crowded venue cannot squeeze out the others", () => {
    const crowded = [
      ...Array.from({ length: 60 }, (_, index) => instrument("binance", `BTC${index}-USDT`)),
      instrument("okx", "BTC-USDT"),
    ];
    const groups = groupByVenue(crowded, "btc");
    expect(groups.map((group) => group.venue)).toEqual(["binance", "okx"]);
    // The crowded venue yields room rather than taking the budget off the top:
    // okx's single listing is on screen and whole.
    const okx = groups.find((group) => group.venue === "okx");
    expect(okx?.matches).toHaveLength(1);
    expect(okx?.hidden).toBe(0);
  });

  it("lists the whole catalog, grouped, when nothing is typed", () => {
    const groups = groupByVenue(CATALOG, "");
    expect(groups.map((group) => group.venue).sort()).toEqual(["binance", "bybit", "okx"]);
  });
});
