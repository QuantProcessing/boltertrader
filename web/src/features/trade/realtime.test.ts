import { describe, expect, it } from "vitest";

import { realtimeEventMovesPositions } from "@/features/trade/realtime";

describe("trade realtime invalidation", () => {
  it("resnapshots positions on fills but not on provisional command states", () => {
    expect(realtimeEventMovesPositions("fill.recorded")).toBe(true);
    expect(realtimeEventMovesPositions("account.fill")).toBe(true);
    expect(realtimeEventMovesPositions("command.filled")).toBe(true);
    expect(realtimeEventMovesPositions("command.submitted")).toBe(false);
    expect(realtimeEventMovesPositions("command.failed")).toBe(false);
  });
});
