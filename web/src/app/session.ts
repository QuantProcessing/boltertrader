/* ---------------------------------------------------------------------------
 * Who is signed in.
 *
 * A plain store rather than a React context, because two things that are not
 * both React need the same answer: the router's `beforeLoad` guard, which runs
 * outside rendering and must be able to `await` the first probe, and the
 * components, which need to re-render when it changes. `useSyncExternalStore`
 * bridges the second to the first without duplicating the state.
 *
 * The session itself lives in an HttpOnly cookie this code cannot read. What is
 * held here is the server's answer about that cookie — `GET /api/v1/me` — plus
 * the CSRF token that came with it, which `api/client` keeps in memory. So a
 * reload starts at `unknown`, asks once, and lands on one of the other two.
 * ------------------------------------------------------------------------- */

import { useSyncExternalStore } from "react";

import { advanceSessionEpoch, setCsrfToken } from "@/api/client";
import { auth } from "@/api/endpoints";
import { isAuthFailure, isStaleSession } from "@/api/errors";
import type { SessionState } from "@/api/types";

/**
 * `unknown` is not a loading flag bolted on — it is the honest third answer.
 * Treating it as "signed out" would flash the login form on every reload of an
 * authenticated tab; treating it as "signed in" would render an account screen
 * for a principal who may not exist.
 */
export type SessionStatus = "unknown" | "anonymous" | "authenticated";

export interface SessionSnapshot {
  status: SessionStatus;
  state: SessionState | null;
}

const UNKNOWN: SessionSnapshot = { status: "unknown", state: null };
const ANONYMOUS: SessionSnapshot = { status: "anonymous", state: null };

export class SessionStore {
  #snapshot: SessionSnapshot = UNKNOWN;
  #listeners = new Set<() => void>();
  #probe: Promise<SessionSnapshot> | null = null;

  subscribe = (listener: () => void): (() => void) => {
    this.#listeners.add(listener);
    return () => {
      this.#listeners.delete(listener);
    };
  };

  /**
   * Must return a stable reference for an unchanged session, or
   * `useSyncExternalStore` re-renders forever. That is why the snapshot is
   * replaced wholesale on change and never mutated in place.
   */
  getSnapshot = (): SessionSnapshot => this.#snapshot;

  /**
   * Resolves once the status is known, asking the server at most once no matter
   * how many callers race here — route guards and components both land on this
   * during the first paint.
   */
  ensure = async (): Promise<SessionSnapshot> => {
    if (this.#snapshot.status !== "unknown") return this.#snapshot;
    this.#probe ??= this.#probeOnce();
    return this.#probe;
  };

  async #probeOnce(): Promise<SessionSnapshot> {
    try {
      const state = await auth.me();
      this.#publish({ status: "authenticated", state });
    } catch (error) {
      // A 401 here is the ordinary case — an unauthenticated visitor. Anything
      // else (the auth edge down, a network failure) is NOT proof of being
      // signed out, but there is nothing to render without an identity, so it
      // resolves the same way and the login screen surfaces the real error when
      // the attempt fails again for the same reason.
      if (!isStaleSession(error)) this.#publish(ANONYMOUS);
    } finally {
      this.#probe = null;
    }
    return this.#snapshot;
  }

  login = async (username: string, password: string, signal?: AbortSignal): Promise<SessionState> => {
    const state = await auth.login(username, password, signal);
    this.#publish({ status: "authenticated", state });
    return state;
  };

  /**
   * Clears local state whether or not the server agreed. `auth.logout` already
   * drops the CSRF token and advances the epoch in its own `finally`, so a
   * failed request still ends with a client that cannot write.
   */
  logout = async (): Promise<void> => {
    try {
      await auth.logout();
    } finally {
      this.#publish(ANONYMOUS);
    }
  };

  /**
   * Called by the query cache when any request comes back unauthorized.
   *
   * This is what catches an expiry mid-session: nothing polls `/me`, so the
   * first request to fail is the notification. Advancing the epoch discards
   * every other in-flight answer, which stops a half-loaded screen from
   * finishing its paint under the wrong identity.
   */
  reportAuthFailure = (error: unknown): void => {
    if (!isAuthFailure(error) || this.#snapshot.status === "anonymous") return;
    setCsrfToken(null);
    advanceSessionEpoch();
    this.#publish(ANONYMOUS);
  };

  #publish(snapshot: SessionSnapshot): void {
    this.#snapshot = snapshot;
    for (const listener of this.#listeners) listener();
  }
}

export const sessionStore = new SessionStore();

export function useSession(): SessionSnapshot {
  return useSyncExternalStore(sessionStore.subscribe, sessionStore.getSnapshot, sessionStore.getSnapshot);
}

/**
 * The signed-in principal, or a throw.
 *
 * For everything rendered inside the authenticated shell, where the route guard
 * has already established there is one. Views below that point should not each
 * carry a null branch for a state their own route made impossible.
 */
export function usePrincipal(): { id: string; username: string } {
  const { state } = useSession();
  if (!state) throw new Error("usePrincipal was called outside an authenticated route");
  return state.principal;
}
