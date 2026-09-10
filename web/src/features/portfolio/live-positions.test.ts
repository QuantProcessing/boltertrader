import { describe, expect, it } from "vitest";
import type { AccountPosition } from "@/api/types";
import { applyIndexPrices, summarizeLivePositions } from "./live-positions";
const position: AccountPosition = { accountId: "a", instrumentId: "binance:live:perp:USDT:BTC-USDT", product: "perp", settlementAsset: "USDT", side: "buy", quantity: "0.01", entryPrice: "70000", markPrice: "70010", unrealizedPnl: "0.1" };
describe("index position estimates", () => {
 it("uses the matching index, retaining the distinct venue mark", () => {
  const [result] = applyIndexPrices([position], { [position.instrumentId]: {price:"71000",stale:false} });
  expect(result?.markPrice).toBe("70010"); expect(result?.indexPrice).toBe("71000"); expect(result?.unrealizedPnl).toBe("10");
 });
 it("retains the last estimate and labels a delayed index", () => {
  const [result] = applyIndexPrices([position], { [position.instrumentId]: {price:"71000",stale:true} });
  expect(result?.unrealizedPnl).toBe("10"); expect(result?.indexPriceStale).toBe(true);
 });
 it("never substitutes another venue or an inverse contract formula", () => {
  const other = {...position, instrumentId:"bybit:live:perp:USDT:BTC-USDT"};
  const inverse = {...position, settlementAsset:"BTC"};
  const results = applyIndexPrices([other, inverse], { [position.instrumentId]: {price:"71000",stale:false} });
  expect(results.map(p => p.unrealizedPnl)).toEqual(["0.1","0.1"]);
 });
 it("sums exact decimal values separately by settlement asset", () => {
  const result = summarizeLivePositions([{...position, unrealizedPnl:"0.1"},{...position, unrealizedPnl:"0.2"},{...position, settlementAsset:"USDC",unrealizedPnl:"1"}]);
  expect(result.map(s => [s.settlementAsset,s.unrealizedPnl])).toEqual([["USDT","0.3"],["USDC","1"]]);
 });
});
