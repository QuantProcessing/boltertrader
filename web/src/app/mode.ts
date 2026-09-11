/* ---------------------------------------------------------------------------
 * Whether the current viewer may use private data and write actions.
 *
 * Authentication is the sole product boundary: signed out is read-only and
 * signed in is read-write. The server's authorization and operational live-
 * write switch remain the final authority.
 * ------------------------------------------------------------------------- */

import { useSession } from "@/app/session";

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
