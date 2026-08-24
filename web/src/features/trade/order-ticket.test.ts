import { describe, expect, it } from "vitest";

import type { Command } from "@/api/types";
import {
  marginPercentQuantity,
  orderConfirmationRequired,
  positionPercentQuantity,
  quoteAmountToBaseQuantity,
  sumDecimals,
} from "@/features/trade/order-ticket";
import { describeCommandFailure } from "@/features/trade/order-status";
import type { Translate } from "@/lib/i18n";

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
  it("opens the confirmation dialog for a successful preview by default", () => {
    expect(orderConfirmationRequired(true, [])).toBe(true);
  });

  it("submits a successful preview directly after the operator opts out", () => {
    expect(orderConfirmationRequired(false, [])).toBe(false);
  });

  it("always shows server blockers even after the operator opts out", () => {
    expect(orderConfirmationRequired(false, ["minimum_notional"])).toBe(true);
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
    if (key === "ticket.failure.insufficientMargin") return `insufficient margin (${values?.code})`;
    if (key === "ticket.failure.detail") return `${values?.reason} | ${values?.commandId}`;
    if (key === "ticket.failure.unknown") return "unknown rejection";
    return key;
  };

  it("turns a failed Binance command into an actionable persistent reason", () => {
    const failed = command({
      executionEvidence: { state: "rejected", venueCode: "-2019", venueMessage: "venue rejected order command" },
    });
    expect(describeCommandFailure(failed, "binance", t)).toBe("insufficient margin (-2019) | command-1");
  });

  it("keeps the command id when the venue supplies no detail", () => {
    const failed = command({ commandId: "command-2" });
    expect(describeCommandFailure(failed, "binance", t)).toBe("unknown rejection | command-2");
  });
});
