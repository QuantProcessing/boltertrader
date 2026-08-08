// @vitest-environment node

import { mkdtemp, rm, writeFile } from "node:fs/promises";
import { createServer, type RequestListener, type Server } from "node:http";
import { tmpdir } from "node:os";
import { join } from "node:path";

import { afterEach, describe, expect, it } from "vitest";

import { createWebHandler } from "../../server.mjs";

const servers: Server[] = [];
const directories: string[] = [];

afterEach(async () => {
  await Promise.all(servers.splice(0).map((server) => new Promise<void>((resolve) => {
    server.close(() => resolve());
  })));
  await Promise.all(directories.splice(0).map((directory) => rm(directory, {
    recursive: true,
    force: true,
  })));
});

async function listen(handler: RequestListener): Promise<{ server: Server; origin: string }> {
  const server = createServer(handler);
  servers.push(server);
  await new Promise<void>((resolve) => server.listen(0, "127.0.0.1", resolve));
  const address = server.address();
  if (!address || typeof address === "string") throw new Error("server did not bind TCP");
  return { server, origin: `http://127.0.0.1:${address.port}` };
}

describe("production web server", () => {
  it("exports the production handler and server entrypoint", async () => {
    const module = await import("../../server.mjs").catch(() => null);

    expect(module?.createWebHandler).toBeTypeOf("function");
    expect(module?.startWebServer).toBeTypeOf("function");
  });

  it("serves immutable assets and falls back to index.html for SPA routes", async () => {
    const distDir = await mkdtemp(join(tmpdir(), "bolter-web-dist-"));
    directories.push(distDir);
    await writeFile(join(distDir, "index.html"), "<!doctype html><main>BolterTrader</main>");
    await writeFile(join(distDir, "app.js"), "globalThis.BOLTER_LOADED = true;");
    const web = await listen(createWebHandler({
      distDir,
      platformUrl: "http://127.0.0.1:1/api/v1",
    }));

    const route = await fetch(`${web.origin}/funding/opportunities`);
    const asset = await fetch(`${web.origin}/app.js`);

    expect(route.status).toBe(200);
    expect(await route.text()).toContain("BolterTrader");
    expect(route.headers.get("Content-Type")).toContain("text/html");
    expect(asset.headers.get("Cache-Control")).toBe("public, max-age=31536000, immutable");
    expect(asset.headers.get("Content-Type")).toContain("text/javascript");
  });

  it("streams API requests while preserving browser security and correlation headers", async () => {
    let received: {
      url?: string;
      headers?: Record<string, string | string[] | undefined>;
      body?: string;
    } = {};
    const upstream = await listen((request, response) => {
      const chunks: Buffer[] = [];
      request.on("data", (chunk) => chunks.push(Buffer.from(chunk)));
      request.on("end", () => {
        received = {
          url: request.url,
          headers: request.headers,
          body: Buffer.concat(chunks).toString("utf8"),
        };
        response.writeHead(201, {
          "Content-Type": "application/json",
          "Set-Cookie": "bt_session=session-2; Path=/; HttpOnly; SameSite=Strict",
        });
        response.end(JSON.stringify({ data: { commandId: "command-1" } }));
      });
    });
    const web = await listen(createWebHandler({
      distDir: join(tmpdir(), "unused-bolter-dist"),
      platformUrl: `${upstream.origin}/api/v1`,
    }));

    const response = await fetch(`${web.origin}/api/v1/commands?wait=true`, {
      method: "POST",
      headers: {
        Authorization: "Bearer browser-credential-that-must-not-cross",
        Cookie: "bt_session=session-1",
        "Content-Type": "application/json",
        "Idempotency-Key": "idem-1",
        "X-CSRF-Token": "csrf-1",
        "X-Request-Id": "req-1",
      },
      body: JSON.stringify({ commandKind: "place" }),
    });

    expect(response.status).toBe(201);
    expect(received.url).toBe("/api/v1/commands?wait=true");
    expect(received.body).toBe(JSON.stringify({ commandKind: "place" }));
    expect(received.headers).toMatchObject({
      cookie: "bt_session=session-1",
      "idempotency-key": "idem-1",
      "x-csrf-token": "csrf-1",
      "x-request-id": "req-1",
    });
    expect(received.headers?.authorization).toBeUndefined();
    expect(response.headers.get("Set-Cookie")).toContain("bt_session=session-2");
  });

  it("passes SSE response chunks through without changing stream semantics", async () => {
    const upstream = await listen((_request, response) => {
      response.writeHead(200, {
        "Content-Type": "text/event-stream",
        "Cache-Control": "no-cache",
        "X-Accel-Buffering": "no",
      });
      response.write("id: opaque-1\nevent: command.accepted\ndata: {\"sequence\":1}\n\n");
      setTimeout(() => response.end("id: opaque-2\nevent: command.finalized\ndata: {\"sequence\":2}\n\n"), 5);
    });
    const web = await listen(createWebHandler({
      distDir: join(tmpdir(), "unused-bolter-dist"),
      platformUrl: `${upstream.origin}/api/v1`,
    }));

    const response = await fetch(`${web.origin}/api/v1/realtime/stream?cursor=opaque-0`, {
      headers: { Cookie: "bt_session=session-1" },
    });
    const stream = await response.text();

    expect(response.headers.get("Content-Type")).toContain("text/event-stream");
    expect(response.headers.get("X-Accel-Buffering")).toBe("no");
    expect(stream).toContain("id: opaque-1");
    expect(stream).toContain("id: opaque-2");
  });

  it("redacts upstream transport errors", async () => {
    const unavailable = await listen((_request, response) => response.end());
    const address = unavailable.server.address();
    if (!address || typeof address === "string") throw new Error("server did not bind TCP");
    await new Promise<void>((resolve) => unavailable.server.close(() => resolve()));
    servers.splice(servers.indexOf(unavailable.server), 1);
    const target = `http://127.0.0.1:${address.port}/api/v1`;
    const web = await listen(createWebHandler({
      distDir: join(tmpdir(), "unused-bolter-dist"),
      platformUrl: target,
    }));

    const response = await fetch(`${web.origin}/api/v1/system/status`, {
      headers: { "X-Request-Id": "req-redacted" },
    });
    const body = await response.text();

    expect(response.status).toBe(502);
    expect(body).toContain("UPSTREAM_UNAVAILABLE");
    expect(body).toContain("req-redacted");
    expect(body).not.toContain("ECONNREFUSED");
    expect(body).not.toContain(target);
  });
});
