/* ---------------------------------------------------------------------------
 * The answer given at the first-entry disclosure.
 *
 * Read-only is the anonymous product: a book, a chart, a funding board, all of
 * it public data the venues publish to anyone who asks. Choosing read-write
 * does not grant a browser capability; it sends the visitor to sign in.
 *
 * This store remembers only that the visitor has passed that door in this tab.
 * Authentication is the sole product boundary: signed out is read-only and
 * signed in is read-write. The server's authorization and operational live-
 * write switch remain the final authority.
 *
 * Read-only is another name for the anonymous session, not a third
 * `SessionStatus`. That
 * matters more than it looks: `session.ts`'s `reportAuthFailure` short-circuits
 * on `status === "anonymous"`, so a read-only visitor who trips a 403 is left
 * where they are. A new status would slip past that guard and start logging
 * out visitors who were never logged in.
 *
 * Same shape as `session.ts` — a plain store with `useSyncExternalStore` over
 * it — so the entry disclosure re-renders immediately after either choice.
 * ------------------------------------------------------------------------- */

import { useSyncExternalStore } from "react";

import { useSession } from "@/app/session";

/**
 * `undecided` is the state the entry modal exists to resolve, not a loading
 * flag. A fresh tab genuinely has not been asked yet, and the two other values
 * are both answers a visitor gave.
 */
export type Mode = "undecided" | "readOnly" | "readWrite";

const STORAGE_KEY = "bolter.mode";

/**
 * Survives a reload of the same tab, not a new one.
 *
 * `sessionStorage` rather than `localStorage` on purpose: the risk disclosure
 * is worth showing again to someone opening the product fresh, and a choice
 * that outlives the tab it was made in would quietly stop being a choice.
 *
 * Every access is wrapped because storage throws rather than returns null in
 * two ordinary configurations — Safari's private mode and a browser set to
 * block site data — and a storage failure must degrade to "ask again", never
 * to a blank screen.
 */
function read(): Mode {
  try {
    const stored = window.sessionStorage.getItem(STORAGE_KEY);
    return stored === "readOnly" || stored === "readWrite" ? stored : "undecided";
  } catch {
    return "undecided";
  }
}

function write(mode: Mode): void {
  try {
    if (mode === "undecided") window.sessionStorage.removeItem(STORAGE_KEY);
    else window.sessionStorage.setItem(STORAGE_KEY, mode);
  } catch {
    // Nothing to do and nothing to report: the mode still holds in memory for
    // as long as this document lives, which is the whole of what it promises.
  }
}

export class ModeStore {
  #mode: Mode = read();
  #listeners = new Set<() => void>();

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  };

  getSnapshot = (): Mode => this.#mode;

  choose = (mode: Mode): void => {
    if (this.#mode === mode) return;
    this.#mode = mode;
    write(mode);
    for (const listener of this.#listeners) listener();
  };
}

export const modeStore = new ModeStore();

export function useMode(): Mode {
  return useSyncExternalStore(modeStore.subscribe, modeStore.getSnapshot, modeStore.getSnapshot);
}

/**
 * Whether the viewer can only read.
 *
 * Authentication is the whole rule: anonymous visitors see public market data
 * only; authenticated operators get account data and write surfaces. There is
 * deliberately no "authenticated but read-only" browser state.
 *
 * Use it to decide what to *render* — a private query to skip, a control to
 * replace with "sign in to use". Do not use it as an authorization check.
 * Nothing on this side of the wire is one: the auth edge refuses anonymous
 * writes on its own, and this only keeps the product from asking for things it
 * already knows will be refused.
 */
export function useReadOnly(): boolean {
  const session = useSession();
  return session.status !== "authenticated";
}
