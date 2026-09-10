import { describe, expect, it } from "vitest";

import type { Command } from "@/api/types";
import {
  defaultLimitPrice,
  notionalBelowMinimum,
  marginPercentQuantity,
  orderConfirmationRequired,
  positionPercentQuantity,
  quoteAmountToBaseQuantity,
  sumDecimals,
} from "@/features/trade/order-ticket";
import { describeCommandFailure } from "@/features/trade/order-status";
import type { Translate } from "@/lib/i18n";

describe("order ticket position sizing", () => {
  it("initializes a limit price from the mark on the venue price grid", () => {
    expect(defaultLimitPrice("78396.7", "0.1")).toBe("78396.7");
    expect(defaultLimitPrice("78396.7", "0.5")).toBe("78396.5");
    expect(defaultLimitPrice("78396.75", "0.5")).toBe("78397");
    expect(defaultLimitPrice(undefined, "0.1")).toBeNull();
  });

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

  it("converts quote currency to base quantity and floors to the venue step", () => {
    expect(quoteAmountToBaseQuantity("100", "60000", "0.001")).toBe("0.001");
    expect(quoteAmountToBaseQuantity("77.1316", "77131.6", "0.001")).toBe("0.001");
    expect(quoteAmountToBaseQuantity("50", "60000", "0.001")).toBeNull();
  });

  it("sizes from available margin, leverage, and slider percentage", () => {
    expect(marginPercentQuantity("346.28290631", "77131.6", 20, 100, "0.001")).toBe("0.089");
    expect(marginPercentQuantity("346.28290631", "77131.6", 20, 50, "0.001")).toBe("0.044");
  });
});

describe("order confirmation policy", () => {
  it("opens the confirmation dialog by default", () => {
    expect(orderConfirmationRequired(true)).toBe(true);
  });

  it("submits directly after the operator opts out", () => {
    expect(orderConfirmationRequired(false)).toBe(false);
  });
});

describe("order ticket failure detail", () => {
  const command = (overrides: Partial<Command>): Command => ({
    commandId: "command-1",
    accountId: "account-1",
    lineageId: "lineage-1",
    productId: "manual",
    reservationId: null,
    idempotencyKey: "key-1",
    commandKind: "manual.place",
    state: "failed",
    payload: {},
    blockers: [],
    createdAt: "2026-08-23T00:00:00Z",
    updatedAt: "2026-08-23T00:00:01Z",
    ...overrides,
  });

  const t: Translate = (key, values) => {
    if (key === "ticket.failure.venue") return `${values?.message} (${values?.code})`;
    if (key === "ticket.failure.detail") return `${values?.reason} | ${values?.commandId}`;
    if (key === "ticket.failure.unknown") return "unknown rejection";
    return key;
  };

  it("preserves the safe venue message without venue-specific product branches", () => {
    const failed = command({
      executionEvidence: { state: "rejected", venueCode: "-2019", venueMessage: "insufficient margin" },
    });
    expect(describeCommandFailure(failed, t)).toBe("insufficient margin (-2019) | command-1");
  });

  it("keeps the command id when the venue supplies no detail", () => {
    const failed = command({ commandId: "command-2" });
    expect(describeCommandFailure(failed, t)).toBe("unknown rejection | command-2");
  });
});

describe("minimum order notional", () => {
  it("compares exactly at the boundary and beyond Number precision", () => {
    expect(notionalBelowMinimum("0.001", "4999.9999999999999999", "5")).toBe(true);
    expect(notionalBelowMinimum("0.001", "5000", "5")).toBe(false);
    expect(notionalBelowMinimum("9007199254740992", "1", "9007199254740993")).toBe(true);
    expect(notionalBelowMinimum("0.00000001", "0.01", "0.000000001")).toBe(true);
  });
  it("does not invent a market estimate or minimum when unavailable", () => {
    expect(notionalBelowMinimum("1", "", "5")).toBe(false);
    expect(notionalBelowMinimum("1", "10", undefined)).toBe(false);
    expect(notionalBelowMinimum("1", "10", "0")).toBe(false);
  });
});
