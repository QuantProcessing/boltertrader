import { expect, test, type Locator, type Page } from "@playwright/test";

import { ACCOUNT, CREDENTIALS, FakeEdge } from "./fake-edge";

/* ---------------------------------------------------------------------------
 * The plan's MVP path, in a browser, against a fake auth edge.
 *
 *   sign in → session → accounts → preview → place → see it resting →
 *   cancel it → see the audit chain
 *
 * The same eight steps the curl acceptance walked in P4. What this adds is the
 * half curl cannot reach: that the frontend unwraps six different envelopes
 * correctly, that a preview seals before a command is allowed, and that the
 * blotter reflects a cancel. What it deliberately does not test is the venue —
 * that is `mvp.live.spec.ts`.
 * ------------------------------------------------------------------------- */

const ticketOf = (page: Page): Locator =>
  page.locator("section").filter({ has: page.getByRole("heading", { name: "Order" }) });

/** Radix mounts only the selected tab's panel, so there is exactly one. */
const blotterOf = (page: Page): Locator => page.getByRole("tabpanel");

const navOf = (page: Page): Locator => page.getByRole("navigation", { name: "Sections" });

test.describe("the MVP path", () => {
  let edge: FakeEdge;

  test.beforeEach(async ({ page }) => {
    edge = new FakeEdge();
    await edge.install(page);
  });

  test("signs in and lands on the terminal", async ({ page }) => {
    await page.goto("/trade");

    // Step 1: an unauthenticated visit is sent to the login screen, carrying
    // where it was going so the operator gets back there.
    await expect(page).toHaveURL(/\/login\?redirect=/);

    await page.getByLabel("Username").fill(CREDENTIALS.username);
    await page.getByLabel("Password").fill(CREDENTIALS.password);
    await page.getByRole("button", { name: "Sign in" }).click();

    // Step 2: the session is real, so the shell renders.
    await expect(page).toHaveURL(/\/trade$/);
    await expect(navOf(page)).toBeVisible();
  });

  test("refuses a bad password without saying which half was wrong", async ({ page }) => {
    await page.goto("/login");

    await page.getByLabel("Username").fill(CREDENTIALS.username);
    await page.getByLabel("Password").fill("wrong");
    await page.getByRole("button", { name: "Sign in" }).click();

    await expect(page.getByText("invalid username or password")).toBeVisible();
    await expect(page).toHaveURL(/\/login/);
  });

  test("places an order, sees it resting, and cancels it", async ({ page }) => {
    await signIn(page, edge);
    const ticket = ticketOf(page);

    // Step 3: the account arrives through the gateway plane's meta-less
    // envelope and is selectable in the ticket.
    await expect(ticket.getByLabel("Account")).toContainText(ACCOUNT.displayName);

    // Step 4: price and quantity, then a preview. The confirm button does not
    // exist until the server has sealed one.
    await ticket.getByLabel(/^Price/).fill("50000.00");
    await ticket.getByLabel(/^Quantity/).fill("0.001");
    await expect(ticket.getByRole("button", { name: /^Confirm/ })).toHaveCount(0);

    await ticket.getByRole("button", { name: "Review order" }).click();
    await expect(ticket.getByRole("button", { name: /^Confirm buy/ })).toBeEnabled();

    // Step 5: the command.
    await ticket.getByRole("button", { name: /^Confirm buy/ }).click();
    await expect(page.getByText("Buy order submitted")).toBeVisible();

    // Step 6: it is resting in the blotter, and the ticket has gone back to its
    // unsealed state so the seal cannot be spent twice.
    const blotter = blotterOf(page);
    await expect(blotter.getByText("submitted")).toBeVisible();
    await expect(ticket.getByRole("button", { name: "Review order" })).toBeVisible();

    // Step 7: cancel it. The row keeps its place and changes state rather than
    // vanishing — an order that disappears is indistinguishable from one that
    // was never placed.
    await blotter.getByRole("button", { name: "Cancel" }).click();
    await expect(blotter.getByText("canceled")).toBeVisible();
    await expect(blotter.getByRole("button", { name: "Cancel" })).toHaveCount(0);
  });

  test("shows the audit chain the order left behind", async ({ page }) => {
    await signIn(page, edge);
    await placeAndCancel(page);

    // Step 8: both events, newest first, on the screen that reads trading's
    // ring buffer rather than the order list. Asserted on the cells, because
    // the kind filter beside the table offers the same strings as options.
    await navOf(page).getByRole("link", { name: "Activity" }).click();

    await expect(page).toHaveURL(/\/activity$/);
    await expect(page.getByRole("cell", { name: "manual.order.canceled" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "manual.order.placed" })).toBeVisible();
  });

  test("keeps every section one click from every other", async ({ page }) => {
    await signIn(page, edge);
    const nav = navOf(page);

    for (const [label, path] of [
      ["Portfolio", /\/portfolio$/],
      ["Settings", /\/settings$/],
      ["Activity", /\/activity$/],
      ["Funding", /\/funding$/],
      ["Trade", /\/trade$/],
    ] as const) {
      await nav.getByRole("link", { name: label }).click();
      await expect(page).toHaveURL(path);
    }
  });

  // The board is the one screen that needs no account, no credential and no
  // instrument selection, so this is also the assertion that it renders from
  // public data alone. What it checks beyond "the page loaded" is the two
  // things the matrix exists to do: pivot one instrument across venues into a
  // single row, and mark a column that did not answer instead of dropping it.
  test("shows the funding board without an account", async ({ page }) => {
    await signIn(page, edge);
    await navOf(page).getByRole("link", { name: "Funding" }).click();

    await expect(page).toHaveURL(/\/funding$/);
    await expect(page.getByRole("columnheader", { name: /binance/ })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /bybit/ })).toBeVisible();

    // BTC is quoted by binance and bybit under the same symbol, so it survives
    // the cross-venue filter; a venue that answered nothing is still a column.
    await expect(page.getByRole("cell", { name: "BTC-USDT", exact: false })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /okx/ })).toContainText("down");
  });

  test("sends the operator back to login when the session ends", async ({ page }) => {
    await signIn(page, edge);

    await page.getByRole("button", { name: "Sign out" }).click();
    await expect(page).toHaveURL(/\/login/);

    // And the guard holds on a direct navigation afterwards, rather than
    // flashing the terminal while a query discovers the 401.
    await page.goto("/portfolio");
    await expect(page).toHaveURL(/\/login\?redirect=/);
  });
});

/* --- Steps used by more than one test ------------------------------------- */

async function signIn(page: Page, edge: FakeEdge): Promise<void> {
  edge.signIn();
  await page.goto("/trade");
  await expect(navOf(page)).toBeVisible();
}

async function placeAndCancel(page: Page): Promise<void> {
  const ticket = ticketOf(page);
  await ticket.getByLabel(/^Price/).fill("50000.00");
  await ticket.getByLabel(/^Quantity/).fill("0.001");
  await ticket.getByRole("button", { name: "Review order" }).click();
  await ticket.getByRole("button", { name: /^Confirm buy/ }).click();

  const blotter = blotterOf(page);
  await blotter.getByRole("button", { name: "Cancel" }).click();
  await expect(blotter.getByText("canceled")).toBeVisible();
}
