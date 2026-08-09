import { writeFile } from "node:fs/promises";

import { expect, test, type Page } from "@playwright/test";

import {
  assertCanaryPixelIsolation,
  assertNoAuthoritativeAccountWrites,
  installEventSourceCapture,
  type BrowserRequestFact,
  type PixelRect,
  type RealtimeCapture,
} from "./real-acceptance-helpers";

const optedIn = process.env.BT_DEV_REAL_BROWSER_ACCEPTANCE === "1";
const skipReason = "Real-browser acceptance requires BT_DEV_REAL_BROWSER_ACCEPTANCE=1 with exact FT_MODE=test.";
const forbiddenLabels = ["secretRef", "providerPath", "gatewayToken", "gateway-adapter-token", "authorization: bearer", "/run/secrets/", "/var/lib/secret-store"];
const credentialCanary = process.env.BT_DEV_REAL_CREDENTIAL_CANARY?.trim() ?? "";
const credentialSecretCanary = process.env.BT_DEV_REAL_CREDENTIAL_SECRET_CANARY?.trim() ?? "";

async function captureStableFullPage(page: Page, options: { fullPage: true; path?: string }): Promise<Buffer> {
  for (let attempt = 0; attempt < 5; attempt += 1) {
    const first = await page.screenshot({ fullPage: options.fullPage });
    await page.waitForTimeout(80);
    const second = await page.screenshot({ fullPage: options.fullPage });
    if (!first.equals(second)) continue;
    if (options.path) await writeFile(options.path, second);
    return second;
  }
  throw new Error("Full-page rendering did not become deterministic.");
}

async function credentialPixelRects(page: Page): Promise<PixelRect[]> {
  const scale = await page.evaluate(() => devicePixelRatio);
  const scrollY = await page.evaluate(() => window.scrollY);
  const inputs = page.locator(".account-form input[type=password]");
  const rects: PixelRect[] = [];
  for (let index = 0; index < await inputs.count(); index += 1) {
    const box = await inputs.nth(index).boundingBox();
    if (!box) throw new Error("Credential input is not visible for pixel validation.");
    const padding = 4;
    rects.push({
      x: Math.floor((box.x - padding) * scale),
      y: Math.floor((box.y + scrollY - padding) * scale),
      width: Math.ceil((box.width + padding * 2) * scale),
      height: Math.ceil((box.height + padding * 2) * scale),
    });
  }
  return rects;
}

test.skip(!optedIn, skipReason);

test("real Test platform exposes only redacted account validation and recovery state", async ({ page, request }, testInfo) => {
  const username = process.env.BT_DEV_REAL_USERNAME!.trim();
  const password = process.env.BT_DEV_REAL_PASSWORD!;
  const harnessURL = process.env.BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL!.trim();

  const browserRequests: BrowserRequestFact[] = [];
  const consoleMessages: string[] = [];
  const responseBodies: string[] = [];
  const websocketPayloads: string[] = [];
  const responseCaptures: Promise<void>[] = [];
  await page.addInitScript(installEventSourceCapture);
  page.on("request", (browserRequest) => {
    const path = new URL(browserRequest.url()).pathname;
    if (path.startsWith("/api/v1/")) browserRequests.push({ method: browserRequest.method(), path });
  });
  page.on("console", (message) => consoleMessages.push(message.text()));
  page.on("websocket", (socket) => {
    socket.on("framereceived", (event) => websocketPayloads.push(typeof event.payload === "string" ? event.payload : event.payload.toString("utf8")));
    socket.on("framesent", (event) => websocketPayloads.push(typeof event.payload === "string" ? event.payload : event.payload.toString("utf8")));
  });
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
  await expect(page.getByText("connected", { exact: true })).toBeVisible();
  await page.addStyleTag({ content: "*,*::before,*::after{animation:none!important;transition:none!important;caret-color:transparent!important}" });
  await page.evaluate(async () => { await document.fonts.ready; window.scrollTo(0, 0); (document.activeElement as HTMLElement | null)?.blur(); });

  const credentialInputs = page.locator(".account-form input[type=password]");
  expect(await credentialInputs.count(), "The real Test catalog must expose controlled credential fields.").toBeGreaterThan(0);
  const allowedRects = await credentialPixelRects(page);
  const beforePixels = await captureStableFullPage(page, { fullPage: true });
  for (let index = 0; index < await credentialInputs.count(); index += 1) {
    await credentialInputs.nth(index).fill(index === 0 ? credentialCanary : credentialSecretCanary);
  }
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  const withCanaryPixels = await captureStableFullPage(page, { fullPage: true });
  for (let index = 0; index < await credentialInputs.count(); index += 1) await credentialInputs.nth(index).fill("");
  await page.evaluate(() => (document.activeElement as HTMLElement | null)?.blur());
  const afterPixels = await captureStableFullPage(page, { fullPage: true, path: testInfo.outputPath("real-accounts-redaction.png") });
  assertCanaryPixelIsolation(beforePixels, withCanaryPixels, afterPixels, allowedRects);

  const validations = page.getByRole("button", { name: /^Retry validation for / });
  if (await validations.count()) {
    const validationResponse = page.waitForResponse((response) => response.request().method() === "POST" && /^\/api\/v1\/accounts\/[^/]+\/validate$/.test(new URL(response.url()).pathname));
    await validations.first().click();
    await validationResponse;
  } else {
    const accountRead = page.waitForResponse((response) => response.request().method() === "GET" && new URL(response.url()).pathname === "/api/v1/accounts");
    await page.getByRole("button", { name: "Refresh accounts" }).click();
    await accountRead;
    await expect(page.getByText("No accounts configured")).toBeVisible();
  }

  const authoritativeFacts = page.locator(".account-table");
  const beforeFacts = await authoritativeFacts.innerText();
  const harnessResponse = await request.post(harnessURL);
  expect(harnessResponse.ok(), "The resnapshot harness request failed.").toBe(true);
  const markers = await harnessResponse.json() as { beforeMarker?: unknown; afterMarker?: unknown };
  expect(typeof markers.beforeMarker === "string" && typeof markers.afterMarker === "string" && markers.beforeMarker !== markers.afterMarker, "The resnapshot harness must report distinct authoritative before/after markers.").toBe(true);
  await expect.poll(() => authoritativeFacts.innerText(), { timeout: 20_000 }).not.toBe(beforeFacts);
  await expect(page.getByText("connected", { exact: true })).toBeVisible();

  const storage = await page.evaluate(() => ({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
    realtime: [...((window as typeof window & { __btRealtimePayloads?: RealtimeCapture[] }).__btRealtimePayloads ?? [])],
  }));
  expect(storage.realtime.some((capture) => capture.path === "listener"), "EventSource listener capture observed no payload.").toBe(true);
  expect(storage.realtime.some((capture) => capture.path === "onmessage"), "EventSource onmessage capture observed no payload.").toBe(true);
  assertNoAuthoritativeAccountWrites(browserRequests);
  await Promise.all(responseCaptures);
  const surfaces = [
    await page.locator("html").innerText(),
    JSON.stringify(storage),
    consoleMessages.join("\n"),
    responseBodies.join("\n"),
    storage.realtime.map((capture) => capture.data).join("\n"),
    websocketPayloads.join("\n"),
  ];
  const forbidden = [credentialCanary, credentialSecretCanary, ...forbiddenLabels];
  for (const surface of surfaces) {
    const normalized = surface.toLowerCase();
    for (const value of forbidden) expect(normalized.includes(value.toLowerCase()), "Sensitive material reached an observable browser surface.").toBe(false);
  }
});
