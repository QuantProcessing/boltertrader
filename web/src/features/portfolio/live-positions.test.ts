import { describe, expect, it } from "vitest";

import type { AccountPosition, FundingBoardSnapshot } from "@/api/types";
import { applyFundingBoardMarks } from "./live-positions";

const position: AccountPosition = {
  accountId: "a", instrumentId: "binance:perp:BTC-USDT", product: "perp", settlementAsset: "USDT",
  side: "buy", quantity: "0.01", entryPrice: "70000", markPrice: "70010", unrealizedPnl: "0.1",
};

function board(staleFields: Array<"mark"> = []): FundingBoardSnapshot {
  return { observedAt: new Date().toISOString(), venues: [{
    venue: "binance", environment: "live", product: "perp", settlement: "USDT", routeProfileId: "binance.perp.usdt.v1",
    stale: staleFields.length > 0, unavailable: false, status: staleFields.length > 0 ? "degraded" : "live",
    rows: [{ instrumentId: position.instrumentId, symbol: "BTC-USDT", marketFamily: "venue_perpetual", assetClass: "unknown", markPrice: "71000", staleFields }],
  }] };
}

describe("portfolio public mark projection", () => {
  it("uses the matching instrument mark for linear real-time PnL", () => {
    const [result] = applyFundingBoardMarks([position], board());
    expect(result?.markPrice).toBe("71000");
    expect(result?.unrealizedPnl).toBe("10");
    expect(result?.unrealizedPnlSource).toBe("estimated");
  });

  it("keeps a stale last mark but suppresses PnL", () => {
    const [result] = applyFundingBoardMarks([position], board(["mark"]));
    expect(result?.markPrice).toBe("71000");
    expect(result?.unrealizedPnl).toBe("");
    expect(result?.unrealizedPnlSource).toBe("unavailable");
  });
});
