/* ---------------------------------------------------------------------------
 * The route tree.
 *
 * Code-based rather than file-based: `@tanstack/router-plugin` is not a
 * dependency, so there is no generated route tree and these definitions are the
 * whole of it. Adding a route means adding it here and to `SECTIONS` in the top
 * nav — the two are deliberately not derived from each other, because not every
 * route belongs in the header.
 *
 * Authentication is a pathless layout route wrapping everything except
 * `/login`. Putting the guard on the layout rather than on each leaf means a
 * route added later is protected by default; forgetting to opt in cannot
 * produce an unguarded screen.
 * ------------------------------------------------------------------------- */

import type { QueryClient } from "@tanstack/react-query";
import { Outlet, createRootRouteWithContext, createRoute, createRouter, redirect } from "@tanstack/react-router";

import type { SessionStore } from "@/app/session";
import { AppShell } from "@/app/shell/app-shell";
import { ActivityPage } from "@/routes/activity";
import { LoginPage } from "@/routes/login";
import { PortfolioPage } from "@/routes/portfolio";
import { SettingsPage } from "@/routes/settings";
import { TradePage } from "@/routes/trade";

export interface RouterContext {
  session: SessionStore;
  queryClient: QueryClient;
}

const rootRoute = createRootRouteWithContext<RouterContext>()({
  component: Outlet,
});

interface LoginSearch {
  redirect?: string;
}

const loginRoute = createRoute({
  getParentRoute: () => rootRoute,
  path: "/login",
  /**
   * Only same-origin absolute paths survive validation. `redirect` comes out of
   * the address bar, and passing it to `navigate` unchecked would turn this
   * form into an open redirect: sign in here, land on someone else's page.
   */
  validateSearch: (search: Record<string, unknown>): LoginSearch => {
    const value = search.redirect;
    return typeof value === "string" && value.startsWith("/") && !value.startsWith("//") ? { redirect: value } : {};
  },
  beforeLoad: async ({ context, search }) => {
    const snapshot = await context.session.ensure();
    if (snapshot.status === "authenticated") {
      throw redirect({ href: search.redirect ?? "/trade", replace: true });
    }
  },
  component: LoginPage,
});

const appRoute = createRoute({
  getParentRoute: () => rootRoute,
  id: "app",
  beforeLoad: async ({ context, location }) => {
    const snapshot = await context.session.ensure();
    if (snapshot.status !== "authenticated") {
      // `location.href` is the full path with search, so signing in returns the
      // operator to the screen they asked for rather than to the default one.
      throw redirect({ to: "/login", search: { redirect: location.href }, replace: true });
    }
  },
  component: AppShell,
});

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  beforeLoad: () => {
    throw redirect({ to: "/trade", replace: true });
  },
});

const tradeRoute = createRoute({ getParentRoute: () => appRoute, path: "/trade", component: TradePage });
const portfolioRoute = createRoute({ getParentRoute: () => appRoute, path: "/portfolio", component: PortfolioPage });
const activityRoute = createRoute({ getParentRoute: () => appRoute, path: "/activity", component: ActivityPage });
const settingsRoute = createRoute({ getParentRoute: () => appRoute, path: "/settings", component: SettingsPage });

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([indexRoute, tradeRoute, portfolioRoute, activityRoute, settingsRoute]),
]);

export function createAppRouter(context: RouterContext) {
  // No `defaultPendingComponent`: the only thing a route's `beforeLoad` waits
  // on is the session probe, and each screen already renders its own waiting
  // state for its own data. A router-level spinner would only add a flash.
  return createRouter({ routeTree, context });
}

declare module "@tanstack/react-router" {
  interface Register {
    router: ReturnType<typeof createAppRouter>;
  }
}
