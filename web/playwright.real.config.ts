import { defineConfig, devices } from "@playwright/test";

import { assertSafeRealPlatformURL } from "./e2e/real-acceptance-helpers";

const optedIn = process.env.BT_DEV_REAL_BROWSER_ACCEPTANCE === "1";
const mode = process.env.FT_MODE;
const requiredEnvironment = [
  "BT_DEV_REAL_PLATFORM_URL",
  "BT_DEV_REAL_USERNAME",
  "BT_DEV_REAL_PASSWORD",
  "BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL",
  "BT_DEV_REAL_CREDENTIAL_CANARY",
  "BT_DEV_REAL_CREDENTIAL_SECRET_CANARY",
] as const;

if (mode === "live") {
  throw new Error("Real-browser acceptance refuses FT_MODE=live before browser launch.");
}
if (optedIn && mode !== "test") {
  throw new Error("Real-browser acceptance requires exact FT_MODE=test.");
}
if (optedIn) {
  const missing = requiredEnvironment.filter((name) => !process.env[name]?.trim());
  if (missing.length > 0) throw new Error(`Real-browser acceptance requires ${missing.join(", ")}.`);
  assertSafeRealPlatformURL(process.env.BT_DEV_REAL_PLATFORM_URL!);
  const harnessURL = new URL(process.env.BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL!);
  if ((harnessURL.protocol !== "http:" && harnessURL.protocol !== "https:")
    || harnessURL.hostname !== "127.0.0.1"
    || !harnessURL.port || harnessURL.username || harnessURL.password
    || harnessURL.pathname !== "/__bt/resnapshot" || harnessURL.search || harnessURL.hash) {
    throw new Error("Real-browser acceptance requires a loopback-only resnapshot harness.");
  }
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
    trace: "off",
    screenshot: "only-on-failure",
  },
  projects: [
    { name: "real-chromium", use: { ...devices["Desktop Chrome"], channel: "chrome" } },
  ],
});
