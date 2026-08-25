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

  it("matches marks independently across venues and positions", () => {
    const bybit: AccountPosition = {
      ...position,
      accountId: "b",
      instrumentId: "bybit:live:perp:USDC:ETH-USDC",
      settlementAsset: "USDC",
      quantity: "2",
      entryPrice: "3000",
    };
    const multi: FundingBoardSnapshot = {
      observedAt: new Date().toISOString(),
      venues: [
        ...board().venues,
        {
          venue: "bybit", environment: "live", product: "perp", settlement: "USDC", routeProfileId: "bybit.perp.usdc.v1",
          stale: false, unavailable: false, status: "live",
          rows: [{ instrumentId: bybit.instrumentId, symbol: "ETH-USDC", marketFamily: "venue_perpetual", assetClass: "unknown", markPrice: "3010" }],
        },
      ],
    };
    const [binanceResult, bybitResult] = applyFundingBoardMarks([position, bybit], multi);
    expect(binanceResult?.markPrice).toBe("71000");
    expect(binanceResult?.unrealizedPnl).toBe("10");
    expect(bybitResult?.markPrice).toBe("3010");
    expect(bybitResult?.unrealizedPnl).toBe("20");
  });
});
