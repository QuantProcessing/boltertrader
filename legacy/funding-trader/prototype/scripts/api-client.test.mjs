import assert from "node:assert/strict";
import test from "node:test";

import { ApiError, FundingTraderApiClient, generateIdempotencyKey } from "../src/apiClient.js";

test("API client sends cookie credentials, request id, CSRF, idempotency, and encoded paths", async () => {
  const calls = [];
  const client = new FundingTraderApiClient({
    requestIdFactory: () => "req-test",
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse(202, { data: { executionId: "ex-1" }, meta: { requestId: "req-test" } });
    },
  });

  const response = await client.closePosition("position/1", {
    csrfToken: "csrf-token",
    idempotencyKey: "idem-close",
  });

  assert.equal(response.data.executionId, "ex-1");
  assert.equal(calls[0].url, "/api/v1/positions/position%2F1/close");
  assert.equal(calls[0].init.credentials, "include");
  assert.equal(calls[0].init.method, "POST");
  assert.equal(calls[0].init.headers.get("X-Request-Id"), "req-test");
  assert.equal(calls[0].init.headers.get("X-CSRF-Token"), "csrf-token");
  assert.equal(calls[0].init.headers.get("Idempotency-Key"), "idem-close");
  assert.equal(calls[0].init.body, undefined);
});

test("API client does not invent idempotency keys for execution writes", async () => {
  const client = new FundingTraderApiClient({
    fetchImpl: async () => {
      throw new Error("fetch must not be called");
    },
  });

  await assert.rejects(
    () => client.submitOrder({ operationKind: "open" }, { csrfToken: "csrf" }),
    (error) => error instanceof ApiError && error.code === "missing_idempotency_key",
  );
});

test("API client surfaces backend error envelope and 401 session handling", async () => {
  const client = new FundingTraderApiClient({
    requestIdFactory: () => "req-fallback",
    fetchImpl: async () => jsonResponse(401, {
      error: {
        code: "unauthenticated",
        message: "session expired",
        retryable: false,
        requestId: "req-auth",
      },
    }),
  });

  await assert.rejects(
    () => client.getSession(),
    (error) => {
      assert.equal(error.status, 401);
      assert.equal(error.code, "unauthenticated");
      assert.equal(error.requestId, "req-auth");
      assert.equal(error.isAuthError, true);
      return true;
    },
  );
});

test("readiness accepts 503 success-shaped responses", async () => {
  const client = new FundingTraderApiClient({
    fetchImpl: async () => jsonResponse(503, {
      data: {
        ready: false,
        components: { gateway: { ready: false, reason: "reconnecting" } },
      },
      meta: { requestId: "req-ready" },
    }),
  });

  const readiness = await client.getReadiness();
  assert.equal(readiness.data.ready, false);
  assert.equal(readiness.data.components.gateway.reason, "reconnecting");
});

test("API client rejects non-JSON and malformed successful responses", async () => {
  for (const response of [
    new Response("not json", { status: 200, headers: { "content-type": "text/plain" } }),
    new Response("{", { status: 200, headers: { "content-type": "application/json" } }),
  ]) {
    const client = new FundingTraderApiClient({
      requestIdFactory: () => "req-invalid-response",
      fetchImpl: async () => response,
    });
    await assert.rejects(
      () => client.getSession(),
      (error) => error instanceof ApiError &&
        error.code === "invalid_response" &&
        error.requestId === "req-invalid-response",
    );
  }
});

test("query params repeat arrays and generated idempotency keys are stable strings", async () => {
  const calls = [];
  const client = new FundingTraderApiClient({
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse(200, { data: [], meta: { requestId: "req" } });
    },
  });

  await client.listPositions({ state: ["open", "attention"], cursor: "c 1", empty: "" });
  assert.equal(calls[0].url, "/api/v1/positions?state=open&state=attention&cursor=c+1");
  assert.match(generateIdempotencyKey("open"), /^open_[a-z0-9]+_[a-z0-9]+/i);
});

test("API client strips browser product mode override query params", async () => {
  const calls = [];
  const client = new FundingTraderApiClient({
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse(200, { data: [], meta: { requestId: "req" } });
    },
  });

  await client.listOpportunities({
    mode: "live",
    environment: "demo",
    shortVenueId: "bybit-demo",
  });

  assert.equal(calls[0].url, "/api/v1/opportunities?shortVenueId=bybit-demo");
});

test("API client exposes authenticated ledger reads without browser mode overrides", async () => {
  const calls = [];
  const client = new FundingTraderApiClient({
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return jsonResponse(200, { data: [], meta: { requestId: "req-ledger" } });
    },
  });

  await client.getPortfolioLedger({ reportingAsset: "USDT", mode: "live" });
  await client.getVerification({ environment: "demo" });
  await client.listClosedPositions({ reportingAsset: "USDT", state: "open", mode: "live" });
  await client.getPositionLedger("position/1");
  await client.listFundingPayments({
    accountId: "account-1",
    instrumentId: "BTC/USDT perp",
    positionId: "position-1",
    from: "2026-08-01T00:00:00Z",
    to: "2026-08-02T00:00:00Z",
    mode: "live",
  });

  assert.deepEqual(calls.map((call) => call.url), [
    "/api/v1/ledger/portfolio?reportingAsset=USDT",
    "/api/v1/system/verification",
    "/api/v1/positions?reportingAsset=USDT&state=closed",
    "/api/v1/positions/position%2F1",
    "/api/v1/ledger/funding-payments?accountId=account-1&instrumentId=BTC%2FUSDT+perp&positionId=position-1&from=2026-08-01T00%3A00%3A00Z&to=2026-08-02T00%3A00%3A00Z",
  ]);
  assert.equal(calls.every((call) => call.init.credentials === "include"), true);
  assert.equal(calls.every((call) => call.init.method === "GET"), true);
});

function jsonResponse(status, body) {
  return new Response(JSON.stringify(body), {
    status,
    headers: { "content-type": "application/json" },
  });
}
