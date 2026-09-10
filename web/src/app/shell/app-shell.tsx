import { useQueryClient } from "@tanstack/react-query";
import { Outlet, useLocation, useMatches, useNavigate } from "@tanstack/react-router";
import { useEffect } from "react";

import { useReadOnly } from "@/app/mode";
import { useSession } from "@/app/session";
import { useTradeRealtime } from "@/features/trade/realtime";
import { cn } from "@/lib/utils";
import { BottomStatus } from "./bottom-status";
import { EntryDialog } from "./entry-dialog";
import { TopNav, WorkspaceToolbar } from "./top-nav";

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
  const { status } = useSession();
  const readOnly = useReadOnly();
  useTradeRealtime(status === "authenticated" && !readOnly);
  const queryClient = useQueryClient();
  const navigate = useNavigate();
  const location = useLocation();
  const routePresentation = useRoutePresentation();
  const readOnlyRoute = routePresentation.readOnly;

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

  // Stopping private polling is not enough: React Query keeps the last result
  // by design. Remove every centrally marked private result when this tab
  // becomes anonymous so cached private account data cannot remain painted
  // from the authenticated session.
  useEffect(() => {
    if (!readOnly) return;
    queryClient.removeQueries({ predicate: (query) => query.meta?.privateData === true });
  }, [queryClient, readOnly]);

  if (status === "unknown" || (status === "anonymous" && !readOnlyRoute)) {
    return <div className="h-dvh bg-bg" aria-busy="true" />;
  }

  return (
    <div className="workspace-shell bg-bg text-text">
      <TopNav />
      <div className="workspace-main">
      <WorkspaceToolbar />
      <main className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <div
          className={cn(
            "flex min-h-0 flex-1 flex-col overflow-hidden",
            routePresentation.fullBleed ? "w-full" : "mx-auto w-full max-w-content",
          )}
        >
          <Outlet />
        </div>
      </main>
      <BottomStatus />
      </div>
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
function useRoutePresentation(): { readOnly: boolean; fullBleed: boolean } {
  const matches = useMatches();
  const leaf = matches[matches.length - 1]?.staticData;
  return {
    readOnly: leaf?.readOnly === true,
    fullBleed: leaf?.fullBleed === true,
  };
}
