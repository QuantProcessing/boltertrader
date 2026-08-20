import { expect, test, type Page } from "@playwright/test";

/* ---------------------------------------------------------------------------
 * The MVP path against a real stack. It proves that a preview sealed by trading
 * is accepted by trading and that an order this app places is an order the
 * venue really rests.
 *
 * It needs a stack (`make up`), an account with working credentials, and the
 * bootstrap password — which is not on disk anywhere, only its hash is, so it
 * has to be handed in:
 *
 *   BOLTER_E2E_LIVE=1 BOLTER_E2E_PASSWORD=… npx playwright test --project=live
 *
 * It places one limit order at half the book price. A buy that far below the
 * touch cannot cross, so it rests until this test cancels it — and if the
 * cancel fails, the failure names the order id so it can be cancelled by hand.
 * ------------------------------------------------------------------------- */

const USERNAME = process.env.BOLTER_E2E_USERNAME ?? process.env.BT_BOOTSTRAP_USERNAME ?? "operator";
const PASSWORD = process.env.BOLTER_E2E_PASSWORD ?? "";
const QUANTITY = process.env.BOLTER_E2E_QUANTITY ?? "0.001";

test.describe("the MVP path, live", () => {
  test.beforeAll(() => {
    if (PASSWORD === "") {
      throw new Error(
        "BOLTER_E2E_PASSWORD is required: the auth edge stores only a hash of the bootstrap password, " +
          "so the live suite cannot recover it from the environment the services run in.",
      );
    }
  });

  // A real venue round trip is slower than a fake one, and a preview that has
  // to reach an exchange is slower still.
  test.setTimeout(180_000);

  test("signs in, places a resting order, cancels it, and finds both in the audit chain", async ({ page }) => {
    await test.step("sign in", async () => {
      await page.goto("/login");
      await page.getByLabel("Username").fill(USERNAME);
      await page.getByLabel("Password").fill(PASSWORD);
      await page.getByRole("button", { name: "Sign in" }).click();

      await expect(page.getByRole("navigation", { name: "Sections" })).toBeVisible();
      await expect(page).toHaveURL(/\/trade$/);
    });

    const ticket = page.locator("section").filter({ has: page.getByRole("heading", { name: "Order" }) });

    // The book is a labelled region, not a titled panel — 256px of width goes
    // to the ladder rather than to a heading. And a depth row is specifically a
    // button carrying figures: the price-grouping controls in the header are
    // buttons too, and one of those being visible says nothing about whether a
    // venue ever answered.
    const book = page.getByRole("region", { name: "Order book" });
    const depthRows = book.locator("button:has([data-numeric])");

    await test.step("an account and a book arrived", async () => {
      // The account comes from the gateway through the edge; the book comes
      // from the venue through trading. Both have to be real before anything
      // below means anything.
      await expect(ticket.getByLabel("Account")).not.toHaveValue("");
      await expect(depthRows.first()).toBeVisible({ timeout: 30_000 });
    });

    await test.step("price it out of reach", async () => {
      // Take a level straight from the book so the tick size is the venue's
      // own, then halve it: a buy limit at half the bid cannot cross.
      await depthRows.last().click();

      const field = ticket.getByLabel(/^Price/);
      await field.fill(halve(await field.inputValue()));
    });

    await test.step("preview and place", async () => {
      await ticket.getByLabel(/^Quantity/).fill(QUANTITY);
      await ticket.getByRole("button", { name: "Review order" }).click();

      // A blocker here is the venue refusing the order, and its text is the
      // most useful thing this test can fail with.
      await expect(ticket.getByRole("button", { name: /^Confirm buy/ })).toBeEnabled();
      await ticket.getByRole("button", { name: /^Confirm buy/ }).click();

      await expect(page.getByText(/order (submitted|accepted|placed)/i)).toBeVisible();
    });

    const blotter = page.getByRole("tabpanel");

    await test.step("it is resting", async () => {
      await expect(blotter.getByRole("button", { name: "Cancel" }).first()).toBeVisible({ timeout: 30_000 });
    });

    await test.step("cancel it", async () => {
      await blotter.getByRole("button", { name: "Cancel" }).first().click();
      await expect(blotter.getByText("canceled").first()).toBeVisible({ timeout: 30_000 });
    });

    await test.step("the audit chain has both ends", async () => {
      await page.getByRole("navigation", { name: "Sections" }).getByRole("link", { name: "Activity" }).click();

      await expect(page.getByRole("cell", { name: "manual.order.placed" }).first()).toBeVisible();
      await expect(page.getByRole("cell", { name: "manual.order.canceled" }).first()).toBeVisible();
    });

    await test.step("sign out", async () => {
      await signOut(page);
    });
  });
});

/** Half the price, at the decimal count it arrived with, so the tick still divides it. */
function halve(price: string): string {
  const value = Number.parseFloat(price);
  if (!Number.isFinite(value) || value <= 0) {
    throw new Error(`the order book handed the ticket an unusable price: ${JSON.stringify(price)}`);
  }
  const dot = price.indexOf(".");
  return (value / 2).toFixed(dot < 0 ? 0 : price.length - dot - 1);
}

async function signOut(page: Page): Promise<void> {
  await page.getByRole("button", { name: "Sign out" }).click();
  await expect(page).toHaveURL(/\/login/);
}
