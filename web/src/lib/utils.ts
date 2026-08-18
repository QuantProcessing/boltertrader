import { clsx, type ClassValue } from "clsx";
import { extendTailwindMerge } from "tailwind-merge";

/**
 * tailwind-merge has to be taught this theme's scales.
 *
 * Out of the box it groups every `text-*` class together and keeps the last
 * one, which silently deletes `text-danger` when a component also sets
 * `text-meta`. Nothing errors; the class is simply absent and the element
 * inherits its parent colour — a rejected order that renders in body grey.
 *
 * Listing the custom font sizes and colours separates the two groups again, so
 * a size and a colour can coexist and only same-group classes override.
 */
const FONT_SIZES = ["micro", "meta", "base", "title", "metric"] as const;

/**
 * Every colour in `@theme inline`, and nothing that is not.
 *
 * This list is the other half of `theme.css`: a token added there and forgotten
 * here still paints, but stops overriding — `cn("bg-panel", "bg-fill")` would
 * emit both and let the cascade decide. So the rule is that the two move
 * together, the same rule `chart-palette.ts` carries for the chart.
 */
const COLORS = [
  "bg",
  "panel",
  "raised",
  "overlay",
  "fill",
  "fill-strong",
  "line",
  "line-strong",
  "text",
  "muted",
  "dim",
  "faint",
  "accent",
  "accent-hover",
  "accent-fg",
  "up",
  "down",
  "buy",
  "buy-fg",
  "sell",
  "sell-fg",
  "ok",
  "pending",
  "warn",
  "danger",
  "manual",
] as const;

/** Semantic rather than a t-shirt scale; see design.md §5. Same rule as COLORS. */
const RADII = ["seam", "control", "pill", "card", "modal", "panel"] as const;

const twMerge = extendTailwindMerge({
  extend: {
    classGroups: {
      "font-size": [{ text: [...FONT_SIZES] }],
      "text-color": [{ text: [...COLORS] }],
      "bg-color": [{ bg: [...COLORS] }],
      "border-color": [{ border: [...COLORS] }],
      "border-w": [{ border: [] }],
    },
    theme: {
      // `h-row`, `min-h-bar` and friends resolve through the spacing scale.
      spacing: ["row", "bar", "depth", "side", "ticket", "deck"],
      radius: [...RADII],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
