import { cva, type VariantProps } from "class-variance-authority";
import type { ComponentProps } from "react";

import { Tooltip } from "@/components/ui/tooltip";
import { cn } from "@/lib/utils";

/**
 * State is never communicated by colour alone.
 *
 * PRODUCT.md requires provisional, stale, degraded, uncertain, blocked and
 * manual-intervention to be distinguishable, and WCAG 2.2 requires that the
 * distinction survive when colour does not land — colour blindness, a
 * monochrome display, a screenshot pasted into a ticket. So the dot is
 * decorative and hidden from assistive technology, and the adjacent label is
 * the actual signal. There is deliberately no way to render this without one.
 */
const dot = cva("size-1.5 shrink-0 rounded-full", {
  variants: {
    tone: {
      ok: "bg-ok",
      pending: "bg-pending",
      warn: "bg-warn",
      danger: "bg-danger",
      manual: "bg-manual",
      neutral: "bg-dim",
    },
  },
  defaultVariants: { tone: "neutral" },
});

export type StateTone = NonNullable<VariantProps<typeof dot>["tone"]>;

export interface StateBadgeProps extends Omit<ComponentProps<"span">, "children"> {
  tone: StateTone;
  label: string;
  /** Longer explanation surfaced in a hover/focus tooltip and to screen readers. */
  detail?: string;
}

export function StateBadge({ tone, label, detail, className, ...props }: StateBadgeProps) {
  const badge = (
    <span
      className={cn("inline-flex items-center gap-1.5 whitespace-nowrap", className)}
      tabIndex={detail ? 0 : undefined}
      {...props}
    >
      <span className={dot({ tone })} aria-hidden="true" />
      <span>{label}</span>
      {detail ? <span className="sr-only">{detail}</span> : null}
    </span>
  );
  return detail ? <Tooltip content={detail}>{badge}</Tooltip> : badge;
}

/** A count or short qualifier that sits beside a title. Carries no state. */
export function Tag({ className, ...props }: ComponentProps<"span">) {
  return (
    <span
      className={cn(
        "th inline-flex h-5 items-center rounded-pill border border-line px-1.5",
        className,
      )}
      {...props}
    />
  );
}
