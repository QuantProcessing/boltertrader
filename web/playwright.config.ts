import { defineConfig, devices } from "@playwright/test";

/* ---------------------------------------------------------------------------
 * Two suites, one browser.
 *
 * `offline` walks the MVP path against a fake auth edge installed in the page.
 * It is the default because AGENTS.md requires the default test path to be
 * offline and deterministic: it needs no postgres, no gateway, no venue and no
 * credentials, so it can run anywhere and cannot place an order anywhere.
 *
 * `live` walks the same path against a real `make up` stack and is opt-in via
 * BOLTER_E2E_LIVE=1, in the same shape as the Makefile's other live gates. It
 * is the one that actually proves the plan's P5 acceptance, because the fake
 * proves the frontend and only the real one proves the seam.
 *
 * Both drive the Vite dev server, whose `/api` proxy points at the auth edge —
 * the offline suite simply intercepts before the proxy is reached.
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
    {
      name: "offline",
      testMatch: /.*\.offline\.spec\.ts/,
      use: { ...devices["Desktop Chrome"] },
    },
    ...(live
      ? [
          {
            name: "live",
            testMatch: /.*\.live\.spec\.ts/,
            use: { ...devices["Desktop Chrome"] },
          },
        ]
      : []),
  ],

  webServer: {
    command: "npm run dev",
    url: baseURL,
    // Reuse whatever is already listening in development; start a clean one in
    // CI so a stale server cannot serve a build nobody just made.
    reuseExistingServer: !process.env.CI,
    timeout: 60_000,
    stdout: "ignore",
    stderr: "pipe",
  },
});
