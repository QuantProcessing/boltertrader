import { describe, expect, it, vi } from "vitest";

import { AuthGenerationGuard, StaleAuthGenerationError } from "../state/authGeneration";
import { ApiClient, ApiError } from "../api/client";

function jsonResponse(body: unknown, status = 200): Response {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "Content-Type": "application/json" },
  });
}

describe("ApiClient", () => {
  it("exports the shared API client contract", async () => {
    const module = await import("../api/client").catch(() => null);

    expect(module?.ApiClient).toBeTypeOf("function");
    expect(module?.ApiError).toBeTypeOf("function");
    expect(module?.apiClient).toBeDefined();
  });

  it("sends same-origin reads with cookies and request correlation", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      jsonResponse({ meta: { requestId: "req-1" }, data: { ready: true } }),
    );
    const client = new ApiClient({
      fetchImpl,
      requestIdFactory: () => "req-1",
      authGuard: new AuthGenerationGuard(),
    });

    const result = await client.get<{ ready: boolean }>("/system/status", {
      query: { accountId: "account a", empty: "", ignored: undefined },
    });

    expect(result?.data).toEqual({ ready: true });
    expect(fetchImpl).toHaveBeenCalledOnce();
    const [url, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    expect(url).toBe("/api/v1/system/status?accountId=account+a");
    expect(init.credentials).toBe("include");
    expect(init.method).toBe("GET");
    expect(new Headers(init.headers).get("X-Request-Id")).toBe("req-1");
    expect(new Headers(init.headers).get("Accept")).toBe("application/json");
  });

  it("automatically attaches CSRF and explicit idempotency headers to mutations", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(jsonResponse({ data: { commandId: "cmd-1" } }));
    const client = new ApiClient({ fetchImpl, authGuard: new AuthGenerationGuard() });
    client.setSession({ csrfToken: "csrf-1" });

    await client.post("/commands", { commandKind: "place" }, { idempotencyKey: "idem-1" });

    const [, init] = fetchImpl.mock.calls[0] as [string, RequestInit];
    const headers = new Headers(init.headers);
    expect(headers.get("X-CSRF-Token")).toBe("csrf-1");
    expect(headers.get("Idempotency-Key")).toBe("idem-1");
    expect(headers.get("Content-Type")).toBe("application/json");
    expect(init.body).toBe(JSON.stringify({ commandKind: "place" }));
  });

  it("fails closed when an authenticated mutation has no CSRF token", async () => {
    const fetchImpl = vi.fn();
    const client = new ApiClient({ fetchImpl, authGuard: new AuthGenerationGuard() });

    await expect(client.patch("/accounts/account-1", { displayName: "Primary" })).rejects.toMatchObject({
      name: "ApiError",
      code: "MISSING_CSRF_TOKEN",
      status: 0,
    });
    expect(fetchImpl).not.toHaveBeenCalled();
  });

  it("captures the login CSRF token for subsequent mutations", async () => {
    const fetchImpl = vi
      .fn()
      .mockResolvedValueOnce(jsonResponse({ data: { session: { csrfToken: "csrf-login" } } }))
      .mockResolvedValueOnce(jsonResponse({ data: { accountId: "account-1" } }));
    const client = new ApiClient({ fetchImpl, authGuard: new AuthGenerationGuard() });

    await client.post("/auth/login", { username: "owner", password: "secret" });
    await client.post("/accounts", { displayName: "Primary" }, { idempotencyKey: "account-create" });

    const [, mutation] = fetchImpl.mock.calls[1] as [string, RequestInit];
    expect(new Headers(mutation.headers).get("X-CSRF-Token")).toBe("csrf-login");
  });

  it("keeps concurrent session reads in the same auth generation until the caller commits one", async () => {
    const fetchImpl = vi.fn().mockImplementation(() => Promise.resolve(
      jsonResponse({ data: { principal: { id: "operator-1" }, session: { csrfToken: "csrf-session" } } }),
    ));
    const client = new ApiClient({ fetchImpl, authGuard: new AuthGenerationGuard() });

    const results = await Promise.all([
      client.get("/auth/session"),
      client.get("/auth/session"),
    ]);

    expect(results).toHaveLength(2);
    expect(results.every((result) => result?.data)).toBe(true);
  });

  it("maps structured errors and signals session expiry on 401", async () => {
    const fetchImpl = vi.fn().mockResolvedValue(
      jsonResponse(
        {
          error: {
            code: "AUTH_REQUIRED",
            message: "authentication required",
            details: { reason: "expired" },
            retryable: false,
            requestId: "req-server",
          },
        },
        401,
      ),
    );
    const guard = new AuthGenerationGuard();
    const client = new ApiClient({ fetchImpl, authGuard: guard });
    client.setSession({ csrfToken: "csrf-1" });
    const expired = vi.fn();
    client.onSessionExpired(expired);

    const promise = client.get("/me");

    await expect(promise).rejects.toMatchObject({
      name: "ApiError",
      status: 401,
      code: "AUTH_REQUIRED",
      requestId: "req-server",
      details: { reason: "expired" },
      isAuthError: true,
    });
    expect(expired).toHaveBeenCalledOnce();
    expect(expired.mock.calls[0][0]).toBeInstanceOf(ApiError);
  });

  it("rejects a late response after the auth generation changes", async () => {
    let resolveResponse!: (response: Response) => void;
    const fetchImpl = vi.fn(
      () => new Promise<Response>((resolve) => {
        resolveResponse = resolve;
      }),
    );
    const client = new ApiClient({ fetchImpl, authGuard: new AuthGenerationGuard() });
    client.setSession({ csrfToken: "csrf-1" });

    const pending = client.post("/commands", { action: "open" }, { idempotencyKey: "idem-1" });
    client.clearSession("logout");
    resolveResponse(jsonResponse({ data: { commandId: "stale" } }));

    await expect(pending).rejects.toBeInstanceOf(StaleAuthGenerationError);
  });

  it("clears stable runtime idempotency keys at auth boundaries", () => {
    const idempotencyKeyFactory = vi
      .fn()
      .mockReturnValueOnce("idem-before")
      .mockReturnValueOnce("idem-after");
    const client = new ApiClient({
      fetchImpl: vi.fn(),
      authGuard: new AuthGenerationGuard(),
      idempotencyKeyFactory,
    });

    expect(client.idempotencyKey("manual.place")).toBe("idem-before");
    expect(client.idempotencyKey("manual.place")).toBe("idem-before");
    client.clearSession("logout");
    expect(client.idempotencyKey("manual.place")).toBe("idem-after");
  });

  it("rejects absolute or non-API request paths", async () => {
    const client = new ApiClient({ fetchImpl: vi.fn(), authGuard: new AuthGenerationGuard() });

    await expect(client.get("https://example.invalid/api/v1/me")).rejects.toThrow(
      "API paths must start with /",
    );
    await expect(client.get("/../internal/v1/status")).rejects.toThrow(
      "API path cannot escape /api/v1",
    );
  });
});
