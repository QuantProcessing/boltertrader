import type { LucideIcon } from "lucide-react";
import type { ReactNode } from "react";

import { cn } from "@/lib/utils";

/**
 * Empty is a state, not a blank space. It says what would be here and, when
 * there is one, offers the action that would put something here — but it never
 * implies the absence is a problem when it is simply a fresh account.
 */
export function EmptyState({
  icon: Icon,
  title,
  detail,
  action,
  className,
}: {
  icon?: LucideIcon;
  title: string;
  detail?: string;
  action?: ReactNode;
  className?: string;
}) {
  return (
    <div
      className={cn("flex flex-col items-center justify-center gap-2 px-6 py-10 text-center", className)}
    >
      {Icon ? <Icon className="size-5 text-dim" aria-hidden="true" strokeWidth={1.5} /> : null}
      <p className="text-base text-muted">{title}</p>
      {detail ? <p className="max-w-80 text-meta text-dim">{detail}</p> : null}
      {action ? <div className="mt-1">{action}</div> : null}
    </div>
  );
}

/** Holds layout while data loads. Sized in row units so nothing jumps on arrival. */
export function Skeleton({ className, ...props }: { className?: string } & Record<string, unknown>) {
  return <span aria-hidden="true" className={cn("block rounded-sm bg-raised", className)} {...props} />;
}
