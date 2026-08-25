import { describe, expect, it } from "vitest";

import type { FundingBoardRow, FundingBoardSnapshot, FundingBoardVenue } from "@/api/types";
import { actionableFundingPercent, pivotRows } from "@/features/funding/matrix";

/* ---------------------------------------------------------------------------
 * What makes two venues' rows the same instrument.
 *
 * The defect these tests exist for: the pivot keyed on `row.symbol`, and okx
 * spells its USDT perpetuals `BTC-USDT-SWAP` where everyone else writes
 * `BTC-USDT`. So okx never joined a row anyone else was on — it appeared in
 * zero of 654 cross-venue rows while binance, bybit and lighter were all
 * present. A whole venue was silently missing from the view the page exists
 * for, and nothing on screen said so: an absent venue and a mis-keyed one look
 * identical.
 *
 * That is also why this is a test rather than something to check in a browser.
 * okx is a live third-party endpoint — unreachable from some networks, and
 * never the same numbers twice when it is reachable.
 * ------------------------------------------------------------------------- */

function row(symbol: string, base: string, quote: string, extra: Partial<FundingBoardRow> = {}): FundingBoardRow {
  return {
    symbol,
    baseAsset: base,
    quoteAsset: quote,
    marketFamily: "venue_perpetual",
    assetClass: "unknown",
    rate: "0.0001",
    ...extra,
  };
}

function venue(name: string, settlement: string, rows: FundingBoardRow[]): FundingBoardVenue {
  return {
    venue: name,
    environment: "live",
    product: "perp",
    settlement,
    routeProfileId: `${name}.perp.${settlement.toLowerCase()}.v1`,
    stale: false,
    unavailable: false,
    status: "live",
    rows,
  };
}

function board(...venues: FundingBoardVenue[]): FundingBoardSnapshot {
  return { observedAt: "2026-08-18T00:00:00Z", venues };
}

describe("pivotRows", () => {
  it("treats an unavailable venue with null rows as an empty column", () => {
    const unavailable = {
      ...venue("okx", "USDT", []),
      unavailable: true,
      status: "unavailable" as const,
      rows: null,
    } as unknown as FundingBoardVenue;

    expect(() => pivotRows(board(unavailable))).not.toThrow();
    expect(pivotRows(board(unavailable))).toEqual([]);
  });

  it("puts okx's -SWAP spelling on the same row as everyone else's", () => {
    const rows = pivotRows(
      board(
        venue("binance", "USDT", [row("BTC-USDT", "BTC", "USDT")]),
        venue("bybit", "USDT", [row("BTCUSDT", "BTC", "USDT")]),
        venue("okx", "USDT", [row("BTC-USDT-SWAP", "BTC", "USDT")]),
      ),
    );

    expect(rows).toHaveLength(1);
    expect(rows[0]?.venueCount).toBe(3);
    expect(Object.keys(rows[0]?.cells ?? {})).toContain("okx.perp.usdt.v1");
  });

  it("keeps a USDC book apart from the USDT one", () => {
    // lighter settles in USDC, so its BTC book is a different instrument — not
    // a spelling difference. Merging them would offer a spread between two
    // things that do not hedge each other.
    const rows = pivotRows(
      board(
        venue("binance", "USDT", [row("BTC-USDT", "BTC", "USDT")]),
        venue("lighter", "USDC", [row("BTC-USDC", "BTC", "USDC")]),
      ),
    );

    expect(rows.map((entry) => entry.key).sort()).toEqual(["BTC-USDC", "BTC-USDT"]);
    expect(rows.every((entry) => entry.venueCount === 1)).toBe(true);
  });

  it("labels the row with a venue's own spelling, which the hedge link needs", () => {
    // `symbol` is what the operator reads and what the hedge link hands to the
    // ticket, so it stays a string some venue actually quoted — never the
    // synthesized key.
    const rows = pivotRows(board(venue("okx", "USDT", [row("ETH-USDT-SWAP", "ETH", "USDT")])));

    expect(rows[0]?.key).toBe("ETH-USDT");
    expect(rows[0]?.symbol).toBe("ETH-USDT-SWAP");
  });

  it("falls back to the symbol when a venue publishes no assets", () => {
    const bare: FundingBoardRow = {
      symbol: "MYSTERY-PERP",
      marketFamily: "venue_perpetual",
      assetClass: "unknown",
      rate: "0.0001",
    };
    const rows = pivotRows(board(venue("binance", "USDT", [bare])));

    expect(rows[0]?.key).toBe("MYSTERY-PERP");
  });

  it("takes the largest open interest any venue published, and names it", () => {
    const rows = pivotRows(
      board(
        venue("binance", "USDT", [
          row("BTC-USDT", "BTC", "USDT", {
            openInterest: "999999",
            openInterestNotional: "1000",
          }),
        ]),
        venue("okx", "USDT", [
          row("BTC-USDT-SWAP", "BTC", "USDT", {
            openInterest: "1",
            openInterestNotional: "9000",
            openInterestNotionalAsset: "USD",
          }),
        ]),
        venue("bybit", "USDT", [row("BTCUSDT", "BTC", "USDT")]),
      ),
    );

    expect(rows[0]?.openInterest).toBe("9000");
    expect(rows[0]?.openInterestVenue).toBe("okx");
    expect(rows[0]?.openInterestAsset).toBe("USD");
  });
});

describe("actionableFundingPercent", () => {
  it("keeps a stale rate displayable but excludes it from opportunity math", () => {
    const stale = row("BTC-USDT", "BTC", "USDT", {
      rate: "0.0001",
      intervalHours: 8,
      staleFields: ["funding"],
    });

    expect(stale.rate).toBe("0.0001");
    expect(actionableFundingPercent(stale, "24h")).toBeNull();
  });

  it("normalizes a fresh rate", () => {
    expect(actionableFundingPercent(row("BTC-USDT", "BTC", "USDT", { intervalHours: 8 }), "24h"))
      .toBeCloseTo(0.03);
  });
});
