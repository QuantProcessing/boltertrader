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
      className={cn("min-w-0 rounded-md border border-line bg-panel", className)}
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

/** The metric strip above the panels. Label over value, nothing else. */
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
      <div className="mt-1 flex items-baseline gap-1.5">
        <span
          data-numeric=""
          className={cn(
            "truncate text-metric leading-none font-medium",
            tone === "up" && "text-up",
            tone === "down" && "text-down",
          )}
        >
          {value}
        </span>
        {unit ? <span className="shrink-0 text-meta text-dim">{unit}</span> : null}
      </div>
    </div>
  );
}
