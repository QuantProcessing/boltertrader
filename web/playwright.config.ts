import { defineConfig, devices } from "@playwright/test";

/* ---------------------------------------------------------------------------
 * The only browser acceptance suite uses the real stack and a production venue.
 * It places and cancels an order, so a caller must opt in explicitly and supply
 * the bootstrap password. The credential-free default test path stops at unit,
 * type and build checks; neither `npm test` nor `make test-browser` runs this.
 * ------------------------------------------------------------------------- */

if (process.env.BOLTER_E2E_LIVE !== "1") {
  throw new Error("BOLTER_E2E_LIVE=1 is required: this suite can place a real production order");
}

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
      name: "live",
      testMatch: /.*\.live\.spec\.ts/,
      use: { ...devices["Desktop Chrome"] },
    },
  ],

  webServer: {
    // The dev server proxies `/api` to the real auth edge. The stack and the
    // explicitly configured production account must already be available.
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
