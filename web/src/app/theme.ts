import { useSyncExternalStore } from "react";
export type Theme = "light" | "dark" | "system";
const key = "bolter.theme.v1";
const listeners = new Set<() => void>();
const media =
  typeof matchMedia === "function"
    ? matchMedia("(prefers-color-scheme: dark)")
    : null;
let theme: Theme = "system";
try {
  const saved = localStorage.getItem(key);
  if (saved === "light" || saved === "dark") theme = saved;
} catch {
  /* local preference is optional */
}
function apply() {
  if (typeof document !== "undefined")
    document.documentElement.dataset.theme =
      theme === "system" ? (media?.matches ? "dark" : "light") : theme;
  for (const listener of listeners) listener();
}
media?.addEventListener("change", apply);
apply();
import.meta.hot?.dispose(() => media?.removeEventListener("change", apply));
export function setTheme(next: Theme) {
  theme = next;
  try {
    localStorage.setItem(key, next);
  } catch {
    /* in-memory still works */
  }
  apply();
}
export function useTheme() {
  const preference = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    () => theme,
    () => "system" as Theme,
  );
  const resolved = useSyncExternalStore(
    (fn) => {
      listeners.add(fn);
      return () => {
        listeners.delete(fn);
      };
    },
    () => document.documentElement.dataset.theme ?? "dark",
    () => "dark",
  );
  return { preference, resolved };
}
