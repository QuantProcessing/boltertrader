import { useId } from "react";

import { cn } from "@/lib/utils";

export interface SegmentedOption<T extends string> {
  value: T;
  label: string;
  /** Applies the up/down semantic when the choice is a market direction. */
  tone?: "up" | "down";
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
}: {
  name: string;
  /** Group name for assistive technology. */
  label: string;
  value: T;
  options: SegmentedOption<T>[];
  onChange: (next: T) => void;
  className?: string;
}) {
  const id = useId();
  return (
    <fieldset className={cn("min-w-0", className)}>
      <legend className="sr-only">{label}</legend>
      <div className="flex w-full rounded-md border border-line-strong p-0.5">
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
                  "flex h-[26px] cursor-pointer items-center justify-center rounded-sm px-2 text-base font-medium",
                  "transition-colors select-none",
                  "peer-focus-visible:outline-2 peer-focus-visible:outline-offset-2 peer-focus-visible:outline-accent",
                  "peer-disabled:cursor-not-allowed peer-disabled:opacity-50",
                  "pointer-coarse:min-h-bar",
                  selected
                    ? option.tone === "up"
                      ? "bg-up text-bg"
                      : option.tone === "down"
                        ? "bg-down text-bg"
                        : "bg-raised text-text"
                    : "text-muted hover:text-text",
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
