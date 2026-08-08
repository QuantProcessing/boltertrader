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
  await page.getByLabel("Credential JSON").fill('{"apiKey":"credential-canary","secret":"must-disappear"}');
  await page.getByRole("button", { name: "Add account" }).click();
  await expect(page.getByLabel("Credential JSON")).toHaveValue("");
  await expect(page.getByText("credential-canary")).toHaveCount(0);

  await page.getByRole("button", { name: "Activity" }).click();
  await expect(page.getByRole("heading", { name: "Activity & Audit" })).toBeVisible();
  await expect(page.getByText("Funding command finalized")).toBeVisible();
  await page.getByRole("tab", { name: "Audit" }).click();
  await expect(page.getByText("command.submit")).toBeVisible();
  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await expect(page.getByText("boltertrader-product-platform-upgrade/v4")).toBeVisible();

  expect(platform.readPaths()).not.toContain("/catalog/capabilities");

  const manualCommand = platform.writes.find((write) => write.path === "/products/manual/commands");
  const fundingCommands = platform.writes.filter((write) => write.path === "/products/funding/commands");
  const reservations = platform.writes.filter((write) => write.path === "/reservations");
  const accountCreate = platform.writes.find((write) => write.path === "/accounts" && write.method === "POST");
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
  expect(accountCreate?.body).toMatchObject({ secretMaterial: { apiKey: "credential-canary", secret: "must-disappear" } });
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
});
