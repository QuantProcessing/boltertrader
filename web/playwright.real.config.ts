import { defineConfig, devices } from "@playwright/test";

const optedIn = process.env.BT_DEV_REAL_BROWSER_ACCEPTANCE === "1";
const mode = process.env.FT_MODE;

if (mode === "live") {
  throw new Error("Real-browser acceptance refuses FT_MODE=live before browser launch.");
}
if (optedIn && mode !== "test") {
  throw new Error("Real-browser acceptance requires exact FT_MODE=test.");
}

const baseURL = process.env.BT_DEV_REAL_PLATFORM_URL?.trim() || "http://127.0.0.1:13000";

if (!optedIn) {
  console.warn("SKIP real-browser acceptance: set BT_DEV_REAL_BROWSER_ACCEPTANCE=1 and FT_MODE=test to opt in.");
}

export default defineConfig({
  testDir: "./e2e",
  testMatch: "platform-real.spec.ts",
  fullyParallel: false,
  use: {
    baseURL,
    trace: "on-first-retry",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "real-chromium", use: { ...devices["Desktop Chrome"], channel: "chrome" } },
  ],
});
