import { readFile } from "node:fs/promises";

import { expect, test } from "@playwright/test";

const optedIn = process.env.BT_DEV_REAL_BROWSER_ACCEPTANCE === "1";
const skipReason = "Real-browser acceptance requires BT_DEV_REAL_BROWSER_ACCEPTANCE=1 with exact FT_MODE=test.";
const forbiddenLabels = ["secretRef", "providerPath", "gatewayToken", "gateway-adapter-token", "authorization: bearer", "/run/secrets/", "/var/lib/secret-store"];
const credentialCanary = process.env.BT_DEV_REAL_CREDENTIAL_CANARY?.trim() || "credential-canary-never-render";

test.skip(!optedIn, skipReason);

test("real Test platform exposes only redacted account validation and recovery state", async ({ page, request }, testInfo) => {
  const username = process.env.BT_DEV_REAL_USERNAME?.trim();
  const password = process.env.BT_DEV_REAL_PASSWORD;
  if (!username || !password) throw new Error("Real-browser acceptance requires BT_DEV_REAL_USERNAME and BT_DEV_REAL_PASSWORD.");

  const consoleMessages: string[] = [];
  const responseBodies: string[] = [];
  const responseCaptures: Promise<void>[] = [];
  page.on("console", (message) => consoleMessages.push(message.text()));
  page.on("response", (response) => {
    if (!new URL(response.url()).pathname.startsWith("/api/v1/")) return;
    const contentType = response.headers()["content-type"] ?? "";
    if (contentType.includes("json")) responseCaptures.push(response.text().then((body) => { responseBodies.push(body); }).catch(() => undefined));
  });

  await page.goto("/");
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Email").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();
  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();

  const validations = page.getByRole("button", { name: /^Retry validation for / });
  if (await validations.count()) {
    await validations.first().click();
    await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  } else {
    await expect(page.getByText("No accounts configured")).toBeVisible();
  }

  const harnessURL = process.env.BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL?.trim();
  if (harnessURL) {
    const parsed = new URL(harnessURL);
    if (parsed.hostname !== "127.0.0.1" && parsed.hostname !== "localhost") throw new Error("The resnapshot harness must be loopback-only.");
    const response = await request.post(harnessURL);
    expect(response.ok()).toBe(true);
    await expect(page.getByText("connected", { exact: true })).toBeVisible();
  }

  const screenshotPath = testInfo.outputPath("real-accounts-redaction.png");
  await page.screenshot({ path: screenshotPath, fullPage: true });
  const storage = await page.evaluate(() => ({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
  }));
  await Promise.all(responseCaptures);
  const surfaces = [
    await page.locator("html").innerText(),
    JSON.stringify(storage),
    consoleMessages.join("\n"),
    responseBodies.join("\n"),
    (await readFile(screenshotPath)).toString("latin1"),
  ];
  const forbidden = [credentialCanary, ...forbiddenLabels];
  for (const surface of surfaces) {
    const normalized = surface.toLowerCase();
    for (const value of forbidden) expect(normalized).not.toContain(value.toLowerCase());
  }
});
