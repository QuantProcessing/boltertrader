/**
 * Decimal presentation.
 *
 * Every quantity, price and balance crossing the API is a string, because the
 * services carry them as arbitrary-precision decimals. Nothing in this file
 * parses one into a `number`. A float round-trip of "0.1" is invisible in a
 * label and catastrophic in an order ticket, and once a value has been through
 * `parseFloat` there is no way to tell from the result whether it was harmed.
 *
 * So: grouping, padding and sign handling are string operations, and the value
 * that reaches the DOM is a transformation of the value the server sent.
 */

const GROUP_SEPARATOR = " "; // thin space — groups without the comma/period ambiguity
const MINUS = "−"; // true minus, so a negative aligns with a positive digit

export interface DecimalParts {
  sign: "+" | "-" | "";
  integer: string;
  fraction: string;
}

/** Splits a decimal string without interpreting it. Returns null if it is not one. */
export function splitDecimal(value: string): DecimalParts | null {
  const match = /^([+-]?)(\d*)(?:\.(\d*))?$/.exec(value.trim());
  if (!match) {
    return null;
  }
  const integer = match[2] ?? "";
  const fraction = match[3] ?? "";
  if (integer === "" && fraction === "") {
    // Bare "", "+", "-" and "." all land here.
    return null;
  }
  return {
    sign: match[1] === "+" ? "+" : match[1] === "-" ? "-" : "",
    integer: integer === "" ? "0" : integer,
    fraction,
  };
}

function group(integer: string): string {
  if (integer.length <= 4) {
    // Four digits stay solid. "1 234" reads as two values at a glance; "1234"
    // does not, and prices in this range are the common case.
    return integer;
  }
  let out = "";
  for (let i = integer.length; i > 0; i -= 3) {
    const start = Math.max(0, i - 3);
    out = integer.slice(start, i) + (out === "" ? "" : GROUP_SEPARATOR + out);
  }
  return out;
}

export interface FormatOptions {
  /** Pad or truncate the fraction to exactly this many places. */
  decimals?: number;
  /** Show a leading + on positive values. Off by default; on for deltas. */
  signed?: boolean;
  /** Insert thin-space group separators in the integer part. On by default. */
  grouped?: boolean;
}

/**
 * Formats a decimal string for display. Unparseable input is returned as-is
 * rather than replaced with a placeholder — if the server sends something
 * unexpected the operator should see it, not a dash that hides it.
 */
export function formatDecimal(value: string | null | undefined, options: FormatOptions = {}): string {
  if (value === null || value === undefined || value === "") {
    return "—";
  }
  const parts = splitDecimal(value);
  if (!parts) {
    return value;
  }

  const { decimals, signed = false, grouped = true } = options;

  let fraction = parts.fraction;
  if (decimals !== undefined) {
    fraction = fraction.slice(0, decimals).padEnd(decimals, "0");
  }

  const integer = grouped ? group(parts.integer) : parts.integer;
  const magnitude = fraction === "" ? integer : `${integer}.${fraction}`;

  if (parts.sign === "-") {
    return isZero(parts) ? magnitude : MINUS + magnitude;
  }
  if (signed && !isZero(parts)) {
    return "+" + magnitude;
  }
  return magnitude;
}

function isZero(parts: DecimalParts): boolean {
  return /^0*$/.test(parts.integer) && /^0*$/.test(parts.fraction);
}

/** -1, 0 or 1. Used to pick a colour, never to do arithmetic. */
export function decimalSign(value: string | null | undefined): -1 | 0 | 1 {
  if (!value) return 0;
  const parts = splitDecimal(value);
  if (!parts || isZero(parts)) return 0;
  return parts.sign === "-" ? -1 : 1;
}

/** Compact instrument rendering: BTC-USDT stays BTC-USDT, but the pair splits. */
export function splitInstrument(instrumentId: string): { base: string; quote: string | null } {
  const index = instrumentId.lastIndexOf("-");
  if (index <= 0) {
    return { base: instrumentId, quote: null };
  }
  return { base: instrumentId.slice(0, index), quote: instrumentId.slice(index + 1) };
}

const TIME = new Intl.DateTimeFormat(undefined, {
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

const DATE_TIME = new Intl.DateTimeFormat(undefined, {
  month: "short",
  day: "2-digit",
  hour: "2-digit",
  minute: "2-digit",
  second: "2-digit",
  hour12: false,
});

/** Time of day for today's events, date-qualified for anything older. */
export function formatTimestamp(iso: string | null | undefined, now: Date = new Date()): string {
  if (!iso) return "—";
  const at = new Date(iso);
  if (Number.isNaN(at.getTime())) return iso;
  const sameDay =
    at.getFullYear() === now.getFullYear() &&
    at.getMonth() === now.getMonth() &&
    at.getDate() === now.getDate();
  return sameDay ? TIME.format(at) : DATE_TIME.format(at);
}

/** "4s", "1m 20s" — for countdowns the operator is racing. */
export function formatDuration(milliseconds: number): string {
  const total = Math.max(0, Math.round(milliseconds / 1000));
  const minutes = Math.floor(total / 60);
  const seconds = total % 60;
  return minutes > 0 ? `${minutes}m ${String(seconds).padStart(2, "0")}s` : `${seconds}s`;
}
