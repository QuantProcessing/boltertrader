import { describe, expect, it } from "vitest";

import { compactIdentifier, decimalSign, formatDecimal, formatDuration, formatFullTimestamp, splitDecimal, splitInstrument } from "./format";

/**
 * The separator `formatDecimal` actually used, recovered from its own output
 * rather than restated here — the point of the test below is that the code
 * chose a non-breaking one, which a copy of the character cannot demonstrate.
 */
const GROUP = formatDecimal("41528.51").replace(/[\d.]/g, "");
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
      `123${GROUP}456${GROUP}789${GROUP}012${GROUP}345${GROUP}678${GROUP}901${GROUP}234${GROUP}567${GROUP}890`,
    );
  });

  it("groups above four integer digits and leaves four alone", () => {
    expect(formatDecimal("1234.5")).toBe("1234.5");
    expect(formatDecimal("41528.51")).toBe(`41${GROUP}528.51`);
  });

  it("groups with a separator that cannot break", () => {
    // The regression this guards had no failing test and no visible symptom
    // until a price grew a digit: the separator was U+2009 THIN SPACE, which is
    // a break opportunity, so `114 250.19` split after the group and rendered
    // as two line boxes inside the order book's 22px row.
    //
    // There is no DOM API for "is this a break opportunity", so the assertion is
    // the code point. U+00A0 and U+202F are the two spaces that carry none;
    // every other space in Unicode does, including the one that was here.
    expect([..."\u00a0\u202f"]).toContain(GROUP);
    expect(GROUP).not.toBe("\u2009");
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

describe("compactIdentifier", () => {
  it("preserves short IDs and bounds long command IDs", () => {
    expect(compactIdentifier("command-1")).toBe("command-1");
    expect(compactIdentifier("command_8ae2449c8d91a057f1797ea2602abcdef")).toBe("command_8ae2449c8d91a…abcdef");
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

describe("formatFullTimestamp", () => {
  it("always retains the calendar date", () => {
    const at = new Date(2026, 7, 29, 19, 36, 7);
    expect(formatFullTimestamp(at.toISOString())).toBe("2026-08-29 19:36:07");
  });
});
