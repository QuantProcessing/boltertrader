import { describe, expect, it } from "vitest";

import { orderAverageFillPrice, orderFilledQuantity } from "./blotter";

describe("order evidence projection", () => {
  it("sums fills and calculates their quantity-weighted average price exactly", () => {
    const order = {
      executionEvidence: {
        fills: [
          { quantity: "1", price: "100" },
          { quantity: "3", price: "102" },
        ],
      },
    };

    expect(orderFilledQuantity(order)).toBe("4");
    expect(orderAverageFillPrice(order)).toBe("101.5");
  });

  it("does not invent values when no valid fill evidence exists", () => {
    const order = {
      executionEvidence: {
        fills: [
          { quantity: "", price: "100" },
          { quantity: "0.1", price: "invalid" },
        ],
      },
    };

    expect(orderFilledQuantity(order)).toBe("0.1");
    expect(orderAverageFillPrice(order)).toBe("");
    expect(orderFilledQuantity({})).toBe("");
    expect(orderAverageFillPrice({})).toBe("");
  });
});
