import { expect, test } from "@playwright/test";

import { installFixturePlatform, installFixtureRealtime } from "./fixture-platform";

test.beforeEach(async ({ page }) => {
  await installFixtureRealtime(page);
});

test("authenticated desktop operator can inspect and act across both products", async ({ page }, testInfo) => {
  const platform = await installFixturePlatform(page, { authenticated: false });
  await page.goto("/");

  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Email").fill("operator@example.test");
  await page.getByLabel("Password").fill("correct-horse-battery-staple");
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();
  await expect(page.getByText("1.25 USDT").first()).toBeVisible();
  await page.screenshot({ path: testInfo.outputPath("desktop-overview.png"), fullPage: true });

  await page.getByRole("button", { name: "Manual" }).click();
  await expect(page.getByRole("heading", { name: "Manual Trading" })).toBeVisible();
  await page.getByLabel("Quantity").fill("0.001");
  await page.getByRole("button", { name: "Preview order" }).click();
  await expect(page.getByRole("button", { name: "Submit order" })).toBeEnabled();
  await page.getByRole("button", { name: "Submit order" }).click();
  await expect(page.getByText("Manual order accepted.")).toBeVisible();

  await page.getByRole("button", { name: "Funding" }).click();
  await expect(page.getByRole("heading", { name: "Funding Trading" })).toBeVisible();
  await page.getByRole("button", { name: "Preview plan" }).click();
  await expect(page.getByRole("button", { name: "Submit open" })).toBeEnabled();
  await page.getByRole("button", { name: "Submit open" }).click();
  await expect(page.getByText("Funding command accepted.")).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page.getByText("Funding close accepted.")).toBeVisible();

  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  await page.getByLabel("Account name").fill("OKX Test");
  await page.getByLabel("API key").fill("credential-canary");
  await page.getByLabel("API secret").fill("must-disappear");
  await page.getByLabel("Spot / USDT route").check();
  await page.getByRole("button", { name: "Add account" }).click();
  await expect(page.getByLabel("API key")).toHaveValue("");
  await expect(page.getByLabel("API secret")).toHaveValue("");
  await expect(page.getByText("credential-canary")).toHaveCount(0);
  await page.getByLabel("Account name").fill("Binance Secondary");
  await page.getByLabel("API key").fill("second-browser-key");
  await page.getByLabel("API secret").fill("second-browser-secret");
  await page.getByRole("button", { name: "Add account" }).click();
  await expect(page.getByLabel("API secret")).toHaveValue("");
  await expect(page.getByRole("cell", { name: "private stream gap", exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("desktop-accounts.png"), fullPage: true });

  await page.getByRole("button", { name: "Activity" }).click();
  await expect(page.getByRole("heading", { name: "Activity & Audit" })).toBeVisible();
  await expect(page.getByText("Funding command finalized")).toBeVisible();
  await page.getByRole("tab", { name: "Audit" }).click();
  await expect(page.getByText("command.submit")).toBeVisible();
  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await expect(page.getByText("boltertrader-product-platform-upgrade/v5")).toBeVisible();

  expect(platform.readPaths()).not.toContain("/catalog/capabilities");

  const manualCommand = platform.writes.find((write) => write.path === "/products/manual/commands");
  const fundingCommands = platform.writes.filter((write) => write.path === "/products/funding/commands");
  const reservations = platform.writes.filter((write) => write.path === "/reservations");
  const accountCreates = platform.writes.filter((write) => write.path === "/accounts" && write.method === "POST");
  expect(manualCommand?.headers["x-csrf-token"]).toBe("csrf-fixture");
  expect(manualCommand?.headers["idempotency-key"]).toBeTruthy();
  expect(fundingCommands).toHaveLength(2);
  expect(reservations).toHaveLength(4);
  const previewWrite = platform.writes.find((write) => write.path === "/products/funding/execution-previews");
  expect(previewWrite?.body).toMatchObject({ longAccountId: "binance-primary", shortAccountId: "bybit-hedge" });
  const fundingOpenIndex = platform.writes.findIndex((write) => write.path === "/products/funding/commands" && (write.body as { action?: string })?.action === "open");
  const fundingPreviewIndex = platform.writes.findIndex((write) => write.path === "/products/funding/execution-previews");
  const openReservationIndexes = platform.writes
    .map((write, index) => ({ write, index }))
    .filter(({ write }) => write.path === "/reservations" && (write.body as { reservationKind?: string })?.reservationKind === "open")
    .map(({ index }) => index);
  expect(openReservationIndexes).toHaveLength(2);
  expect(fundingPreviewIndex).toBeLessThan(Math.min(...openReservationIndexes));
  expect(Math.max(...openReservationIndexes)).toBeLessThan(fundingOpenIndex);
  expect(accountCreates).toHaveLength(2);
  expect(accountCreates[0]?.body).toMatchObject({ venue: "binance", productIds: ["manual"], routes: [{ routeId: "spot-usdt", requestedState: "active" }, { routeId: "perp-usdt", requestedState: "active" }], credentialBundle: { schemaVersion: 1, venue: "binance", authKind: "api_key_secret", secrets: { apiKey: "credential-canary", apiSecret: "must-disappear" } } });
  expect(accountCreates[1]?.body).toMatchObject({ venue: "binance", credentialBundle: { secrets: { apiKey: "second-browser-key", apiSecret: "second-browser-secret" } } });
});

test("degraded Live workspace shows authority failures without submitting an order", async ({ page }) => {
  const platform = await installFixturePlatform(page, { authenticated: true, degraded: true, liveDenied: true, mode: "live" });
  const sessionResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/auth/session");
  await page.goto("/");
  expect((await sessionResponse).status()).toBe(200);

  await expect(page.getByText("Live environment")).toBeVisible();
  await expect(page.getByText("Platform is degraded")).toBeVisible();
  await page.getByRole("button", { name: "Manual" }).click();
  await page.getByLabel("Quantity").fill("0.001");
  await page.getByRole("button", { name: "Preview order" }).click();
  await expect(page.getByRole("alert")).toContainText("LIVE PRIVATE WRITE DENIED");
  expect(platform.writes.filter((write) => write.path === "/products/manual/commands")).toHaveLength(0);
});

test("Manual resting order cancel and safe close preserve durable identity", async ({ page }) => {
  const platform = await installFixturePlatform(page, { authenticated: true });
  await page.goto("/");
  await page.getByRole("button", { name: "Manual" }).click();
  await expect(page.getByRole("heading", { name: "Manual Trading" })).toBeVisible();

  await page.getByRole("button", { name: "Cancel" }).click();
  await expect(page.getByText("Manual order canceled.")).toBeVisible();
  await page.getByRole("button", { name: "Close", exact: true }).click();
  await expect(page.getByText("Manual close accepted.")).toBeVisible();

  const commands = platform.writes.filter((write) => write.path === "/products/manual/commands");
  expect(commands).toHaveLength(2);
  expect(commands[0].body).toMatchObject({ action: "cancel", lineageId: "manual-lineage", orderId: "manual-command" });
  expect(commands[1].body).toMatchObject({ action: "close", lineageId: "manual-lineage", orderId: "" });
  for (const command of commands) {
    expect(command.headers["x-csrf-token"]).toBe("csrf-fixture");
    expect(command.headers["idempotency-key"]).toBeTruthy();
  }
});

test("realtime resnapshot signal reloads authoritative workspace before resuming", async ({ page }) => {
  const platform = await installFixturePlatform(page, { authenticated: true });
  const sessionResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/auth/session");
  await page.goto("/");
  expect((await sessionResponse).status()).toBe(200);
  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();
  const before = platform.snapshotReads();

  await page.evaluate(() => {
    const emit = (window as typeof window & { __emitRealtime?: (type: string, payload: unknown) => void }).__emitRealtime;
    emit?.("realtime.resnapshot_required", { eventId: "event-1", sequence: 1, cursor: "cursor-1", observedAt: "2026-08-08T10:00:01Z", payload: {} });
  });

  await expect.poll(platform.snapshotReads).toBeGreaterThan(before);
  await expect(page.getByText("connected", { exact: true })).toBeVisible();
});

test("account operations remain scoped, retryable, recoverable, and redacted", async ({ page }) => {
  const platform = await installFixturePlatform(page, { authenticated: true });
  await page.goto("/");
  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  await expect(page.getByText("STALE PUBLIC CONNECTOR")).toBeVisible();
  await expect(page.getByRole("cell", { name: "private stream gap", exact: true })).toBeVisible();

  await page.getByRole("button", { name: "Retry validation for Binance Primary" }).click();
  await expect(page.getByRole("alert", { name: "Binance Primary operation error" })).toContainText("PRIVATE STREAM GAP");
  await expect(page.getByRole("alert", { name: "Bybit Hedge operation error" })).toHaveCount(0);
  await expect(page.locator("body")).not.toContainText("credential-canary");
  await expect(page.locator("body")).not.toContainText("/var/lib/secret-store");
  await page.getByRole("button", { name: "Retry validation for Binance Primary" }).click();
  await expect(page.getByRole("alert", { name: "Binance Primary operation error" })).toHaveCount(0);

  await page.getByRole("button", { name: "Drain Binance Primary" }).click();
  await expect(page.getByRole("alert", { name: "Binance Primary operation error" })).toContainText("DRAIN BLOCKED");
  await expect(page.getByRole("alert", { name: "Bybit Hedge operation error" })).toHaveCount(0);
  await page.getByRole("button", { name: "Disable Binance Primary" }).click();
  await expect(page.getByRole("alert", { name: "Binance Primary operation error" })).toContainText("DISABLE BLOCKED");
  await expect(page.getByRole("alert", { name: "Bybit Hedge operation error" })).toHaveCount(0);
  await page.getByRole("button", { name: "Remove Binance Primary" }).click();
  await page.getByRole("button", { name: "Confirm removal" }).click();
  await expect(page.getByRole("group", { name: "Remove Binance Primary" })).toBeVisible();
  await expect(page.getByRole("group", { name: "Remove Binance Primary" }).getByRole("alert")).toContainText("ACCOUNT BLOCKED");
  await expect(page.locator("body")).not.toContainText("opaque-canary");
  await expect(page.locator("body")).not.toContainText("token-canary");

  platform.recoverPrivateState();
  await page.getByRole("button", { name: "Refresh accounts" }).click();
  await expect(page.getByText("private stream gap")).toHaveCount(0);
  await expect(page.getByText("STALE PUBLIC CONNECTOR")).toHaveCount(0);
});

test("account creation visibly denies a catalog venue that conflicts with the session mode", async ({ page }) => {
  const platform = await installFixturePlatform(page, { authenticated: true, modeConflictCatalog: true });
  await page.goto("/");
  await page.getByRole("button", { name: "Accounts" }).click();

  await expect(page.getByRole("option", { name: "coinbase" })).toHaveAttribute("disabled", "");
  await expect(page.getByRole("alert")).toContainText("Mode conflict: live venue is unavailable in Test mode.");
  await expect(page.getByRole("button", { name: "Add account" })).toBeDisabled();
  expect(platform.writes.filter((write) => write.path === "/accounts" && write.method === "POST")).toHaveLength(0);
});

test("mobile navigation preserves every route without document overflow", async ({ page }, testInfo) => {
  await page.setViewportSize({ width: 390, height: 844 });
  await installFixturePlatform(page, { authenticated: true });
  const sessionResponse = page.waitForResponse((response) => new URL(response.url()).pathname === "/api/v1/auth/session");
  await page.goto("/");
  expect((await sessionResponse).status()).toBe(200);

  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();
  const navigation = page.getByLabel("Primary navigation");
  await expect(navigation).toHaveCSS("box-shadow", "none");
  await expect.poll(async () => {
    const bounds = await navigation.boundingBox();
    return bounds ? bounds.x + bounds.width : Number.POSITIVE_INFINITY;
  }).toBeLessThanOrEqual(0);
  await page.getByRole("button", { name: "Open navigation" }).click();
  await expect(navigation).not.toHaveCSS("box-shadow", "none");
  await page.getByRole("button", { name: "Funding" }).click();
  await expect(page.getByRole("heading", { name: "Funding Trading" })).toBeVisible();
  await expect(navigation).toHaveCSS("box-shadow", "none");
  await expect.poll(async () => {
    const bounds = await navigation.boundingBox();
    return bounds ? bounds.x + bounds.width : Number.POSITIVE_INFINITY;
  }).toBeLessThanOrEqual(0);
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await page.screenshot({ path: testInfo.outputPath("mobile-funding.png"), fullPage: true });
  await page.getByRole("button", { name: "Open navigation" }).click();
  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  await expect(page.getByRole("cell", { name: "private stream gap", exact: true })).toBeVisible();
  await expect.poll(() => page.evaluate(() => document.documentElement.scrollWidth <= window.innerWidth)).toBe(true);
  await expect.poll(() => page.evaluate(() => window.scrollX)).toBe(0);
  await page.screenshot({ path: testInfo.outputPath("mobile-accounts.png"), fullPage: true });
});
