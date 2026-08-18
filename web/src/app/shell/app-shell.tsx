import { Outlet, useLocation, useMatches, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { useSession } from "@/app/session";
import { CommandPalette } from "./command-palette";
import { EntryDialog } from "./entry-dialog";
import { TopNav } from "./top-nav";

/* ---------------------------------------------------------------------------
 * The frame every route inside the app renders inside.
 *
 * `h-dvh` with an internal scroll rather than a page that grows: this is a
 * terminal, and a chart, a book and an order ticket that scroll off the bottom
 * of the window together are worse than any of them being shorter. Each panel
 * owns its own overflow.
 *
 * Two kinds of visitor reach it. An authenticated one gets the product. A
 * signed-out one gets here only on a route that opted into read-only, and gets
 * the market with every private surface either absent or saying why — plus the
 * entry dialog, once, over the top of it.
 * ------------------------------------------------------------------------- */

export function AppShell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const { status } = useSession();
  const navigate = useNavigate();
  const location = useLocation();
  const readOnlyRoute = useRouteOpensToReadOnly();

  /**
   * The route guard runs on navigation. This catches the other case: a session
   * that expires while the operator sits on one screen, which surfaces as a 401
   * on whatever query happens to fire next. Without this the shell would keep
   * rendering stale numbers behind a single failed panel.
   *
   * A read-only visitor is signed out on purpose and must not be swept up by
   * it — that is what `readOnlyRoute` is doing in the condition, and it is the
   * whole difference between "your session ended" and "you never had one".
   *
   * The pathname check is what stops the effect eating its own output. This
   * shell stays mounted until the login route's `beforeLoad` resolves, so
   * `location.href` is already `/login?redirect=…` while the effect is still
   * live; since that href is a dependency, the next run wraps the wrapper, once
   * per render, and signing out lands on a multi-kilobyte address that has to
   * bounce a few dozen times to unwrap itself.
   */
  useEffect(() => {
    if (status !== "anonymous" || readOnlyRoute || location.pathname === "/login") return;
    void navigate({ to: "/login", search: { redirect: location.href }, replace: true });
  }, [status, readOnlyRoute, navigate, location.href, location.pathname]);

  if (status === "unknown" || (status === "anonymous" && !readOnlyRoute)) {
    return <div className="h-dvh bg-bg" aria-busy="true" />;
  }

  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-bg text-text">
      <TopNav onOpenPalette={() => setPaletteOpen(true)} />
      <main className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <Outlet />
      </main>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
      <EntryDialog />
    </div>
  );
}

/**
 * Whether the route on screen is one a signed-out visitor may read.
 *
 * The router's guard already decided this once; asking the same matches again
 * is what keeps the two answers from drifting.
 *
 * This is a question about the *route*, and it is not the same question as
 * "can this viewer write" — an authenticated operator on `/trade` matches here
 * too, and must still get the whole product. `useReadOnly` in `app/mode` is the
 * one that answers for the viewer.
 */
function useRouteOpensToReadOnly(): boolean {
  const matches = useMatches();
  return matches[matches.length - 1]?.staticData.readOnly === true;
}
