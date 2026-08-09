import { expect, test } from "@playwright/test";
// Playwright bundles pngjs for its own screenshot assertions without publishing this internal module's types.
// @ts-expect-error -- runtime export verified by the installed Playwright package.
import { PNG } from "playwright-core/lib/utilsBundle";

const optedIn = process.env.BT_DEV_REAL_BROWSER_ACCEPTANCE === "1";
const skipReason = "Real-browser acceptance requires BT_DEV_REAL_BROWSER_ACCEPTANCE=1 with exact FT_MODE=test.";
const forbiddenLabels = ["secretRef", "providerPath", "gatewayToken", "gateway-adapter-token", "authorization: bearer", "/run/secrets/", "/var/lib/secret-store"];
const credentialCanary = process.env.BT_DEV_REAL_CREDENTIAL_CANARY?.trim() ?? "";
const credentialSecretCanary = process.env.BT_DEV_REAL_CREDENTIAL_SECRET_CANARY?.trim() ?? "";

function pixelMismatchRatio(before: Buffer, after: Buffer): number {
  const baseline = PNG.sync.read(before);
  const result = PNG.sync.read(after);
  if (baseline.width !== result.width || baseline.height !== result.height) return 1;
  let mismatches = 0;
  for (let index = 0; index < baseline.data.length; index += 4) {
    if (baseline.data[index] !== result.data[index]
      || baseline.data[index + 1] !== result.data[index + 1]
      || baseline.data[index + 2] !== result.data[index + 2]
      || baseline.data[index + 3] !== result.data[index + 3]) mismatches += 1;
  }
  return mismatches / (baseline.width * baseline.height);
}

test.skip(!optedIn, skipReason);

test("real Test platform exposes only redacted account validation and recovery state", async ({ page, request }, testInfo) => {
  const username = process.env.BT_DEV_REAL_USERNAME!.trim();
  const password = process.env.BT_DEV_REAL_PASSWORD!;
  const harnessURL = process.env.BT_DEV_REAL_TEST_HARNESS_RESNAPSHOT_URL!.trim();

  const consoleMessages: string[] = [];
  const responseBodies: string[] = [];
  const realtimePayloads: string[] = [];
  const responseCaptures: Promise<void>[] = [];
  await page.addInitScript(() => {
    const payloads: string[] = [];
    Object.defineProperty(window, "__btRealtimePayloads", { configurable: false, value: payloads });
    const nativeAdd = EventSource.prototype.addEventListener as (type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) => void;
    EventSource.prototype.addEventListener = function (this: EventSource, type: string, listener: EventListenerOrEventListenerObject, options?: boolean | AddEventListenerOptions) {
      const capture = (event: Event) => {
        if (event instanceof MessageEvent && typeof event.data === "string") payloads.push(event.data);
      };
      nativeAdd.call(this, type, capture, options);
      nativeAdd.call(this, type, listener, options);
    } as typeof EventSource.prototype.addEventListener;
  });
  page.on("console", (message) => consoleMessages.push(message.text()));
  page.on("websocket", (socket) => {
    socket.on("framereceived", (event) => realtimePayloads.push(typeof event.payload === "string" ? event.payload : event.payload.toString("utf8")));
    socket.on("framesent", (event) => realtimePayloads.push(typeof event.payload === "string" ? event.payload : event.payload.toString("utf8")));
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

  const credentialInputs = page.locator(".account-form input[type=password]");
  expect(await credentialInputs.count(), "The real Test catalog must expose a credential schema.").toBeGreaterThan(0);
  const blankCredentialPixels: Buffer[] = [];
  for (let index = 0; index < await credentialInputs.count(); index += 1) {
    blankCredentialPixels.push(await credentialInputs.nth(index).screenshot());
  }
  await page.getByLabel("Account name").fill(`Browser acceptance ${Date.now()}`);
  for (let index = 0; index < await credentialInputs.count(); index += 1) {
    await credentialInputs.nth(index).fill(index === 0 ? credentialCanary : credentialSecretCanary);
  }
  await page.getByRole("button", { name: "Add account" }).click();
  for (let index = 0; index < blankCredentialPixels.length; index += 1) {
    await expect(credentialInputs.nth(index)).toHaveValue("");
    const clearedPixels = await credentialInputs.nth(index).screenshot();
    expect(pixelMismatchRatio(blankCredentialPixels[index], clearedPixels), "Credential input did not return to its visually blank state.").toBeLessThan(0.01);
  }

  const validations = page.getByRole("button", { name: /^Retry validation for / });
  if (await validations.count()) {
    await validations.first().click();
    await expect(page.getByRole("heading", { name: "Accounts & Security" })).toBeVisible();
  }

  const authoritativeFacts = page.locator(".account-table");
  const beforeFacts = await authoritativeFacts.innerText();
  const harnessResponse = await request.post(harnessURL);
  expect(harnessResponse.ok(), "The resnapshot harness request failed.").toBe(true);
  const markers = await harnessResponse.json() as { beforeMarker?: unknown; afterMarker?: unknown };
  expect(typeof markers.beforeMarker === "string" && typeof markers.afterMarker === "string" && markers.beforeMarker !== markers.afterMarker, "The resnapshot harness must report distinct authoritative before/after markers.").toBe(true);
  await expect.poll(() => authoritativeFacts.innerText(), { timeout: 20_000 }).not.toBe(beforeFacts);
  await expect(page.getByText("connected", { exact: true })).toBeVisible();

  await page.screenshot({ path: testInfo.outputPath("real-accounts-redaction.png"), fullPage: true });
  const storage = await page.evaluate(() => ({
    local: Object.fromEntries(Object.entries(localStorage)),
    session: Object.fromEntries(Object.entries(sessionStorage)),
    realtime: [...((window as typeof window & { __btRealtimePayloads?: string[] }).__btRealtimePayloads ?? [])],
  }));
  await Promise.all(responseCaptures);
  const surfaces = [
    await page.locator("html").innerText(),
    JSON.stringify(storage),
    consoleMessages.join("\n"),
    responseBodies.join("\n"),
    storage.realtime.join("\n"),
    realtimePayloads.join("\n"),
  ];
  const forbidden = [credentialCanary, credentialSecretCanary, ...forbiddenLabels];
  for (const surface of surfaces) {
    const normalized = surface.toLowerCase();
    for (const value of forbidden) expect(normalized.includes(value.toLowerCase()), "Sensitive material reached an observable browser surface.").toBe(false);
  }
});
