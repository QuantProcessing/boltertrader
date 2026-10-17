import { describe, expect, it } from "vitest";

import { rowsFor } from "./order-book";

describe("rowsFor", () => {
  it("fills the body exactly when it fits whole rows", () => {
    // 24px spread + 2 × 10 rows of 24px.
    expect(rowsFor(504)).toEqual({ asks: 10, bids: 10 });
  });

  it("gives leftover space to the bids so no gap shows at the bottom", () => {
    const { asks, bids } = rowsFor(520);
    expect(asks).toBe(10);
    expect(bids).toBe(11);
    expect(24 + (asks + bids) * 24).toBeGreaterThanOrEqual(520);
  });

  it("caps at the depth the gateway keeps and shows at least one row", () => {
    expect(rowsFor(10_000)).toEqual({ asks: 50, bids: 50 });
    expect(rowsFor(0)).toEqual({ asks: 1, bids: 1 });
  });
});
