import { randomUUID } from "node:crypto";
import { createServer, request as httpRequest, type IncomingMessage, type ServerResponse } from "node:http";
import { resolve } from "node:path";
import { fileURLToPath } from "node:url";

interface HarnessEnvironment {
  [name: string]: string | undefined;
}

export interface ResnapshotHarnessConfiguration {
  host: "127.0.0.1";
  port: number;
  upstream: URL;
}

export interface ResnapshotHarnessSnapshot {
  marker: string;
  triggerCount: number;
  canonicalMutationCount: number;
  activeHeadMutationCount: number;
}

export interface ResnapshotHarnessResult extends ResnapshotHarnessSnapshot {
  beforeMarker: string;
  afterMarker: string;
}

interface HarnessOptions {
  upstream: URL;
  now?: () => Date;
  marker?: () => string;
}

const controlPath = "/__bt/resnapshot";
const markerPaths = new Set(["/api/v1/accounts", "/api/v1/catalog/venues"]);
const readMethods = new Set(["GET", "HEAD", "OPTIONS"]);

function parsePort(value: string | undefined): number {
  if (!value || !/^\d+$/.test(value)) throw new Error("Resnapshot harness requires an explicit TCP port.");
  const port = Number(value);
  if (!Number.isSafeInteger(port) || port < 1 || port > 65_535) throw new Error("Resnapshot harness port is invalid.");
  return port;
}

function exactLoopbackRoot(value: string, label: string): URL {
  const target = new URL(value);
  if (target.protocol !== "http:"
    || target.hostname !== "127.0.0.1"
    || target.port === ""
    || target.username !== ""
    || target.password !== ""
    || target.pathname !== "/"
    || target.search !== ""
    || target.hash !== "") {
    throw new Error(`Resnapshot harness requires a literal loopback ${label}.`);
  }
  return target;
}

export function validateHarnessEnvironment(environment: HarnessEnvironment): ResnapshotHarnessConfiguration {
  if (environment.BT_DEV_REAL_TEST_HARNESS_ACCEPT !== "1") {
    throw new Error("Resnapshot harness requires explicit opt-in.");
  }
  if (environment.FT_MODE !== "test") {
    throw new Error("Resnapshot harness requires exact Test mode.");
  }
  if (environment.BT_DEV_ENABLE_LIVE_PRIVATE_WRITES === "1" || environment.BT_LIVE_PRIVATE_WRITES_ENABLED === "1") {
    throw new Error("Resnapshot harness refuses Live private writes.");
  }
  const host = environment.BT_DEV_REAL_TEST_HARNESS_HOST;
  if (host !== "127.0.0.1") {
    throw new Error("Resnapshot harness host must be literal 127.0.0.1.");
  }
  return {
    host,
    port: parsePort(environment.BT_DEV_REAL_TEST_HARNESS_PORT),
    upstream: exactLoopbackRoot(environment.BT_DEV_REAL_TEST_HARNESS_UPSTREAM_URL ?? "", "loopback upstream"),
  };
}

function isAllowedProxyRequest(method: string, pathname: string): boolean {
  return readMethods.has(method)
    || (method === "POST" && pathname === "/api/v1/auth/login")
    || (method === "DELETE" && pathname === "/api/v1/auth/session");
}

function writeJSON(response: ServerResponse, status: number, value: unknown): void {
  const body = Buffer.from(`${JSON.stringify(value)}\n`);
  response.writeHead(status, {
    "cache-control": "no-store",
    "content-length": String(body.length),
    "content-type": "application/json",
  });
  response.end(body);
}

function isActiveHeadMutation(pathname: string): boolean {
  const normalized = pathname.toLowerCase();
  return normalized.includes("gateway") && (normalized.includes("active-head") || normalized.includes("promote"));
}

function markerResponse(body: Buffer, marker: string): Buffer {
  try {
    const parsed = JSON.parse(body.toString("utf8")) as Record<string, unknown>;
    if (!parsed || Array.isArray(parsed) || typeof parsed !== "object") return body;
    return Buffer.from(`${JSON.stringify({ ...parsed, testHarness: { marker } })}\n`);
  } catch {
    return body;
  }
}

export function createResnapshotHarness(options: HarnessOptions) {
  const upstream = exactLoopbackRoot(options.upstream.toString(), "loopback upstream");
  const now = options.now ?? (() => new Date());
  const makeMarker = options.marker ?? (() => `g010-resnapshot-${randomUUID()}`);
  const streams = new Set<ServerResponse>();
  const replays = new Map<string, ResnapshotHarnessResult>();
  let marker = makeMarker();
  let triggerCount = 0;
  let canonicalMutationCount = 0;
  let activeHeadMutationCount = 0;

  const snapshot = (): ResnapshotHarnessSnapshot => ({
    marker,
    triggerCount,
    canonicalMutationCount,
    activeHeadMutationCount,
  });

  const trigger = (idempotencyKey: string): ResnapshotHarnessResult | null => {
    const replay = replays.get(idempotencyKey);
    if (replay) return replay;
    if (triggerCount !== 0) return null;

    const beforeMarker = marker;
    marker = makeMarker();
    triggerCount += 1;
    const result: ResnapshotHarnessResult = {
      beforeMarker,
      afterMarker: marker,
      ...snapshot(),
    };
    replays.set(idempotencyKey, result);
    const envelope = JSON.stringify({
      eventId: `harness-event-${triggerCount}`,
      sequence: triggerCount,
      cursor: "",
      kind: "realtime.resnapshot_required",
      observedAt: now().toISOString(),
      payload: { marker },
    });
    const event = `id: harness-${triggerCount}\nevent: realtime.resnapshot_required\ndata: ${envelope}\n\n`;
    for (const stream of streams) stream.write(event);
    return result;
  };

  const proxy = (request: IncomingMessage, response: ServerResponse, pathname: string) => {
    const target = new URL(request.url ?? "/", upstream);
    const headers = { ...request.headers, host: upstream.host };
    delete headers.connection;
    const outgoing = httpRequest(target, { method: request.method, headers }, (incoming) => {
      const contentType = incoming.headers["content-type"] ?? "";
      const stream = pathname === "/api/v1/realtime/stream" && incoming.statusCode === 200 && contentType.includes("text/event-stream");
      const decorate = triggerCount > 0 && markerPaths.has(pathname) && contentType.includes("json");
      if (!decorate) {
        response.writeHead(incoming.statusCode ?? 502, incoming.headers);
        if (stream) {
          streams.add(response);
          response.once("close", () => streams.delete(response));
        }
        incoming.pipe(response);
        return;
      }

      const chunks: Buffer[] = [];
      let size = 0;
      incoming.on("data", (chunk: Buffer) => {
        size += chunk.length;
        if (size > 16 * 1024 * 1024) {
          outgoing.destroy(new Error("upstream snapshot exceeds harness limit"));
          return;
        }
        chunks.push(Buffer.from(chunk));
      });
      incoming.on("end", () => {
        const body = markerResponse(Buffer.concat(chunks), marker);
        const responseHeaders = { ...incoming.headers, "content-length": String(body.length) };
        delete responseHeaders["transfer-encoding"];
        response.writeHead(incoming.statusCode ?? 502, responseHeaders);
        response.end(body);
      });
    });
    outgoing.on("error", () => {
      if (!response.headersSent) writeJSON(response, 502, { error: { code: "HARNESS_UPSTREAM_UNAVAILABLE" } });
      else response.destroy();
    });
    response.once("close", () => outgoing.destroy());
    request.pipe(outgoing);
  };

  const server = createServer((request, response) => {
    const method = request.method ?? "GET";
    let pathname: string;
    try {
      pathname = new URL(request.url ?? "/", upstream).pathname;
    } catch {
      writeJSON(response, 400, { error: { code: "HARNESS_INVALID_URL" } });
      return;
    }
    if (pathname === controlPath) {
      if (method !== "POST") {
        writeJSON(response, 405, { error: { code: "HARNESS_METHOD_NOT_ALLOWED" } });
        return;
      }
      const idempotencyKey = String(request.headers["idempotency-key"] ?? "").trim();
      if (idempotencyKey === "" || idempotencyKey.length > 128) {
        writeJSON(response, 400, { error: { code: "HARNESS_IDEMPOTENCY_KEY_REQUIRED" } });
        return;
      }
      const result = trigger(idempotencyKey);
      if (!result) {
        writeJSON(response, 409, { error: { code: "HARNESS_ALREADY_TRIGGERED" }, ...snapshot() });
        return;
      }
      writeJSON(response, 200, result);
      return;
    }
    if (!isAllowedProxyRequest(method, pathname)) {
      canonicalMutationCount += 1;
      if (isActiveHeadMutation(pathname)) activeHeadMutationCount += 1;
      writeJSON(response, 405, { error: { code: "HARNESS_WRITE_FORBIDDEN" } });
      return;
    }
    proxy(request, response, pathname);
  });
  server.on("clientError", (_error, socket) => socket.end("HTTP/1.1 400 Bad Request\r\nConnection: close\r\n\r\n"));

  return { server, snapshot };
}

async function main(): Promise<void> {
  const configuration = validateHarnessEnvironment(process.env);
  const harness = createResnapshotHarness({ upstream: configuration.upstream });
  await new Promise<void>((resolve, reject) => {
    harness.server.once("error", reject);
    harness.server.listen(configuration.port, configuration.host, () => resolve());
  });
  process.stdout.write(`resnapshot-harness: http://${configuration.host}:${configuration.port}${controlPath}\n`);
  const close = () => harness.server.close(() => process.exit(0));
  process.once("SIGINT", close);
  process.once("SIGTERM", close);
}

if (process.argv[1] && fileURLToPath(import.meta.url) === resolve(process.argv[1])) {
  void main().catch((error: unknown) => {
    process.stderr.write(`resnapshot-harness: ${error instanceof Error ? error.message : "startup failed"}\n`);
    process.exitCode = 1;
  });
}
