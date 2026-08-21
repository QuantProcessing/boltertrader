import type { ComponentProps } from "react";

import type { StateTone } from "@/components/ui/state-badge";
import { cn } from "@/lib/utils";

const DOT_TONE: Record<StateTone, string> = {
  ok: "bg-ok",
  pending: "bg-pending",
  warn: "bg-warn",
  danger: "bg-danger",
  manual: "bg-manual",
  neutral: "bg-dim",
};

/** A compact identity chip. Status is optional; the venue remains the focus. */
export function VenueBadge({
  venue,
  tone = "neutral",
  status,
  detail,
  className,
  ...props
}: {
  venue: string;
  tone?: StateTone;
  status?: string;
  detail?: string;
} & Omit<ComponentProps<"span">, "children">) {
  return (
    <span
      className={cn(
        "inline-flex h-5 min-w-0 items-center gap-1.5 rounded-pill border border-line bg-raised px-1.5 text-micro font-medium text-text",
        className,
      )}
      title={detail}
      {...props}
    >
      <span aria-hidden className={cn("size-1.5 shrink-0 rounded-full", DOT_TONE[tone])} />
      <span className="truncate">{venue}</span>
      {status ? <span className={cn("font-normal", tone === "danger" ? "text-danger" : tone === "warn" ? "text-warn" : "text-dim")}>{status}</span> : null}
      {detail ? <span className="sr-only">{detail}</span> : null}
    </span>
  );
}
