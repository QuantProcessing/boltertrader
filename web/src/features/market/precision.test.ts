import { describe, expect, it } from "vitest";

import { decimalsOf, precisionOf, snapPrice, stepLabel } from "./precision";

/* ---------------------------------------------------------------------------
 * The book's precision is the venue's, and it is arrived at without a float.
 *
 * Both halves matter. Getting the places wrong is what put `64142.51000000`
 * next to `0.00762500` in a 256px column; doing the arithmetic in a float is
 * what would put `114250.09999999999` in the ticket after a click. So the
 * cases below are mostly the ones where `Number` would be plausible and wrong.
 * ------------------------------------------------------------------------- */

describe("decimalsOf", () => {
  it("counts the places an increment implies", () => {
    expect(decimalsOf("0.01")).toBe(2);
    expect(decimalsOf("0.1")).toBe(1);
    expect(decimalsOf("0.00001")).toBe(5);
    expect(decimalsOf("1")).toBe(0);
    expect(decimalsOf("10")).toBe(0);
  });

  it("does not mistake padding for precision", () => {
    // A venue that publishes its tick to a fixed width is not claiming eight
    // places of accuracy; it is claiming two.
    expect(decimalsOf("0.01000000")).toBe(2);
    expect(decimalsOf("1.0")).toBe(0);
  });

  it("survives everything an index signature can hand it", () => {
    // `TradeRules` is `[rule: string]: unknown`, so all of these typecheck at
    // the call site and only fail here.
    for (const junk of [undefined, null, 0.01, "", "abc", "1e-8", {}, []]) {
      expect(decimalsOf(junk)).toBeUndefined();
    }
    // Zero is not a grid. The server rejects one, but nothing on this side of
    // the wire re-checks that.
    expect(decimalsOf("0")).toBeUndefined();
    expect(decimalsOf("0.000")).toBeUndefined();
  });

  it("reads both increments off an instrument", () => {
    const rules = { priceIncrement: "0.1", quantityIncrement: "0.001" };
    expect(precisionOf({ rules } as never)).toEqual({ price: 1, quantity: 3 });
    expect(precisionOf(null)).toEqual({ price: undefined, quantity: undefined });
  });
});

describe("stepLabel", () => {
  it("states a grid as the price it is", () => {
    expect(stepLabel(2)).toBe("0.01");
    expect(stepLabel(1)).toBe("0.1");
    expect(stepLabel(0)).toBe("1");
    expect(stepLabel(-1)).toBe("10");
    expect(stepLabel(-2)).toBe("100");
  });
});

describe("snapPrice", () => {
  it("snaps bids down and asks up", () => {
    // Every level in a group is at least as good as the label, on both sides —
    // which is what stops a group from advertising a price inside the spread.
    expect(snapPrice("114250.19", 1, false)).toBe("114250.1");
    expect(snapPrice("114250.19", 1, true)).toBe("114250.2");
    expect(snapPrice("114250.10", 1, false)).toBe("114250.1");
    // Already on the grid: an ask does not round up off a price it can rest at.
    expect(snapPrice("114250.10", 1, true)).toBe("114250.1");
  });

  it("drops the padding the venue quoted with", () => {
    expect(snapPrice("64142.51000000", 2, false)).toBe("64142.51");
    expect(snapPrice("0.00762500", 5, false)).toBe("0.00762");
  });

  it("groups whole units at a negative grid", () => {
    expect(snapPrice("64142.51", -1, false)).toBe("64140");
    expect(snapPrice("64142.51", -1, true)).toBe("64150");
    expect(snapPrice("64142.51", -2, false)).toBe("64100");
  });

  it("carries out of the top without losing the point", () => {
    // Stated at the grid's width, like every other row: on a 0.1 grid the
    // neighbour above `999.9` is `1000.0`, not `1000`. The zero is the grid
    // speaking, and dropping it would make one row in the ladder narrower than
    // the rest.
    expect(snapPrice("999.99", 1, true)).toBe("1000.0");
    expect(snapPrice("99.999", 2, true)).toBe("100.00");
    expect(snapPrice("9.99", 0, true)).toBe("10");
  });

  it("is exact where a float would not be", () => {
    // 0.1 + 0.2 territory. The result is a prefix of the input, so there is no
    // arithmetic to be imprecise about.
    expect(snapPrice("0.30000000000000004", 1, false)).toBe("0.3");
    // Eighteen significant digits, past what a double can hold.
    expect(snapPrice("123456789012345678.9", 0, false)).toBe("123456789012345678");
  });

  it("refuses a grid the price has no digits for", () => {
    // Coarser than the number itself: there is nothing left to group by, and
    // the caller reads null as "show the book ungrouped".
    expect(snapPrice("9.5", -2, false)).toBeNull();
    expect(snapPrice("64142.51", -6, false)).toBeNull();
    // Not a decimal, or a negative one. Neither is a price.
    expect(snapPrice("", 2, false)).toBeNull();
    expect(snapPrice("1e-8", 2, false)).toBeNull();
    expect(snapPrice("-1.5", 1, false)).toBeNull();
  });
});
