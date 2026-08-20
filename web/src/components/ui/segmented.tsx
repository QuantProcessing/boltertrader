import { useId } from "react";

import { cn } from "@/lib/utils";

export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
  /**
   * Fills the selected segment with a side colour, for the one control where
   * the choice *is* the side.
   *
   * Not `up`/`down`: those describe which way a price moved and are read as
   * text, while this puts a label on top of the colour — which is the whole
   * reason design.md §3 gives buy and sell their own pair.
   */
  tone?: "buy" | "sell";
  disabled?: boolean;
}

/**
 * Built on native radios rather than buttons with roving tabindex. The browser
 * already implements arrow-key traversal, group semantics and the announced
 * "2 of 3" position; reimplementing that in JavaScript is how it gets subtly
 * wrong. The inputs are visually hidden but focusable, and the ring is drawn
 * on the label via peer-focus-visible.
 */
export function Segmented<T extends string>({
  name,
  label,
  value,
  options,
  onChange,
  className,
  trackClassName,
}: {
  name: string;
  /** Group name for assistive technology. */
  label: string;
  value: T;
  options: SegmentedOption<T>[];
  onChange: (next: T) => void;
  className?: string;
  /** Optional spacing/layout override for one control's segment track. */
  trackClassName?: string;
}) {
  const id = useId();
  return (
    <fieldset className={cn("min-w-0", className)}>
      <legend className="sr-only">{label}</legend>
      {/* A track, which §4 gives `fill` rather than a border: the selected
          segment is what the eye needs to find, and a ring around the whole
          group competes with it. */}
      <div className={cn("flex w-full rounded-control bg-fill p-0.5", trackClassName)}>
        {options.map((option) => {
          const optionId = `${id}-${option.value}`;
          const selected = option.value === value;
          return (
            <div key={option.value} className="min-w-0 flex-1">
              <input
                id={optionId}
                type="radio"
                name={`${name}-${id}`}
                value={option.value}
                checked={selected}
                disabled={option.disabled}
                onChange={() => onChange(option.value)}
                className="peer sr-only"
              />
              <label
                htmlFor={optionId}
                className={cn(
                  "flex h-[26px] cursor-pointer items-center justify-center rounded-control px-2 text-base font-medium",
                  // A segment label that wraps is a segment label the track was
                  // never measured for: with `flex-1` the intrinsic width comes
                  // from whichever option is narrowest to break, so `EN | 中文`
                  // sized itself to `EN` and folded the Chinese onto two lines.
                  "whitespace-nowrap",
                  "transition-colors select-none",
                  "peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent",
                  "peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
                  "pointer-coarse:min-h-bar",
                  selected
                    ? option.tone === "buy"
                      ? "bg-buy text-buy-fg"
                      : option.tone === "sell"
                        ? "bg-sell text-sell-fg"
                        : "bg-raised text-text"
                    : "text-muted hover:bg-fill-strong hover:text-text",
                )}
              >
                {option.label}
              </label>
            </div>
          );
        })}
      </div>
    </fieldset>
  );
}
