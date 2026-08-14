import { afterEach, describe, expect, it, vi } from "vitest";

import { advanceSessionEpoch, apiRequest, setCsrfToken } from "./client";
import { ApiError, NetworkError, StaleSessionError } from "./errors";

/* ---------------------------------------------------------------------------
 * The transport's job is to be boring in ways that are hard to notice when they
 * break: a missing CSRF header only fails on the first write, a blank query
 * parameter only fails on the routes that validate strictly, and an unenveloped
 * 401 only fails by showing an unexplained error instead of a login screen.
 * Each of those is tested here rather than discovered in a session.
 * ------------------------------------------------------------------------- */

type Handler = (url: string, init: RequestInit) => Response | Promise<Response>;

function mockFetch(handler: Handler) {
  const fetchMock = vi.fn(async (input: unknown, init: unknown) =>
    handler(String(input), (init ?? {}) as RequestInit),
  );
  vi.stubGlobal("fetch", fetchMock);
  return fetchMock;
}

const json = (body: unknown, status = 200): Response =>
  new Response(JSON.stringify(body), { status, headers: { "Content-Type": "application/json" } });

const headersOf = (init: RequestInit): Record<string, string> =>
  (init.headers ?? {}) as Record<string, string>;

afterEach(() => {
  vi.unstubAllGlobals();
  setCsrfToken(null);
});

describe("apiRequest headers", () => {
  it("sends the CSRF token on unsafe methods and withholds it from safe ones", async () => {
    setCsrfToken("token-abc");
    const seen: Record<string, string>[] = [];
    mockFetch((_url, init) => {
      seen.push(headersOf(init));
      return json({ ok: true });
    });

    await apiRequest("/api/v1/me");
    await apiRequest("/api/v1/products/manual/orders", { method: "POST", body: {} });

    expect(seen[0]?.["X-CSRF-Token"]).toBeUndefined();
    expect(seen[1]?.["X-CSRF-Token"]).toBe("token-abc");
  });

  it("sets Content-Type only when there is a body to type", async () => {
    const seen: Record<string, string>[] = [];
    mockFetch((_url, init) => {
      seen.push(headersOf(init));
      return json({ ok: true });
    });

    await apiRequest("/api/v1/accounts/a/validate", { method: "POST" });
    await apiRequest("/api/v1/accounts", { method: "POST", body: { venue: "binance" } });

    expect(seen[0]?.["Content-Type"]).toBeUndefined();
    expect(seen[1]?.["Content-Type"]).toBe("application/json");
  });

  it("passes the idempotency key and the If-Match revision through untouched", async () => {
    let init!: RequestInit;
    mockFetch((_url, seen) => {
      init = seen;
      return json({ ok: true });
    });

    await apiRequest("/api/v1/accounts/a", {
      method: "DELETE",
      idempotencyKey: "key-1",
      ifMatch: '"desired-state-revision-4"',
    });

    expect(headersOf(init)["Idempotency-Key"]).toBe("key-1");
    expect(headersOf(init)["If-Match"]).toBe('"desired-state-revision-4"');
  });
});

describe("apiRequest query", () => {
  it("drops absent and blank values rather than sending them named-but-empty", async () => {
    let url = "";
    mockFetch((seen) => {
      url = seen;
      return json({ ok: true });
    });

    await apiRequest("/api/v1/positions", {
      query: { accountId: "acct-1", productId: "", lineageId: undefined, limit: 0, ready: false },
    });

    expect(url).toBe("/api/v1/positions?accountId=acct-1&limit=0&ready=false");
  });

  it("leaves the path alone when there is nothing to ask for", async () => {
    let url = "";
    mockFetch((seen) => {
      url = seen;
      return json({ ok: true });
    });

    await apiRequest("/api/v1/activity", { query: { kind: "" } });

    expect(url).toBe("/api/v1/activity");
  });
});

describe("apiRequest responses", () => {
  it("treats a 204 as the whole answer", async () => {
    mockFetch(() => new Response(null, { status: 204 }));
    await expect(apiRequest("/api/v1/auth/session", { method: "DELETE" })).resolves.toBeUndefined();
  });

  it("raises the server's own error envelope", async () => {
    mockFetch(() =>
      json(
        {
          error: {
            code: "DESIRED_STATE_REVISION_CONFLICT",
            message: "revision 3 is stale",
            details: { current: 4 },
            retryable: false,
            requestId: "req-9",
          },
        },
        409,
      ),
    );

    const error = await apiRequest("/api/v1/accounts/a/drain", { method: "POST" }).catch((e) => e);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({
      status: 409,
      code: "DESIRED_STATE_REVISION_CONFLICT",
      message: "revision 3 is stale",
      requestId: "req-9",
    });
  });

  it("invents a code when the failure came from somewhere that does not speak the envelope", async () => {
    // A proxy hop with no upstream: HTML body, no error object. It must still
    // become an auth failure, because that is what decides whether the operator
    // is sent to the login screen or shown an error they cannot act on.
    mockFetch(() => new Response("<html>denied</html>", { status: 401 }));

    const error = await apiRequest("/api/v1/me").catch((e) => e);

    expect(error).toBeInstanceOf(ApiError);
    expect(error).toMatchObject({ status: 401, code: "AUTH_REQUIRED", retryable: false });
  });

  it("marks an unenveloped server failure retryable", async () => {
    mockFetch(() => new Response("bad gateway", { status: 502 }));

    const error = await apiRequest("/api/v1/candles").catch((e) => e);

    expect(error).toMatchObject({ code: "UPSTREAM_UNAVAILABLE", retryable: true });
  });

  it("reports a body it cannot read as an error rather than crashing on the parse", async () => {
    mockFetch(() => new Response("not json", { status: 200 }));

    const error = await apiRequest("/api/v1/positions").catch((e) => e);

    expect(error).toMatchObject({ code: "INVALID_RESPONSE" });
  });
});

describe("apiRequest session epoch", () => {
  it("discards an answer that lands after the session changed", async () => {
    mockFetch(() => {
      // The logout happens while this request is in flight.
      advanceSessionEpoch();
      return json({ data: { items: [] } });
    });

    await expect(apiRequest("/api/v1/positions")).rejects.toBeInstanceOf(StaleSessionError);
  });

  it("reports a transport failure as a network error", async () => {
    mockFetch(() => {
      throw new TypeError("Failed to fetch");
    });

    const error = await apiRequest("/api/v1/positions").catch((e) => e);

    expect(error).toBeInstanceOf(NetworkError);
    expect(error).not.toBeInstanceOf(ApiError);
  });

  it("lets a caller's own cancellation through unchanged", async () => {
    const controller = new AbortController();
    mockFetch(() => {
      controller.abort();
      throw new DOMException("The operation was aborted.", "AbortError");
    });

    const error = await apiRequest("/api/v1/positions", { signal: controller.signal }).catch((e) => e);

    expect(error).not.toBeInstanceOf(NetworkError);
    expect((error as Error).name).toBe("AbortError");
  });
});
