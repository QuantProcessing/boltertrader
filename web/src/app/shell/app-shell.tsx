import { Outlet, useLocation, useNavigate } from "@tanstack/react-router";
import { useEffect, useState } from "react";

import { useSession } from "@/app/session";
import { CommandPalette } from "./command-palette";
import { TopNav } from "./top-nav";

/* ---------------------------------------------------------------------------
 * The frame every authenticated route renders inside.
 *
 * `h-dvh` with an internal scroll rather than a page that grows: this is a
 * terminal, and a chart, a book and an order ticket that scroll off the bottom
 * of the window together are worse than any of them being shorter. Each panel
 * owns its own overflow.
 * ------------------------------------------------------------------------- */

export function AppShell() {
  const [paletteOpen, setPaletteOpen] = useState(false);
  const { status } = useSession();
  const navigate = useNavigate();
  const location = useLocation();

  /**
   * The route guard runs on navigation. This catches the other case: a session
   * that expires while the operator sits on one screen, which surfaces as a 401
   * on whatever query happens to fire next. Without this the shell would keep
   * rendering stale numbers behind a single failed panel.
   */
  useEffect(() => {
    if (status !== "anonymous") return;
    void navigate({ to: "/login", search: { redirect: location.href }, replace: true });
  }, [status, navigate, location.href]);

  if (status !== "authenticated") {
    return <div className="h-dvh bg-bg" aria-busy="true" />;
  }

  return (
    <div className="flex h-dvh flex-col overflow-hidden bg-bg text-text">
      <TopNav onOpenPalette={() => setPaletteOpen(true)} />
      <main className="flex min-h-0 flex-1 flex-col overflow-hidden">
        <Outlet />
      </main>
      <CommandPalette open={paletteOpen} onOpenChange={setPaletteOpen} />
    </div>
  );
}
