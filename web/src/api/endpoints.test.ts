import { afterEach, describe, expect, it, vi } from "vitest";

import { currentCsrfToken, setCsrfToken } from "./client";
import { accountData, accounts, auth, catalog, manual, market, trading } from "./endpoints";
import type { CredentialBundle } from "./types";

/* ---------------------------------------------------------------------------
 * These are protocol tests, not plumbing tests. Each one pins a fact about what
 * the services accept that the frontend cannot discover at runtime: the exact
 * ETag string, which mutations must carry no body, which envelope a route
 * speaks. Getting any of them wrong produces a 4xx that looks like a bug in the
 * server, so they are asserted against the shape the Go handlers parse.
 * ------------------------------------------------------------------------- */

interface Call {
  url: string;
  init: RequestInit;
}

function record(body: unknown, status = 200) {
  const calls: Call[] = [];
  vi.stubGlobal(
    "fetch",
    vi.fn(async (input: unknown, init: unknown) => {
      calls.push({ url: String(input), init: (init ?? {}) as RequestInit });
      return new Response(JSON.stringify(body), {
        status,
        headers: { "Content-Type": "application/json" },
      });
    }),
  );
  return calls;
}

const headersOf = (init: RequestInit): Record<string, string> =>
  (init.headers ?? {}) as Record<string, string>;

const account = (over: Record<string, unknown> = {}) => ({
  accountId: "acct-1",
  venue: "binance",
  nativeEnvironment: "live",
  displayName: "Binance",
  productPolicy: ["manual"],
  status: "active",
  lastValidatedAt: null,
  updatedAt: "2026-08-13T00:00:00Z",
  ...over,
});

const bundle: CredentialBundle = {
  schemaVersion: 1,
  venue: "binance",
  authKind: "hmac",
  metadata: {},
  secrets: { apiKey: "k", apiSecret: "s" },
};

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken(null);
});

describe("the account lifecycle", () => {
  it("disables an account without exposing revision management", async () => {
    const calls = record({ data: { account: account() } });

    await accounts.disable("acct-1");

    expect(calls[0]!.init.body).toBeUndefined();
    expect(calls[0]!.init.method).toBe("POST");
    expect(headersOf(calls[0]!.init)["If-Match"]).toBeUndefined();
  });

  it("carries an idempotency key when credentials are created or destroyed", async () => {
    const calls = record({ data: { account: account() } });

    await accounts.create(
      { venue: "binance", displayName: "n", productPolicy: ["manual"], routes: [], credentialBundle: bundle },
      "key-create",
    );
    await accounts.remove("acct-1", "key-remove");

    expect(headersOf(calls[0]!.init)["Idempotency-Key"]).toBe("key-create");
    expect(headersOf(calls[1]!.init)["Idempotency-Key"]).toBe("key-remove");
  });

  it("omits If-Match on create alone, because there is no revision to match yet", async () => {
    const calls = record({ data: { account: account() } });

    await accounts.create(
      { venue: "binance", displayName: "n", productPolicy: ["manual"], routes: [], credentialBundle: bundle },
      "key-create",
    );

    expect(headersOf(calls[0]!.init)["If-Match"]).toBeUndefined();
  });

  it("reads the gateway plane's envelope, which has no meta key", async () => {
    record({ data: { accounts: [account(), account({ accountId: "acct-2" })] } });

    const list = await accounts.list();

    expect(list.map((entry) => entry.accountId)).toEqual(["acct-1", "acct-2"]);
  });

  it("escapes the account id into the path", async () => {
    const calls = record({ data: { account: account() } });

    await accounts.get("acct/1 2");

    expect(calls[0]!.url).toBe("/api/v1/accounts/acct%2F1%202");
  });
});

describe("trading envelopes", () => {
  it("unwraps the items envelope every list route uses", async () => {
    record({ meta: {}, data: { items: [{ instrumentId: "BTC-USDT" }] } });

    const positions = await trading.positions({ accountId: "acct-1" });

    expect(positions).toHaveLength(1);
  });

  it("reads a runtime list from trading, not from the gateway record", async () => {
    const calls = record({ meta: {}, data: { items: [] } });

    await trading.accountRuntimes("acct-1");

    expect(calls[0]!.url).toBe("/api/v1/accounts/acct-1/runtimes");
  });

  it("scopes live venue positions to one owned account and instrument", async () => {
    const calls = record({ meta: {}, data: { items: [{ quantity: "1" }] } });

    const positions = await accountData.positions("acct-1", "binance:live:perp:USDT:BTC-USDT");

    expect(positions).toHaveLength(1);
    expect(calls[0]!.url).toBe(
      "/api/v1/account-positions?accountId=acct-1&instrumentId=binance%3Alive%3Aperp%3AUSDT%3ABTC-USDT",
    );
  });

  it("keeps the catalog's bare array and its projection metadata together", async () => {
    record({ meta: {}, data: [{ venue: "binance" }], projection: { revision: "3", staleness: "fresh" } });

    const venues = await catalog.venues();

    expect(venues.items).toHaveLength(1);
    expect(venues.projection).toEqual({ revision: "3", staleness: "fresh" });
  });

  it("returns the venue budget alongside a metered read", async () => {
    record({ meta: {}, data: { candles: [] }, rateLimit: { remaining: 900 } });

    const page = await market.candles({
      instrumentId: "binance:live:spot:BTC-USDT",
      interval: "1m",
    });

    expect(page.rateLimit).toEqual({ remaining: 900 });
  });
});

describe("the manual product", () => {
  it("sends the idempotency key in the header and the body from one value", async () => {
    const calls = record({ meta: {}, data: { commandId: "cmd-1", state: "accepted" } });

    await manual.command({ idempotencyKey: "key-7", previewId: "prev-1" } as never);

    // Trading compares the two byte-for-byte and rejects a mismatch with 400.
    expect(headersOf(calls[0]!.init)["Idempotency-Key"]).toBe("key-7");
    expect(JSON.parse(String(calls[0]!.init.body)).idempotencyKey).toBe("key-7");
  });

  it("reads the revisioned envelope the venue route alone speaks", async () => {
    record({ meta: {}, data: { revision: "12", items: [{ venue: "binance" }] } });

    const venues = await manual.venues();

    expect(venues.revision).toBe("12");
    expect(venues.items).toHaveLength(1);
  });
});

describe("auth", () => {
  it("captures the CSRF token from a login so the first write is not a 403", async () => {
    record({ meta: {}, data: { session: { csrfToken: "csrf-1" }, principal: { id: "p1" } } });

    await auth.login("operator", "secret");

    expect(currentCsrfToken()).toBe("csrf-1");
  });

  it("clears the token even when the logout request fails", async () => {
    setCsrfToken("csrf-1");
    vi.stubGlobal(
      "fetch",
      vi.fn(async () => new Response("{}", { status: 500, headers: { "Content-Type": "application/json" } })),
    );

    await expect(auth.logout()).rejects.toBeDefined();

    expect(currentCsrfToken()).toBeNull();
  });
});
