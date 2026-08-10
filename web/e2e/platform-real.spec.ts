import { writeFile } from "node:fs/promises";

import { expect, test, type Page } from "@playwright/test";

import {
  assertCanaryPixelIsolation,
  assertNoAuthoritativeAccountWrites,
  assertSafeRealPlatformURL,
  assertTestSessionMode,
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
const platformTarget = assertSafeRealPlatformURL(process.env.BT_DEV_REAL_PLATFORM_URL?.trim() || "http://127.0.0.1:13000");

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
  const responseCaptureErrors: string[] = [];
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
    if (contentType.includes("json")) responseCaptures.push(response.text()
      .then((body) => { responseBodies.push(body); })
      .catch(() => { responseCaptureErrors.push("response body capture failed"); }));
  });

  await page.goto("/");
  expect(new URL(page.url()).origin, "Platform navigation escaped the approved loopback origin.").toBe(platformTarget.origin);
  await expect(page.getByRole("heading", { name: "Sign in" })).toBeVisible();
  await page.getByLabel("Email").fill(username);
  await page.getByLabel("Password").fill(password);
  await page.getByRole("button", { name: "Sign in" }).click();
  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();

  await page.getByRole("button", { name: "Overview" }).click();
  await expect(page.getByRole("heading", { name: "Trading overview" })).toBeVisible();
  await page.getByRole("button", { name: "Funding" }).click();
  await expect(page.getByRole("heading", { name: "Funding Trading" })).toBeVisible();
  await page.getByRole("button", { name: "Manual" }).click();
  await expect(page.getByRole("heading", { name: "Manual Trading" })).toBeVisible();
  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  await page.getByRole("button", { name: "Activity" }).click();
  await expect(page.getByRole("heading", { name: "Activity & Audit" })).toBeVisible();
  await page.getByRole("button", { name: "Settings" }).click();
  await expect(page.getByRole("heading", { name: "Settings" })).toBeVisible();
  await page.getByRole("button", { name: "Accounts" }).click();
  await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  await expect(page.getByText("connected", { exact: true })).toBeVisible();
  const sessionMode = await page.evaluate(async () => {
    const response = await fetch("/api/v1/auth/session", { credentials: "include", redirect: "error" });
    if (!response.ok) throw new Error("Authoritative session mode read failed.");
    const envelope = await response.json() as { data?: unknown };
    return envelope.data;
  });
  assertTestSessionMode(sessionMode);
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

  const accountRead = page.waitForResponse((response) => response.request().method() === "GET" && new URL(response.url()).pathname === "/api/v1/accounts");
  await page.getByRole("button", { name: "Refresh accounts" }).click();
  await accountRead;

  const authoritativeFacts = page.locator(".account-table");
  const beforeFacts = await authoritativeFacts.innerText();
  const accountCount = await page.getByRole("button", { name: /^Edit / }).count();
  const expectedSnapshotPaths = ["/api/v1/accounts", "/api/v1/catalog/venues"];
  if (accountCount > 0) expectedSnapshotPaths.push("/api/v1/accounts/");
  const postHarnessResponses = expectedSnapshotPaths.map((expectedPath) => page.waitForResponse((response) => {
    const path = new URL(response.url()).pathname;
    return response.request().method() === "GET"
      && (expectedPath.endsWith("/") ? path.startsWith(expectedPath) && path.endsWith("/runtimes") : path === expectedPath);
  }));
  const harnessResponse = await request.post(harnessURL);
  expect(harnessResponse.ok(), "The resnapshot harness request failed.").toBe(true);
  const markers = await harnessResponse.json() as {
    beforeMarker?: unknown;
    afterMarker?: unknown;
    accountFactsChanged?: unknown;
    triggerCount?: unknown;
    canonicalMutationCount?: unknown;
    activeHeadMutationCount?: unknown;
  };
  expect(markers.triggerCount, "resnapshot trigger must execute exactly once").toBe(1);
  expect(markers.canonicalMutationCount, "resnapshot harness must not mutate canonical state").toBe(0);
  expect(markers.activeHeadMutationCount, "resnapshot harness must not promote Gateway heads").toBe(0);
  expect(typeof markers.beforeMarker === "string" && typeof markers.afterMarker === "string" && markers.beforeMarker !== markers.afterMarker, "The resnapshot harness must report distinct authoritative before/after markers.").toBe(true);
  const refreshedResponses = await Promise.all(postHarnessResponses);
  const refreshedBodies = await Promise.all(refreshedResponses.map((response) => response.text()));
  expect(refreshedBodies.some((body) => body.includes(markers.afterMarker as string)), "Post-harness snapshots did not expose the authoritative after marker.").toBe(true);
  if (markers.accountFactsChanged === true) await expect.poll(() => authoritativeFacts.innerText(), { timeout: 20_000 }).not.toBe(beforeFacts);
  await expect(page.getByText("connected", { exact: true })).toBeVisible();

  const storage = await page.evaluate(() => ({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
    realtime: [...((window as typeof window & { __btRealtimePayloads?: RealtimeCapture[] }).__btRealtimePayloads ?? [])],
  }));
  expect(storage.realtime.some((capture) => capture.path === "listener"), "EventSource listener capture observed no payload.").toBe(true);
  assertNoAuthoritativeAccountWrites(browserRequests);
  await Promise.all(responseCaptures);
  expect(responseCaptureErrors, "One or more API response bodies could not be captured for redaction checks.").toEqual([]);
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
