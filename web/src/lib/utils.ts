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

const COLORS = [
  "bg",
  "panel",
  "raised",
  "overlay",
  "line",
  "line-strong",
  "text",
  "muted",
  "dim",
  "accent",
  "accent-hover",
  "accent-fg",
  "up",
  "down",
  "ok",
  "pending",
  "warn",
  "danger",
  "manual",
] as const;

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
      spacing: ["row", "bar"],
      radius: ["sm", "md", "lg"],
    },
  },
});

export function cn(...inputs: ClassValue[]): string {
  return twMerge(clsx(inputs));
}
