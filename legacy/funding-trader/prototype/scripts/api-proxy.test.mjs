import assert from "node:assert/strict";
import test from "node:test";

import {
  buildBackendUrl,
  proxyApiRequest,
  proxyRequestHeaders,
  resolveBackendOrigin,
} from "../src/server/apiProxy.js";

test("proxy validates backend origin and preserves path/query", () => {
  assert.equal(
    resolveBackendOrigin({ FT_BACKEND_ORIGIN: "http://127.0.0.1:18080/base?x=1" }),
    "http://127.0.0.1:18080",
  );
  assert.equal(
    buildBackendUrl("http://ui.local/api/v1/positions/position%2F1/close?cursor=c+1", "http://backend.local").toString(),
    "http://backend.local/api/v1/positions/position%2F1/close?cursor=c+1",
  );
  assert.throws(
    () => resolveBackendOrigin({ FT_BACKEND_ORIGIN: "https://user:pass@example.test" }),
    /must not include credentials/,
  );
});

test("proxy forwards browser auth headers and strips gateway credentials", async () => {
  const inbound = new Headers({
    "accept": "application/json",
    "authorization": "Bearer browser-token",
    "cookie": "ft_session=s1",
    "content-type": "application/json",
    "host": "ui.local",
    "idempotency-key": "idem-1",
    "proxy-authorization": "Basic hidden",
    "x-api-key": "hidden",
    "x-csrf-token": "csrf",
    "x-request-id": "req-1",
  });

  const headers = proxyRequestHeaders(inbound);
  assert.equal(headers.get("cookie"), "ft_session=s1");
  assert.equal(headers.get("x-csrf-token"), "csrf");
  assert.equal(headers.get("idempotency-key"), "idem-1");
  assert.equal(headers.get("x-request-id"), "req-1");
  assert.equal(headers.get("authorization"), null);
  assert.equal(headers.get("proxy-authorization"), null);
  assert.equal(headers.get("x-api-key"), null);
  assert.equal(headers.get("host"), null);
});

test("proxy returns upstream 401 JSON and Set-Cookie instead of converting to 502", async () => {
  const calls = [];
  const response = await proxyApiRequest(new Request("http://ui.local/api/v1/auth/login", {
    method: "POST",
    headers: {
      "content-type": "application/json",
      "cookie": "ft_session=old",
      "authorization": "Bearer must-not-forward",
    },
    body: JSON.stringify({ email: "owner@example.test", password: "bad" }),
  }), {
    env: { FT_BACKEND_ORIGIN: "http://backend.local" },
    fetchImpl: async (url, init) => {
      calls.push({ url, init });
      return new Response(JSON.stringify({
        error: { code: "unauthenticated", message: "invalid credentials", retryable: false },
      }), {
        status: 401,
        headers: {
          "content-type": "application/json",
          "set-cookie": "ft_session=; Path=/; HttpOnly",
        },
      });
    },
  });

  assert.equal(response.status, 401);
  assert.equal(response.headers.get("content-type"), "application/json");
  assert.equal(response.headers.get("set-cookie"), "ft_session=; Path=/; HttpOnly");
  assert.equal(calls[0].url.toString(), "http://backend.local/api/v1/auth/login");
  assert.equal(calls[0].init.headers.get("authorization"), null);
  assert.equal(calls[0].init.headers.get("cookie"), "ft_session=old");
  assert.equal(await new Response(calls[0].init.body).text(), JSON.stringify({ email: "owner@example.test", password: "bad" }));
  assert.deepEqual(await response.json(), {
    error: { code: "unauthenticated", message: "invalid credentials", retryable: false },
  });
});

test("proxy buffers empty write body as undefined and preserves SSE stream", async () => {
  const chunks = ["event: heartbeat\ndata: {}\n\n"];
  let forwardedInit;
  const response = await proxyApiRequest(new Request("http://ui.local/api/v1/positions/p1/close", {
    method: "POST",
    headers: { "x-csrf-token": "csrf", "idempotency-key": "idem-close" },
  }), {
    env: { FT_BACKEND_ORIGIN: "http://backend.local" },
    fetchImpl: async (_url, init) => {
      forwardedInit = init;
      return new Response(new ReadableStream({
        start(controller) {
          for (const chunk of chunks) controller.enqueue(new TextEncoder().encode(chunk));
          controller.close();
        },
      }), {
        status: 200,
        headers: { "content-type": "text/event-stream" },
      });
    },
  });

  assert.equal(forwardedInit.body, undefined);
  assert.equal(response.status, 200);
  assert.equal(response.headers.get("content-type"), "text/event-stream");
  assert.equal(await response.text(), chunks.join(""));
});

test("proxy rejects oversized buffered bodies before forwarding", async () => {
  const response = await proxyApiRequest(new Request("http://ui.local/api/v1/orders", {
    method: "POST",
    headers: { "content-type": "application/json" },
    body: JSON.stringify({ big: true }),
  }), {
    env: { FT_BACKEND_ORIGIN: "http://backend.local" },
    maxBodyBytes: 4,
    fetchImpl: async () => {
      throw new Error("fetch must not be called");
    },
  });

  assert.equal(response.status, 413);
  assert.equal((await response.json()).error.code, "request_body_too_large");
});

test("proxy stops reading a chunked body as soon as the limit is exceeded", async () => {
  let pulls = 0;
  let cancelled = false;
  const body = new ReadableStream({
    pull(controller) {
      pulls += 1;
      if (pulls === 1) {
        controller.enqueue(new Uint8Array(5));
        return;
      }
      throw new Error("proxy read beyond the configured body limit");
    },
    cancel() {
      cancelled = true;
    },
  });
  const response = await proxyApiRequest(new Request("http://ui.local/api/v1/orders", {
    method: "POST",
    body,
    duplex: "half",
  }), {
    env: { FT_BACKEND_ORIGIN: "http://backend.local" },
    maxBodyBytes: 4,
    fetchImpl: async () => {
      throw new Error("fetch must not be called");
    },
  });

  assert.equal(response.status, 413);
  assert.equal((await response.json()).error.code, "request_body_too_large");
  assert.equal(pulls, 1);
  assert.equal(cancelled, true);
});

test("proxy forwards downstream cancellation and redacts transport failures", async () => {
  const controller = new AbortController();
  const request = new Request("http://ui.local/api/v1/stream", {
    signal: controller.signal,
  });
  let forwardedSignal;
  const response = await proxyApiRequest(request, {
    env: { FT_BACKEND_ORIGIN: "http://backend.local" },
    fetchImpl: async (_url, init) => {
      forwardedSignal = init.signal;
      throw new Error("connect ECONNREFUSED secret.internal:9000");
    },
  });

  assert.equal(forwardedSignal, request.signal);
  assert.equal(response.status, 502);
  const payload = await response.json();
  assert.equal(payload.error.message, "backend request failed");
  assert.doesNotMatch(JSON.stringify(payload), /secret\.internal|9000|ECONNREFUSED/);
});
