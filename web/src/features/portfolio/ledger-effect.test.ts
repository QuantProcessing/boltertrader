import { describe, expect, it } from "vitest";
import { ledgerEffect } from "./ledger-effect";

describe("native currency ledger earnings effects", () => {
  it.each([
    ["0.000000000000000001", "-0.000000000000000001"],
    ["-0.000000000000000001", "0.000000000000000001"],
    ["123456789012345678901.5", "-123456789012345678901.5"],
    ["+2.5", "-2.5"], ["-0.000", "0"],
  ])("converts signed fee %s without numeric rounding", (amount, expected) => {
    expect(ledgerEffect({ kind: "fee", amount })).toBe(expected);
  });
  it("does not reinterpret ambiguous historical fee signs", () => {
    expect(ledgerEffect({ kind: "fee", amount: "-0.1", accountingStatus: "legacy_fee_review" })).toBeUndefined();
  });
  it("preserves signed income and losses", () => {
    expect(ledgerEffect({ kind: "funding", amount: "-1.25" })).toBe("-1.25");
    expect(ledgerEffect({ kind: "realized_pnl", amount: "2" })).toBe("2");
  });
});
