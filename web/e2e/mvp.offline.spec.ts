import { expect, test, type Locator, type Page } from "@playwright/test";

import { ACCOUNT, CREDENTIALS, HEDGE_ACCOUNT } from "../src/mocks/fixtures";

/* ---------------------------------------------------------------------------
 * The plan's MVP path, in a browser, against the MSW mock edge.
 *
 *   sign in → session → accounts → preview → place → see it resting →
 *   cancel it → see the audit chain
 *
 * The same eight steps the curl acceptance walked in P4. What this adds is the
 * half curl cannot reach: that the frontend unwraps seven different envelopes
 * correctly, that a preview seals before a command is allowed, and that the
 * blotter reflects a cancel. What it deliberately does not test is the venue —
 * that is `mvp.live.spec.ts`.
 *
 * There is no request interception here. `npm run dev:mock` starts the same
 * service worker a developer runs, so this file drives the app exactly as they
 * do and cannot pass against a mock the dev server does not have. The mock's
 * store lives in the page, so every test begins on a clean one, and the session
 * is a real cookie set by a real sign-in rather than a flag flipped from the
 * test — which is what makes the sign-out assertion at the bottom mean anything.
 * ------------------------------------------------------------------------- */

const ticketOf = (page: Page): Locator =>
  page.locator("section").filter({ has: page.getByRole("heading", { name: "Order" }) });

/** Radix mounts only the selected tab's panel, so there is exactly one. */
const blotterOf = (page: Page): Locator => page.getByRole("tabpanel");

const navOf = (page: Page): Locator => page.getByRole("navigation", { name: "Sections" });

test.describe("the MVP path", () => {
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

  // The one step of the MVP path the order tests take for granted, because the
  // fixtures already contain two accounts. What it proves is the part of the
  // account plane that no fixture can: that the form is built from the venue's
  // own credential schema rather than from a shape hardcoded here — okx is in
  // the list precisely because it is the venue with a third field — and that
  // the secrets leave the page the moment they are sent. PRODUCT.md's
  // write-only rule is enforced on the server, but a form that kept the key on
  // screen afterwards would break it on the client alone.
  test("creates an account from the venue's own credential schema", async ({ page }) => {
    await signIn(page);
    await navOf(page).getByRole("link", { name: "Settings" }).click();
    await expect(page).toHaveURL(/\/settings$/);

    const accountList = page.getByRole("navigation", { name: "Accounts" });
    await expect(accountList.getByRole("button")).toHaveCount(2);

    await page.getByRole("button", { name: "Add account" }).click();
    const form = page.locator("form");

    // Choosing the venue is what makes the rest of the form exist: routes and
    // credential fields are the venue's, and neither is rendered before one.
    await expect(form.getByRole("checkbox")).toHaveCount(0);
    await form.getByLabel("Venue").selectOption("okx");

    await form.getByLabel("Display name").fill("OKX Main");
    await form.getByRole("checkbox").first().check();

    // Three fields, because okx's key is useless without the passphrase it was
    // minted with. A form built from a fixed api-key/secret pair would have two.
    await expect(form.getByLabel(/^API key/)).toBeVisible();
    await form.getByLabel(/^API key/).fill("okx-key");
    await form.getByLabel(/^API secret/).fill("okx-secret");
    await form.getByLabel(/^Passphrase/).fill("okx-passphrase");

    await form.getByRole("button", { name: "Create account" }).click();

    // 201, and the shell selects what it just made: the form is gone and the
    // detail panel is showing the new account rather than the old selection.
    await expect(page.getByText("Account created")).toBeVisible();
    await expect(accountList.getByRole("button")).toHaveCount(3);
    await expect(accountList.getByRole("button", { name: /OKX Main/ })).toBeVisible();
    await expect(page.getByRole("heading", { name: "OKX Main" })).toBeVisible();

    // And nothing that was typed survived the submit. The form unmounted, so
    // this is checked on the page: a password input anywhere holding one of
    // those values would be the write-only rule broken before the request.
    expect(await page.locator("input[type='password']").count()).toBe(0);
    await expect(page.getByText("okx-secret")).toHaveCount(0);
  });

  test("places an order, sees it resting, and cancels it", async ({ page }) => {
    await signIn(page);
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
    await signIn(page);
    await placeAndCancel(page);

    // Step 8: both events, newest first, on the screen that reads trading's
    // ring buffer rather than the order list. Asserted on the cells, because
    // the kind filter beside the table offers the same strings as options.
    //
    // The kinds are the server's, not friendlier inventions:
    // `publishCommandActivity` names an event `"command." + command.state`, so a
    // cancel is `command.cancel_accepted` and there is no "order" in either.
    await navOf(page).getByRole("link", { name: "Activity" }).click();

    await expect(page).toHaveURL(/\/activity$/);
    await expect(page.getByRole("cell", { name: "command.cancel_accepted" })).toBeVisible();
    await expect(page.getByRole("cell", { name: "command.submitted" })).toBeVisible();
  });

  test("keeps every section one click from every other", async ({ page }) => {
    await signIn(page);
    const nav = navOf(page);

    for (const [label, path] of [
      ["Portfolio", /\/portfolio$/],
      ["Settings", /\/settings$/],
      ["Activity", /\/activity$/],
      ["Funding", /\/funding$/],
      ["Arbitrage", /\/funding\/arbitrage$/],
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
    await signIn(page);
    await navOf(page).getByRole("link", { name: "Funding" }).click();

    await expect(page).toHaveURL(/\/funding$/);
    await expect(page.getByRole("columnheader", { name: /binance/ })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /bybit/ })).toBeVisible();

    // BTC is quoted by binance and bybit under the same symbol, so it survives
    // the cross-venue filter; a venue that answered nothing is still a column.
    await expect(page.getByRole("cell", { name: "BTC-USDT", exact: false })).toBeVisible();
    await expect(page.getByRole("columnheader", { name: /okx/ })).toContainText("down");
  });

  // A hedge is the one thing on the terminal that is two orders on two venues,
  // so what this checks is that both halves survive the trip: the ranked scan
  // arrives through an envelope nothing else uses (`{items, observedAt}`), and
  // the ticket resolves each leg to an account on the leg's own exchange.
  test("ranks a cross-venue spread and offers both legs a home", async ({ page }) => {
    await signIn(page);
    await navOf(page).getByRole("link", { name: "Arbitrage" }).click();

    await expect(page).toHaveURL(/\/funding\/arbitrage$/);

    // The scan ranked one pair, and it is the pair the board can make.
    const spread = page.getByRole("button", { name: /BTC-USDT/ }).first();
    await expect(spread).toContainText("binance → bybit");

    // Long above short, each picking from accounts on its own exchange: the
    // pickers are filtered by venue, so a wrong filter puts the wrong name here.
    const ticket = page.locator("section").filter({ has: page.getByRole("heading", { name: "Hedge" }) });
    const pickers = ticket.getByLabel("Account");
    await expect(pickers.nth(0)).toContainText(ACCOUNT.displayName);
    await expect(pickers.nth(1)).toContainText(HEDGE_ACCOUNT.displayName);
  });

  // The picker is the control every session starts with, and the one defect
  // reported against it: typing `btc` returned one flat list, so reaching the
  // okx listing meant scrolling past everything binance quotes. design.md §10
  // makes "which venue" the shape of the answer rather than something you find
  // by scrolling, so what this asserts is that every venue quoting BTC is on
  // screen at once — and that a halted market is among them rather than hidden.
  test("answers 'which venue' without a scroll", async ({ page }) => {
    await signIn(page);

    await page.getByRole("button", { name: /Select instrument|BTC-USDT/ }).first().click();
    const list = page.getByRole("listbox");
    await expect(list).toBeVisible();

    await page.getByRole("combobox", { name: "Instruments" }).fill("btc");

    // Every venue that quotes BTC, each under its own heading.
    for (const venue of ["binance", "bybit", "okx"] as const) {
      await expect(list.getByRole("group", { name: venue })).toBeVisible();
    }
    // lighter quotes only ETH, so it is absent rather than empty.
    await expect(list.getByRole("group", { name: "lighter" })).toHaveCount(0);

    // The halted okx spot listing is shown and marked, not dropped.
    const okx = list.getByRole("group", { name: "okx" });
    await expect(okx.getByRole("option", { name: /halted/ })).toBeVisible();

    // And picking the buried one actually switches the terminal to it.
    await okx.getByRole("option", { name: /BTC-USDT/ }).first().click();
    await expect(page.getByRole("button", { name: /BTC-USDT okx/ })).toBeVisible();
  });

  test("sends the operator back to login when the session ends", async ({ page }) => {
    await signIn(page);

    await page.getByRole("button", { name: "Sign out" }).click();
    // One capture of where they were, not a stack of them. This shell outlives
    // the navigation it starts — the login route's `beforeLoad` is async — so an
    // effect keyed on `location.href` re-runs against its own `/login?redirect=…`
    // output and wraps it once per render. That reads as a working sign-out
    // right up until the address is kilobytes long and has to bounce a few dozen
    // times to unwrap, which a `/\/login/` assertion cannot tell apart from this.
    await expect(page).toHaveURL("/login?redirect=%2Ftrade");

    // And the guard holds on a direct navigation afterwards, rather than
    // flashing the terminal while a query discovers the 401.
    await page.goto("/portfolio");
    await expect(page).toHaveURL("/login?redirect=%2Fportfolio");
  });
});

/* --- Steps used by more than one test ------------------------------------- */

/**
 * Signs in the way an operator does: bounced off a guarded route, through the
 * form, and back to where they were going.
 *
 * There is no shortcut past this. The mock mints the session cookie and the
 * CSRF token in its login handler, and every write in these tests carries that
 * token — so a sign-in that cheated would leave the writes failing 403 for a
 * reason no screen would explain.
 */
async function signIn(page: Page): Promise<void> {
  await page.goto("/trade");
  await expect(page).toHaveURL(/\/login\?redirect=/);
  await page.getByLabel("Username").fill(CREDENTIALS.username);
  await page.getByLabel("Password").fill(CREDENTIALS.password);
  await page.getByRole("button", { name: "Sign in" }).click();
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
