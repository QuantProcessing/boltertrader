/* ---------------------------------------------------------------------------
 * Which of the two modes this session is in.
 *
 * Read-only is the market and nothing else: a book, a chart, a funding board,
 * all of it public data the venues publish to anyone who asks. Read-write is
 * the product, and it needs a session because it can spend money.
 *
 * This is a *client* concept, and deliberately so. The server has its own
 * write switch (`BT_LIVE_PRIVATE_WRITES_ENABLED`, a process-level operational
 * cutout) and its own authorization; neither is replaced by anything here. What
 * this store decides is whether the browser has been told the risk and asked
 * for the second one — a question about this tab, which is why the answer lives
 * in `sessionStorage` and why the modal's footnote can honestly say the choice
 * lasts for the session.
 *
 * Read-only is *anonymous plus a mode*, not a third `SessionStatus`. That
 * matters more than it looks: `session.ts`'s `reportAuthFailure` short-circuits
 * on `status === "anonymous"`, so a read-only visitor who trips a 403 is left
 * where they are. A new status would slip past that guard and start logging
 * out visitors who were never logged in.
 *
 * Same shape as `session.ts` — a plain store with `useSyncExternalStore` over
 * it — because the same two callers need it: the router's `beforeLoad`, which
 * runs outside rendering, and components, which need to re-render on a change.
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

  /**
   * Back to being asked. Called on sign-out, where the alternative is a visitor
   * who chose read-write, signed out, and is now a read-write session with no
   * session — a state whose only symptom is being refused by the server.
   */
  reset = (): void => this.choose("undecided");
}

export const modeStore = new ModeStore();

export function useMode(): Mode {
  return useSyncExternalStore(modeStore.subscribe, modeStore.getSnapshot, modeStore.getSnapshot);
}

/**
 * Whether the viewer can only read.
 *
 * Derived from the session, not from the mode: the mode records what was asked
 * for, the session records what was granted, and only the second one decides
 * what a request will be allowed to do. A visitor who picked read-write but has
 * not signed in yet is still read-only, and this is what says so.
 *
 * Use it to decide what to *render* — a private query to skip, a control to
 * replace with "sign in to use". Do not use it as an authorization check.
 * Nothing on this side of the wire is one: the auth edge refuses anonymous
 * writes on its own, and this only keeps the product from asking for things it
 * already knows will be refused.
 */
export function useReadOnly(): boolean {
  return useSession().status !== "authenticated";
}
