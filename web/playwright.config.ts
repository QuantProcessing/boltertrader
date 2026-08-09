import { defineConfig, devices } from "@playwright/test";

export default defineConfig({
  testDir: "./e2e",
  testIgnore: "platform-real.spec.ts",
  fullyParallel: false,
  use: {
    baseURL: "http://127.0.0.1:13001",
    trace: "on-first-retry",
  },
  projects: [
    { name: "chromium", use: { ...devices["Desktop Chrome"], channel: "chrome" } },
  ],
  webServer: {
    command: "npm run dev -- --host 127.0.0.1 --port 13001",
    url: "http://127.0.0.1:13001",
    reuseExistingServer: !process.env.CI,
  },
});
