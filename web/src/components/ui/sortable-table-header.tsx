import { ArrowDown, ArrowUp, ArrowUpDown } from "lucide-react";
import type { MouseEventHandler, ReactNode } from "react";

import { cn } from "@/lib/utils";

export type TableSortDirection = "asc" | "desc" | false;

/**
 * One sorting affordance for every dense table in the product.
 *
 * `DataTable` exposes the state through the owning column's `aria-sort`.
 * Dense ranking lists reuse the same visual control and put the state in this
 * button's accessible name instead, because they deliberately remain lists.
 */
export function SortableTableHeader({
  children,
  direction,
  onToggle,
  align = "left",
  ariaLabel,
  className,
}: {
  children: ReactNode;
  direction: TableSortDirection;
  onToggle: MouseEventHandler<HTMLButtonElement>;
  align?: "left" | "right";
  ariaLabel?: string;
  className?: string;
}) {
  return (
    <button
      type="button"
      onClick={onToggle}
      aria-label={ariaLabel}
      data-table-sort=""
      className={cn(
        "inline-flex min-h-[24px] max-w-full items-center gap-1 rounded-control px-0.5 uppercase hover:text-text",
        align === "right" && "flex-row-reverse",
        direction ? "text-text" : "text-dim",
        className,
      )}
    >
      <span className="truncate">{children}</span>
      {direction === "asc" ? (
        <ArrowUp className="size-3 shrink-0" aria-hidden="true" />
      ) : direction === "desc" ? (
        <ArrowDown className="size-3 shrink-0" aria-hidden="true" />
      ) : (
        <ArrowUpDown className="size-3 shrink-0 text-dim" aria-hidden="true" />
      )}
    </button>
  );
}
