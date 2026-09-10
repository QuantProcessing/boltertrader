import type { AccountPosition } from "@/api/types";
import { instrumentSymbol } from "@/lib/instrument";

interface FixedDecimal {
  units: bigint;
  scale: number;
}

export interface PositionValue {
  value: string;
  estimated: boolean;
  stale?: boolean;
}

/**
 * Reprices a normalized linear position from the public index stream.
 *
 * Venue adapters normalize contract quantities into canonical base quantity
 * before this reaches the browser. Coin-settled/inverse rows fail closed and
 * retain the venue P&L because their reciprocal formula is different.
 */
export function positionUnrealizedPnl(position: AccountPosition, liveIndexPrice?: string, liveIndexStale = false): PositionValue {
  const index = parseFixed(liveIndexPrice);
  const entry = parseFixed(position.entryPrice);
  const quantity = parseFixed(position.quantity);
  if (!index || index.units <= 0n || !entry || entry.units <= 0n || !quantity || quantity.units === 0n || !linearPosition(position)) {
    return { value: position.unrealizedPnl, estimated: false };
  }

  const delta = subtract(index, entry);
  const direction = positionDirection(position, quantity);
  const pnl = multiply({ units: abs(quantity.units) * direction, scale: quantity.scale }, delta);
  return { value: fixedString(pnl), estimated: true, ...(liveIndexStale ? { stale: true } : {}) };
}

/** Zero is the venue's unavailable sentinel, not a real liquidation level. */
export function liquidationPrice(position: AccountPosition): string | undefined {
  return positiveDecimal(position.liquidationPrice) ? position.liquidationPrice : undefined;
}

function linearPosition(position: AccountPosition): boolean {
  if (position.product && position.product !== "perp") return false;
  const symbol = position.symbol || instrumentSymbol(position.instrumentId);
  const [, quote] = symbol.toUpperCase().split(/[-_/]/);
  const settlement = position.settlementAsset?.toUpperCase();
  return Boolean(settlement && quote && settlement === quote);
}

function positionDirection(position: AccountPosition, quantity: FixedDecimal): bigint {
  const side = position.side?.toLowerCase();
  if (side === "sell" || side === "short") return -1n;
  if (side === "buy" || side === "long") return 1n;
  return quantity.units < 0n ? -1n : 1n;
}

function positiveDecimal(value: string | null | undefined): boolean {
  const parsed = parseFixed(value);
  return parsed !== null && parsed.units > 0n;
}

function parseFixed(value: string | null | undefined): FixedDecimal | null {
  if (!value) return null;
  const match = /^([+-]?)(\d+)(?:\.(\d+))?$/.exec(value.trim());
  if (!match) return null;
  const fraction = match[3] ?? "";
  const magnitude = BigInt((match[2] ?? "0") + fraction);
  return { units: match[1] === "-" ? -magnitude : magnitude, scale: fraction.length };
}

function subtract(left: FixedDecimal, right: FixedDecimal): FixedDecimal {
  const scale = Math.max(left.scale, right.scale);
  return {
    units: scaledUnits(left, scale) - scaledUnits(right, scale),
    scale,
  };
}

function multiply(left: FixedDecimal, right: FixedDecimal): FixedDecimal {
  return { units: left.units * right.units, scale: left.scale + right.scale };
}

function scaledUnits(value: FixedDecimal, scale: number): bigint {
  return value.units * 10n ** BigInt(scale - value.scale);
}

function fixedString(value: FixedDecimal): string {
  const negative = value.units < 0n;
  const magnitude = abs(value.units).toString().padStart(value.scale + 1, "0");
  if (value.scale === 0) return `${negative ? "-" : ""}${magnitude}`;
  const integer = magnitude.slice(0, -value.scale);
  const fraction = magnitude.slice(-value.scale).replace(/0+$/, "");
  const unsigned = fraction ? `${integer}.${fraction}` : integer;
  return `${negative && unsigned !== "0" ? "-" : ""}${unsigned}`;
}

function abs(value: bigint): bigint {
  return value < 0n ? -value : value;
}

export function addDecimalValues(values: string[]): string {
  let total: FixedDecimal = { units: 0n, scale: 0 };
  for (const value of values) { const parsed = parseFixed(value); if (!parsed) continue; const scale = Math.max(total.scale, parsed.scale); total = { units: scaledUnits(total, scale) + scaledUnits(parsed, scale), scale }; }
  return fixedString(total);
}
