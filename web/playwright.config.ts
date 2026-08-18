import { defineConfig, devices } from "@playwright/test";

/* ---------------------------------------------------------------------------
 * Two suites, one browser, one dev server — and therefore never both at once.
 *
 * `offline` walks the MVP path against the MSW mock, served by `npm run
 * dev:mock`. The name is about credentials, not connectivity: it needs no
 * postgres, no gateway and no key, so it can run anywhere and cannot place an
 * order anywhere. It does need the internet. The mock answers the market half
 * of the surface by reading the venues' public endpoints, so the prices this
 * suite sees are today's, and any assertion it makes has to hold for every
 * price rather than for one. Since the mock lives in the dev server rather than
 * in `page.route`, that property belongs to the server this config starts,
 * which is why the suite and the command are chosen together below.
 *
 * `live` walks the same path against a real `make up` stack and is opt-in via
 * BOLTER_E2E_LIVE=1, in the same shape as the Makefile's other live gates. It
 * is the one that actually proves the acceptance, because the mock proves the
 * frontend and only the real stack proves the seam.
 *
 * A run is one or the other. Registering both under a single server would put
 * one of them in front of an edge it was not written for: the offline suite
 * would try its fixture credentials against a real auth edge, and the live
 * suite would place its order into a mock and believe it had traded.
 * ------------------------------------------------------------------------- */

const live = process.env.BOLTER_E2E_LIVE === "1";
const baseURL = process.env.BOLTER_E2E_BASE_URL ?? "http://127.0.0.1:5173";

export default defineConfig({
  testDir: "./e2e",
  // One worker: the live suite places and cancels real orders on one account,
  // and two of it at once would be racing over the same blotter.
  workers: 1,
  fullyParallel: false,
  forbidOnly: !!process.env.CI,
  retries: process.env.CI ? 1 : 0,
  reporter: process.env.CI ? [["github"], ["list"]] : [["list"]],
  expect: { timeout: 10_000 },
  timeout: 60_000,

  use: {
    baseURL,
    // A trading terminal is not a phone target; this is the shape it is used in.
    viewport: { width: 1440, height: 900 },
    trace: "on-first-retry",
    screenshot: "only-on-failure",
    video: "off",
  },

  projects: [
    live
      ? {
          name: "live",
          testMatch: /.*\.live\.spec\.ts/,
          use: { ...devices["Desktop Chrome"] },
        }
      : {
          name: "offline",
          testMatch: /.*\.offline\.spec\.ts/,
          use: { ...devices["Desktop Chrome"] },
        },
  ],

  webServer: {
    // The suite's edge, chosen by the same flag that chose the suite. `dev:mock`
    // sets VITE_BOLTER_MOCK, which is what makes `main.tsx` start the worker;
    // plain `dev` proxies `/api` to the real auth edge.
    command: live ? "npm run dev" : "npm run dev:mock",
    url: baseURL,
    // Reuse whatever is already listening in development; start a clean one in
    // CI so a stale server cannot serve a build nobody just made. Note the edge
    // is a property of that server now, so a reused `npm run dev` will fail the
    // offline suite at sign-in rather than silently mocking — loudly, which is
    // the direction this should fail in.
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
