import { describe, expect, it } from "vitest";

import { decimalSign, formatDecimal, formatDuration, splitDecimal, splitInstrument } from "./format";

const THIN = " ";
const MINUS = "−";

describe("splitDecimal", () => {
  it("splits sign, integer and fraction without arithmetic", () => {
    expect(splitDecimal("-41528.5100")).toEqual({ sign: "-", integer: "41528", fraction: "5100" });
    expect(splitDecimal("+7")).toEqual({ sign: "+", integer: "7", fraction: "" });
    expect(splitDecimal(".5")).toEqual({ sign: "", integer: "0", fraction: "5" });
  });

  it("rejects anything that is not a plain decimal", () => {
    expect(splitDecimal("1e9")).toBeNull();
    expect(splitDecimal("abc")).toBeNull();
    expect(splitDecimal("")).toBeNull();
    expect(splitDecimal(".")).toBeNull();
  });
});

describe("formatDecimal", () => {
  it("preserves every digit the server sent", () => {
    // The canonical float hazard: this value has no exact binary representation
    // and a parseFloat round-trip changes it.
    expect(formatDecimal("0.1000000000000000055511151231257827")).toBe(
      "0.1000000000000000055511151231257827",
    );
    expect(formatDecimal("123456789012345678901234567890")).toBe(
      `123${THIN}456${THIN}789${THIN}012${THIN}345${THIN}678${THIN}901${THIN}234${THIN}567${THIN}890`,
    );
  });

  it("groups above four integer digits and leaves four alone", () => {
    expect(formatDecimal("1234.5")).toBe("1234.5");
    expect(formatDecimal("41528.51")).toBe(`41${THIN}528.51`);
  });

  it("pads and truncates to a fixed scale without rounding up", () => {
    expect(formatDecimal("1.5", { decimals: 4 })).toBe("1.5000");
    expect(formatDecimal("1.98765", { decimals: 2 })).toBe("1.98");
  });

  it("uses a true minus and only signs non-zero values", () => {
    expect(formatDecimal("-18.42")).toBe(`${MINUS}18.42`);
    expect(formatDecimal("18.42", { signed: true })).toBe("+18.42");
    expect(formatDecimal("0.00", { signed: true })).toBe("0.00");
    expect(formatDecimal("-0.00", { signed: true })).toBe("0.00");
  });

  it("renders absence as an em dash and passes malformed values through", () => {
    expect(formatDecimal(null)).toBe("—");
    expect(formatDecimal("")).toBe("—");
    expect(formatDecimal("not-a-number")).toBe("not-a-number");
  });
});

describe("decimalSign", () => {
  it("classifies without converting", () => {
    expect(decimalSign("-0.00000000000001")).toBe(-1);
    expect(decimalSign("0.000")).toBe(0);
    expect(decimalSign("0.0000000000000000001")).toBe(1);
    expect(decimalSign(null)).toBe(0);
  });
});

describe("splitInstrument", () => {
  it("splits on the last separator", () => {
    expect(splitInstrument("BTC-USDT")).toEqual({ base: "BTC", quote: "USDT" });
    expect(splitInstrument("BTC-USDT-SWAP")).toEqual({ base: "BTC-USDT", quote: "SWAP" });
    expect(splitInstrument("BTCUSDT")).toEqual({ base: "BTCUSDT", quote: null });
  });
});

describe("formatDuration", () => {
  it("counts down in seconds, then minutes", () => {
    expect(formatDuration(4_400)).toBe("4s");
    expect(formatDuration(80_000)).toBe("1m 20s");
    expect(formatDuration(-500)).toBe("0s");
  });
});
