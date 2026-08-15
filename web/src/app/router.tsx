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
import { FundingPage } from "@/routes/funding";
import { ArbitragePage } from "@/routes/funding.arbitrage";
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
   *
   * A target pointing back at this route is dropped for a different reason: it
   * is never a destination, only a bounce, and one carrying its own `redirect`
   * is a stack of them. Dropping it costs the operator the return trip and
   * nothing else, and keeps a stale or hand-typed URL from unwrapping itself
   * one navigation at a time.
   */
  validateSearch: (search: Record<string, unknown>): LoginSearch => {
    const value = search.redirect;
    if (typeof value !== "string" || !value.startsWith("/") || value.startsWith("//")) return {};
    if (value === "/login" || value.startsWith("/login?") || value.startsWith("/login/")) return {};
    return { redirect: value };
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
const fundingRoute = createRoute({ getParentRoute: () => appRoute, path: "/funding", component: FundingPage });

/**
 * Which pair, and which way round.
 *
 * All three are optional and all three are hints: the page ranks the spreads
 * itself and falls back to the best executable one, so a bare `/funding/arbitrage`
 * is a valid arrival. `long`/`short` exist because the server's orientation is
 * the profitable one and an operator is allowed to disagree — putting the choice
 * in the URL is what keeps a fifteen-second poll from undoing it.
 */
interface ArbitrageSearch {
  symbol?: string;
  long?: string;
  short?: string;
}

const arbitrageRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/funding/arbitrage",
  validateSearch: (search: Record<string, unknown>): ArbitrageSearch => ({
    symbol: typeof search.symbol === "string" && search.symbol !== "" ? search.symbol : undefined,
    long: typeof search.long === "string" && search.long !== "" ? search.long : undefined,
    short: typeof search.short === "string" && search.short !== "" ? search.short : undefined,
  }),
  component: ArbitragePage,
});

const portfolioRoute = createRoute({ getParentRoute: () => appRoute, path: "/portfolio", component: PortfolioPage });
const activityRoute = createRoute({ getParentRoute: () => appRoute, path: "/activity", component: ActivityPage });
const settingsRoute = createRoute({ getParentRoute: () => appRoute, path: "/settings", component: SettingsPage });

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    tradeRoute,
    fundingRoute,
    arbitrageRoute,
    portfolioRoute,
    activityRoute,
    settingsRoute,
  ]),
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
