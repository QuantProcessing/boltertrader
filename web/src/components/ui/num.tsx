import type { ComponentProps } from "react";

import { decimalSign, formatDecimal, type FormatOptions } from "@/lib/format";
import { cn } from "@/lib/utils";

interface NumProps extends Omit<ComponentProps<"span">, "children">, FormatOptions {
  value: string | null | undefined;
  /** Colour by sign. Only for values where direction is the point: P&L, deltas. */
  directional?: boolean;
  /** A display unit that belongs to the same tabular value, for example %. */
  suffix?: string;
}

/**
 * Every number in the product renders through here, which is what guarantees
 * the tabular alignment and the string-safe formatting are not optional.
 */
export function Num({
  value,
  decimals,
  signed,
  grouped,
  directional = false,
  suffix,
  className,
  ...props
}: NumProps) {
  const sign = directional ? decimalSign(value) : 0;
  return (
    <span
      data-numeric=""
      className={cn(
        sign > 0 && "text-up",
        sign < 0 && "text-down",
        directional && sign === 0 && "text-muted",
        className,
      )}
      {...props}
    >
      {formatDecimal(value, { decimals, signed, grouped })}
      {suffix}
    </span>
  );
}

/** Buy/sell. Direction is state, so it is a word first and a colour second. */
export function Side({ side, className }: { side: string; className?: string }) {
  const normalized = side.toLowerCase();
  const isBuy = normalized === "buy" || normalized === "long";
  const isSell = normalized === "sell" || normalized === "short";
  return (
    <span
      className={cn(
        "font-medium capitalize",
        isBuy && "text-up",
        isSell && "text-down",
        !isBuy && !isSell && "text-muted",
        className,
      )}
    >
      {normalized}
    </span>
  );
}
