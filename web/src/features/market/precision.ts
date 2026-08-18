import type { Instrument } from "@/api/types";
import { splitDecimal } from "@/lib/format";

/* ---------------------------------------------------------------------------
 * How many decimal places a price and a quantity are allowed to have.
 *
 * The answer belongs to the venue, not to us. `exchange.InstrumentRules` carries
 * `price_increment` and `quantity_increment`, `Validate()` refuses an instrument
 * whose increments are not positive, and the catalog forwards both to the
 * browser as `Instrument.rules` — so by the time a market reaches a screen the
 * tick is known. Nothing read it until now, which is why the book was rendering
 * binance's `64142.51000000` in full and colliding with the size beside it.
 *
 * The digits are counted off the string rather than derived from the number.
 * `Number("0.00001")` is fine but `String(1e-8)` is `"1e-8"`, and a tick of
 * 1e-8 is exactly the case where getting it wrong is invisible in the fixtures
 * and wrong on the venue that has one. `splitDecimal` is the same parser the
 * rest of the product formats through, so anything it rejects — exponents,
 * blanks, junk — lands here as "unknown" rather than as a confident zero.
 * ------------------------------------------------------------------------- */

/** The decimal places an increment implies. `"0.01"` → 2, `"1"` → 0. */
export function decimalsOf(increment: unknown): number | undefined {
  if (typeof increment !== "string") return undefined;
  const parts = splitDecimal(increment);
  if (!parts) return undefined;
  // Trailing zeros are not precision: a tick of `0.010` is a tick of `0.01`.
  const fraction = parts.fraction.replace(/0+$/, "");
  // A zero increment is not a grid. The server rejects one, so this is only
  // reachable through `TradeRules`' index signature, which types as `unknown`.
  if (parts.integer === "0" && fraction === "") return undefined;
  return fraction.length;
}

export interface Precision {
  /** Places to show a price to, or `undefined` when the venue did not say. */
  price: number | undefined;
  /** Places to show a quantity to. */
  quantity: number | undefined;
}

export function precisionOf(instrument: Instrument | null | undefined): Precision {
  return {
    price: decimalsOf(instrument?.rules.priceIncrement),
    quantity: decimalsOf(instrument?.rules.quantityIncrement),
  };
}

/** The width of a grid, as the number a human reads: `2` → `"0.01"`, `-1` → `"10"`. */
export function stepLabel(decimals: number): string {
  if (decimals > 0) return `0.${"0".repeat(decimals - 1)}1`;
  return `1${"0".repeat(-decimals)}`;
}

/**
 * `price`, snapped onto a grid `decimals` places wide.
 *
 * Aggregating an order book means putting several levels under one price, and
 * the price that appears has to be one an operator can actually rest an order
 * at — clicking a row writes it into the ticket. So bids snap down and asks snap
 * up: every level in a group is at least as good as its label, and no group ever
 * advertises a price inside the spread.
 *
 * It is string arithmetic on purpose. The whole point of the book is that the
 * number handed to the ticket is a number the venue quoted, and a float round
 * trip is how `114250.10` becomes `114250.09999999999`. `decimals` may be
 * negative, which snaps whole units — grouping BTC into tens.
 *
 * Returns `null` when the grid is coarser than the price has digits, which the
 * caller reads as "do not aggregate" rather than as a value.
 */
export function snapPrice(price: string, decimals: number, up: boolean): string | null {
  const parts = splitDecimal(price);
  if (!parts || parts.sign === "-") return null;

  const digits = parts.integer + parts.fraction;
  const point = parts.integer.length;
  const keep = Math.min(point + decimals, digits.length);
  if (keep <= 0) return null;

  let kept = digits.slice(0, keep);
  if (up && /[1-9]/.test(digits.slice(keep))) kept = increment(kept);

  // A carry out of the top ("999" → "1000") grows the number by a digit, which
  // moves the decimal point right along with it.
  const shifted = point + (kept.length - keep);
  // Rebuilt to the width of the *grid*, not of the input: a venue that quotes
  // `64142.51000000` on a 0.01 tick should come back as `64142.51`, since the
  // zeros it padded with are not places the group is accurate to.
  const width = shifted + Math.max(0, keep - point);
  const padded = kept + "0".repeat(Math.max(0, width - kept.length));
  const integer = padded.slice(0, shifted) || "0";
  const fraction = padded.slice(shifted);
  return fraction === "" ? integer : `${integer}.${fraction}`;
}

/** Adds one to the last digit of a digit string, carrying leftwards. */
function increment(digits: string): string {
  const out = digits.split("");
  for (let i = out.length - 1; i >= 0; i -= 1) {
    if (out[i] !== "9") {
      out[i] = String(Number(out[i]) + 1);
      return out.join("");
    }
    out[i] = "0";
  }
  return `1${out.join("")}`;
}
