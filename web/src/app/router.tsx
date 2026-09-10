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
 *
 * Read-only visitors are the one exception, and they are an *opt-in* one for
 * exactly that reason: a route is readable without a session only if it says
 * `staticData: { readOnly: true }` itself. The default stays "protected", so
 * the sentence above still holds — forgetting to opt in costs a screen, never
 * a leak.
 * ------------------------------------------------------------------------- */

import type { QueryClient } from "@tanstack/react-query";
import { Outlet, createRootRouteWithContext, createRoute, createRouter, redirect } from "@tanstack/react-router";

import type { SessionStore } from "@/app/session";
import { AppShell } from "@/app/shell/app-shell";
import { ActivityPage } from "@/routes/activity";
import { FundingPage } from "@/routes/funding";
import { LoginPage } from "@/routes/login";
import { PortfolioPage, validatePortfolioSearch } from "@/routes/portfolio";
import { SettingsPage } from "@/routes/settings";
import { TradePage } from "@/routes/trade";

export interface RouterContext {
  session: SessionStore;
  queryClient: QueryClient;
}

/**
 * A route that a signed-out visitor may read.
 *
 * Declaring it here rather than passing a literal at each call site is what
 * makes `staticData: { readOnly: true }` type-checked — TanStack's
 * `StaticDataRouteOption` is an empty interface published for exactly this
 * merge, so without it a typo like `readonly: true` would compile and silently
 * protect the route it was meant to open.
 */
declare module "@tanstack/react-router" {
  interface StaticDataRouteOption {
    readOnly?: boolean;
    /** The route uses the full viewport rather than the shared reading width. */
    fullBleed?: boolean;
  }
}

/** Whether the route being navigated to opted into read-only access. */
function opensToReadOnly(matches: ReadonlyArray<{ staticData: { readOnly?: boolean } }>): boolean {
  // The last match is the leaf being navigated to. `beforeLoad` on a layout
  // receives the whole destination lane, not just its own ancestors, which is
  // what lets one guard on the layout answer for every route beneath it.
  return matches[matches.length - 1]?.staticData.readOnly === true;
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
  beforeLoad: async ({ context, location, matches }) => {
    const snapshot = await context.session.ensure();
    if (snapshot.status === "authenticated") return;
    // A signed-out visitor reaches a read-only route on the strength of the
    // route saying so, and nothing else. They are not sent to `/login` and not
    // asked to make an entry choice here: the disclosure renders inside the
    // shell, so it can sit over a live market rather than a blank page.
    if (opensToReadOnly(matches)) return;
    // `location.href` is the full path with search, so signing in returns the
    // operator to the screen they asked for rather than to the default one.
    throw redirect({ to: "/login", search: { redirect: location.href }, replace: true });
  },
  component: AppShell,
});

const indexRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/",
  // Read-only because it is a doorway, not a screen: the layout guard runs
  // before this redirect does, so without the opt-in a signed-out visitor
  // typing the bare origin would be sent to `/login` instead of to the
  // read-only `/trade` this points at.
  staticData: { readOnly: true },
  beforeLoad: () => {
    throw redirect({ to: "/trade", replace: true });
  },
});

/**
 * The two routes a signed-out visitor may read.
 *
 * Both are venue public data end to end — a book, a chart, a funding board —
 * and the auth edge's `Public` carve-out is what actually serves them without a
 * session. This flag and that table have to agree: opening a route here whose
 * requests the edge still guards renders a screen of 401s.
 *
 * `/portfolio` and `/activity` are private by nature. `/settings` opens only
 * far enough to explain why account configuration requires sign-in; the page
 * mounts no private query until a session is authenticated.
 */
const tradeRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/trade",
  staticData: { readOnly: true, fullBleed: true },
  component: TradePage,
});

const fundingRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/funding",
  staticData: { readOnly: true },
  component: FundingPage,
});

/**
 * Which pair, and which way round.
 *
 * All three are optional and all three are hints: the page ranks the spreads
 * itself and falls back to the best executable one, so a bare strategy route
 * is a valid arrival. `long`/`short` exist because the server's orientation is
 * the profitable one and an operator is allowed to disagree — putting the choice
 * in the URL is what keeps a fifteen-second poll from undoing it.
 */
const portfolioRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/portfolio",
  validateSearch: validatePortfolioSearch,
  component: PortfolioPage,
});
const activityRoute = createRoute({ getParentRoute: () => appRoute, path: "/activity", component: ActivityPage });
const settingsRoute = createRoute({
  getParentRoute: () => appRoute,
  path: "/settings",
  staticData: { readOnly: true },
  component: SettingsPage,
});

const routeTree = rootRoute.addChildren([
  loginRoute,
  appRoute.addChildren([
    indexRoute,
    tradeRoute,
    fundingRoute,
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
