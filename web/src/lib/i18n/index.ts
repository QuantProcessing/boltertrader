/* ---------------------------------------------------------------------------
 * Which language the product speaks.
 *
 * A hand-written dictionary rather than react-i18next, for the same reason
 * `lib/format.ts` renders decimals by hand rather than reaching for
 * `Intl.NumberFormat`: what is needed here is a typed lookup and an
 * interpolation, and a library that also does lazy namespace loading, plural
 * rule packs and backend connectors is a lot of surface to carry for that.
 * `en.ts` being the key source gets the one property that actually prevents
 * bugs — a missing translation is a compile error.
 *
 * Same store shape as `session.ts`: a plain store with
 * `useSyncExternalStore`. `localStorage` keeps language a property of the
 * reader across tabs.
 * ------------------------------------------------------------------------- */

import { useCallback, useSyncExternalStore } from "react";

import { setDateLocale } from "@/lib/format";
import { en, type Dictionary, type Key } from "./en";
import { zh } from "./zh";

export type Locale = "en" | "zh";

/**
 * Re-exported because a key is sometimes data rather than a call site: the order
 * book's view options are module-level arrays that name their label instead of
 * holding it, so they need the type without needing the English dictionary.
 */
export type { Key } from "./en";

/**
 * Every language, each named in itself — a reader who cannot read the current
 * interface cannot find "Chinese" in it either.
 *
 * `short` is for compact segmented controls, where two long names would not
 * fit; `label` is for menus, where the abbreviation would not be obvious. One
 * list rather than two, so a third language reaches both places.
 */
export const LOCALES: ReadonlyArray<{ value: Locale; label: string; short: string }> = [
  { value: "en", label: "English", short: "EN" },
  { value: "zh", label: "中文", short: "中文" },
];

const DICTIONARIES: Record<Locale, Dictionary> = { en, zh };

const STORAGE_KEY = "bolter.locale";

/**
 * The stored choice, else the browser's, else English.
 *
 * `navigator.language` is matched on its primary subtag, so `zh-Hans-CN`,
 * `zh-TW` and bare `zh` all land on Chinese. There is one Chinese dictionary
 * here and it is Simplified; a Traditional reader gets it rather than English,
 * which is the better of the two wrong answers available.
 */
function detect(): Locale {
  try {
    const stored = window.localStorage.getItem(STORAGE_KEY);
    if (stored === "en" || stored === "zh") return stored;
  } catch {
    // Storage can throw outright — Safari's private mode, blocked site data —
    // and a language preference is never worth a blank screen.
  }
  return navigator.language?.split("-")[0] === "zh" ? "zh" : "en";
}

export class LocaleStore {
  #locale: Locale = detect();
  #listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  };

  getSnapshot = (): Locale => this.#locale;

  set = (locale: Locale): void => {
    if (this.#locale === locale) return;
    this.#locale = locale;
    try {
      window.localStorage.setItem(STORAGE_KEY, locale);
    } catch {
      // Holds for this document either way.
    }
    // The one piece of state that is not React's to hold: assistive technology
    // and the browser's own hyphenation read `lang` off the document, not off a
    // provider. `index.html` ships `lang="en"`, and this is what keeps it true.
    document.documentElement.lang = locale;
    // Same reasoning one layer down: `Intl` formatters are built once and held,
    // so the month name has to be told the language changed.
    setDateLocale(locale);
    for (const listener of this.#listeners) listener();
  };
}

export const localeStore = new LocaleStore();

// Sync the document to the detected language before the first paint, so a
// Chinese-by-default visitor is not announced as reading English, and reads a
// date in the language the rest of the screen is about to be in.
document.documentElement.lang = localeStore.getSnapshot();
setDateLocale(localeStore.getSnapshot());

export function useLocale(): Locale {
  return useSyncExternalStore(localeStore.subscribe, localeStore.getSnapshot, localeStore.getSnapshot);
}

/** Substitutes `{name}` from `values`; an unmatched placeholder is left alone. */
function interpolate(template: string, values?: Record<string, string | number>): string {
  if (!values) return template;
  return template.replace(/\{(\w+)\}/g, (whole, name: string) =>
    name in values ? String(values[name]) : whole,
  );
}

export type Translate = (key: Key, values?: Record<string, string | number>) => string;

export function useT(): Translate {
  const locale = useLocale();
  const dictionary = DICTIONARIES[locale];
  // A translation dependency must not invalidate stateful table cells on every tick.
  return useCallback<Translate>((key, values) => interpolate(dictionary[key], values), [dictionary]);
}
