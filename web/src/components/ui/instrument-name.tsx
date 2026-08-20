import { instrumentIdentity, instrumentMeta } from "@/lib/instrument";
import { cn } from "@/lib/utils";

export function InstrumentName({
  instrumentId,
  context = true,
  className,
}: {
  instrumentId: string | null | undefined;
  /** Show venue and product when the id contains them. */
  context?: boolean;
  className?: string;
}) {
  const identity = instrumentIdentity(instrumentId);
  const meta = context ? instrumentMeta(instrumentId) : null;

  return (
    <span
      className={cn("inline-flex min-w-0 max-w-full items-baseline gap-2", className)}
      title={identity.raw || undefined}
    >
      <span className="truncate font-medium text-text" data-numeric="">
        {identity.symbol}
      </span>
      {meta ? <span className="shrink-0 text-micro text-dim">{meta}</span> : null}
    </span>
  );
}
