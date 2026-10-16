import { describe, expect, it } from "vitest";
import { countdown, decimals, snapToStep } from "./format";

describe("snapToStep", () => {
  it("floors to the step without float error", () => {
    expect(snapToStep("0.0123456", "0.001")).toBe("0.012");
    expect(snapToStep("1.3", "0.1")).toBe("1.3");
    expect(snapToStep("0.30000000000000004", "0.1")).toBe("0.3");
    expect(snapToStep("17", "5")).toBe("15");
    expect(snapToStep("0.0009", "0.001")).toBe("0");
    expect(snapToStep("abc", "0.1")).toBe("");
  });
});

describe("decimals", () => {
  it("counts significant fraction digits", () => {
    expect(decimals("0.010")).toBe(2);
    expect(decimals("1")).toBe(0);
  });
});

describe("countdown", () => {
  it("formats remaining time", () => {
    expect(countdown(3_723_000, 0)).toBe("01:02:03");
  });
});
