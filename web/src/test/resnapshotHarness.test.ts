import { createServer, type Server } from "node:http";

import { afterEach, describe, expect, it } from "vitest";

import {
  createResnapshotHarness,
  validateHarnessEnvironment,
} from "../../e2e/resnapshot-harness";

const servers: Server[] = [];

async function listen(server: Server): Promise<URL> {
  servers.push(server);
  await new Promise<void>((resolve, reject) => {
    server.once("error", reject);
    server.listen(0, "127.0.0.1", () => resolve());
  });
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("test server did not bind TCP");
  return new URL(`http://127.0.0.1:${address.port}/`);
}

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => server.close(() => resolve()))));
});

describe("real-browser resnapshot harness", () => {
  it("requires explicit exact Test mode and literal loopback endpoints", () => {
    const valid = {
      BT_DEV_REAL_TEST_HARNESS_ACCEPT: "1",
      FT_MODE: "test",
      BT_DEV_REAL_TEST_HARNESS_HOST: "127.0.0.1",
      BT_DEV_REAL_TEST_HARNESS_PORT: "13002",
      BT_DEV_REAL_TEST_HARNESS_UPSTREAM_URL: "http://127.0.0.1:13000/",
    };
    expect(validateHarnessEnvironment(valid)).toMatchObject({ host: "127.0.0.1", port: 13002 });
    expect(() => validateHarnessEnvironment({ ...valid, FT_MODE: "live" })).toThrow("exact Test mode");
    expect(() => validateHarnessEnvironment({ ...valid, BT_DEV_REAL_TEST_HARNESS_ACCEPT: "0" })).toThrow("explicit opt-in");
    expect(() => validateHarnessEnvironment({ ...valid, BT_DEV_REAL_TEST_HARNESS_HOST: "localhost" })).toThrow("literal 127.0.0.1");
    expect(() => validateHarnessEnvironment({ ...valid, BT_DEV_REAL_TEST_HARNESS_UPSTREAM_URL: "https://example.com/" })).toThrow("loopback upstream");
  });

  it("injects one idempotent resnapshot while forbidding canonical writes", async () => {
    const upstream = await listen(createServer((request, response) => {
      if (request.url === "/api/v1/realtime/stream") {
        response.writeHead(200, { "content-type": "text/event-stream", "cache-control": "no-cache" });
        response.write(": connected\n\n");
        return;
      }
      response.writeHead(200, { "content-type": "application/json" });
      response.end(JSON.stringify({ data: [], projection: { revision: "real-upstream" } }));
    }));
    let marker = 0;
    const harness = createResnapshotHarness({
      upstream,
      now: () => new Date("2026-08-10T00:00:00Z"),
      marker: () => `marker-${marker += 1}`,
    });
    const harnessURL = await listen(harness.server);

    const streamResponse = await fetch(new URL("api/v1/realtime/stream", harnessURL));
    const reader = streamResponse.body!.getReader();
    await reader.read();

    const first = await fetch(new URL("__bt/resnapshot", harnessURL), {
      method: "POST",
      headers: { "Idempotency-Key": "cycle-1" },
    });
    expect(first.ok).toBe(true);
    const result = await first.json();
    expect(result).toMatchObject({
      beforeMarker: "marker-1",
      afterMarker: "marker-2",
      triggerCount: 1,
      canonicalMutationCount: 0,
      activeHeadMutationCount: 0,
    });
    const injected = new TextDecoder().decode((await reader.read()).value);
    expect(injected).toContain("event: realtime.resnapshot_required");
    expect(injected).toContain("marker-2");

    const replay = await fetch(new URL("__bt/resnapshot", harnessURL), {
      method: "POST",
      headers: { "Idempotency-Key": "cycle-1" },
    });
    expect(await replay.json()).toEqual(result);
    expect(harness.snapshot().triggerCount).toBe(1);

    const catalog = await fetch(new URL("api/v1/catalog/venues", harnessURL));
    expect(await catalog.text()).toContain("marker-2");

    const forbidden = await fetch(new URL("api/v1/accounts/account-1/validate", harnessURL), { method: "POST" });
    expect(forbidden.status).toBe(405);
    expect(harness.snapshot().canonicalMutationCount).toBe(1);

    await reader.cancel();
  });

  it("rejects a second non-idempotent trigger", async () => {
    const upstream = await listen(createServer((_request, response) => response.end("ok")));
    const harness = createResnapshotHarness({ upstream });
    const harnessURL = await listen(harness.server);

    expect((await fetch(new URL("__bt/resnapshot", harnessURL), { method: "POST", headers: { "Idempotency-Key": "first" } })).status).toBe(200);
    expect((await fetch(new URL("__bt/resnapshot", harnessURL), { method: "POST", headers: { "Idempotency-Key": "second" } })).status).toBe(409);
    expect(harness.snapshot().triggerCount).toBe(1);
  });
});
