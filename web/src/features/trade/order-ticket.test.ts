import { describe, expect, it } from "vitest";

import { positionPercentQuantity, sumDecimals } from "@/features/trade/order-ticket";

describe("order ticket position sizing", () => {
  it("sums split position rows exactly", () => {
    expect(sumDecimals(["0.1", "0.02", "-0.005"])).toBe("0.115");
  });

  it("floors a sell percentage to the venue quantity increment", () => {
    expect(positionPercentQuantity("0.053", 50, "0.001")).toBe("0.026");
    expect(positionPercentQuantity("0.053", 100, "0.001")).toBe("0.053");
  });

  it("refuses a percentage when there is no sellable long position", () => {
    expect(positionPercentQuantity("-1", 50, "0.001")).toBeNull();
    expect(positionPercentQuantity("0.0004", 25, "0.001")).toBeNull();
  });
});
