import { describe, expect, it, vi } from "vitest";

import { ApiError } from "@/api/client";
import type { Order } from "@/api/types";
import { submitOrder, type SubmitDeps } from "./submit";

const order = { inst: "binance:perp:BTC-USDT", side: "buy" as const, type: "market" as const, qty: "0.01" };
const filled: Order = { inst: order.inst, orderId: "1", side: "buy", type: "market", qty: "0.01", filled: "0.01", status: "filled" };

function deps(overrides: Partial<SubmitDeps>): SubmitDeps {
  return { place: vi.fn(), lookup: vi.fn(), sleep: async () => {}, ...overrides };
}

describe("submitOrder", () => {
  it("returns accepted orders directly", async () => {
    const d = deps({ place: vi.fn().mockResolvedValue({ status: "accepted", order: filled }) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "accepted", order: filled });
    expect(d.lookup).not.toHaveBeenCalled();
  });

  it("surfaces venue rejection text", async () => {
    const d = deps({ place: vi.fn().mockResolvedValue({ status: "rejected", venueMessage: "Margin is insufficient." }) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "rejected", message: "Margin is insufficient." });
  });

  it("looks up an unknown outcome with the same client id and never re-places", async () => {
    const place = vi.fn().mockResolvedValue({ status: "unknown" });
    const lookup = vi.fn().mockResolvedValueOnce({ status: "unconfirmed" }).mockResolvedValueOnce({ status: "found", order: filled });
    const d = deps({ place, lookup });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "accepted", order: filled });
    expect(place).toHaveBeenCalledTimes(1);
    const clientOrderId = place.mock.calls[0]![1].clientOrderId;
    expect(lookup).toHaveBeenCalledWith("acc", order.inst, clientOrderId);
  });

  it("treats a network failure as unknown, not as rejected", async () => {
    const d = deps({ place: vi.fn().mockRejectedValue(new TypeError("Failed to fetch")), lookup: vi.fn().mockResolvedValue({ status: "unconfirmed" }) });
    const outcome = await submitOrder("acc", order, d);
    expect(outcome.kind).toBe("unknown");
    expect(d.lookup).toHaveBeenCalledTimes(3);
  });

  it("treats a 4xx as definite", async () => {
    const d = deps({ place: vi.fn().mockRejectedValue(new ApiError(400, "INVALID_REQUEST", "qty must be positive")) });
    expect(await submitOrder("acc", order, d)).toEqual({ kind: "rejected", message: "qty must be positive" });
    expect(d.lookup).not.toHaveBeenCalled();
  });
});
