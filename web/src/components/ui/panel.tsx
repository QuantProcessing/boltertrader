import type { ComponentProps, ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * The only container in the product.
 *
 * Borders live on the outer edge and nowhere else: no nested panels, no cards
 * inside cards, no dividers between rows. Density comes from the 34px rhythm,
 * not from ruling every value off from its neighbour.
 */
export interface PanelProps extends Omit<ComponentProps<"section">, "title"> {
  title?: ReactNode;
  /** Right-aligned in the header. Row counts, freshness, a single control. */
  meta?: ReactNode;
  /** Removes the body padding, for tables that span the full width. */
  flush?: boolean;
}

export function Panel({ title, meta, flush = false, className, children, ...props }: PanelProps) {
  return (
    <section
      className={cn("min-w-0 rounded-panel border border-line bg-panel", className)}
      {...props}
    >
      {title !== undefined || meta !== undefined ? (
        <header className="flex h-row items-center justify-between gap-3 border-b border-line px-3">
          <h2 className="truncate text-base font-medium text-text">{title}</h2>
          {meta !== undefined ? (
            <div className="flex shrink-0 items-center gap-2 text-meta text-dim">{meta}</div>
          ) : null}
        </header>
      ) : null}
      <div className={cn(flush ? "" : "p-3")}>{children}</div>
    </section>
  );
}

/**
 * Label over value, at the density of a table cell.
 *
 * It used to be `--text-metric`, on the reading that these are the numbers a
 * page exists to show. They are — but every one of them sits in a 44px `h-bar`,
 * and a label plus 22px of leading measured 40 of those 44 pixels with the
 * page title beside it. That is the same finding the instrument strip reached
 * on real data, so this is the same fix: emphasis moves to weight and colour,
 * which cost no height at all.
 *
 * §6 still reserves 22px and its 700 weight for the one number a panel exists
 * to show. No panel in the product currently leads with one.
 */
export function Metric({
  label,
  value,
  unit,
  tone,
  className,
}: {
  label: string;
  value: ReactNode;
  unit?: string;
  tone?: "up" | "down" | "none";
  className?: string;
}) {
  return (
    <div className={cn("min-w-0", className)}>
      <div className="th truncate">{label}</div>
      <div className="flex items-baseline gap-1.5">
        <span
          data-numeric=""
          className={cn(
            "truncate text-base font-semibold",
            tone === "up" && "text-up",
            tone === "down" && "text-down",
            (tone === undefined || tone === "none") && "text-text",
          )}
        >
          {value}
        </span>
        {unit ? <span className="shrink-0 text-meta text-dim">{unit}</span> : null}
      </div>
    </div>
  );
}
