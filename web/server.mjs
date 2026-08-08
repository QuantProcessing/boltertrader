import { randomUUID } from "node:crypto";
import { createReadStream } from "node:fs";
import { stat } from "node:fs/promises";
import http from "node:http";
import https from "node:https";
import { dirname, extname, resolve, sep } from "node:path";
import { fileURLToPath, pathToFileURL } from "node:url";

const API_PREFIX = "/api/v1";
const DEFAULT_LISTEN_ADDRESS = "127.0.0.1:13001";
const HOP_BY_HOP_HEADERS = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "proxy-authorization",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);
const SENSITIVE_REQUEST_HEADERS = new Set([
  "authorization",
  "proxy-authorization",
  "x-gateway-token",
  "x-internal-token",
  "x-worker-token",
]);
const MIME_TYPES = new Map([
  [".css", "text/css; charset=utf-8"],
  [".html", "text/html; charset=utf-8"],
  [".ico", "image/x-icon"],
  [".jpeg", "image/jpeg"],
  [".jpg", "image/jpeg"],
  [".js", "text/javascript; charset=utf-8"],
  [".json", "application/json; charset=utf-8"],
  [".map", "application/json; charset=utf-8"],
  [".png", "image/png"],
  [".svg", "image/svg+xml"],
  [".txt", "text/plain; charset=utf-8"],
  [".webp", "image/webp"],
  [".woff", "font/woff"],
  [".woff2", "font/woff2"],
]);

const moduleDirectory = dirname(fileURLToPath(import.meta.url));

export function createWebHandler(options = {}) {
  const distDir = resolve(options.distDir ?? resolve(moduleDirectory, "dist"));
  const platformUrl = normalizePlatformUrl(
    options.platformUrl ?? process.env.BT_PLATFORM_BROWSER_URL,
  );

  return (request, response) => {
    let pathname;
    try {
      pathname = new URL(request.url ?? "/", "http://web.local").pathname;
    } catch {
      writeJSONError(response, 400, "INVALID_REQUEST", "request is invalid", requestId(request));
      return;
    }
    if (pathname === API_PREFIX || pathname.startsWith(`${API_PREFIX}/`)) {
      proxyPlatformRequest(request, response, platformUrl);
      return;
    }
    void serveStaticRequest(request, response, distDir).catch(() => {
      if (!response.headersSent) {
        writeJSONError(
          response,
          503,
          "WEB_ASSETS_UNAVAILABLE",
          "web application is unavailable",
          requestId(request),
        );
      } else {
        response.destroy();
      }
    });
  };
}

export async function startWebServer(options = {}) {
  const { host, port } = parseListenAddress(
    options.listenAddress ?? process.env.BT_WEB_LISTEN_ADDR ?? DEFAULT_LISTEN_ADDRESS,
  );
  const server = http.createServer(createWebHandler(options));
  await new Promise((resolvePromise, rejectPromise) => {
    const onError = (error) => rejectPromise(error);
    server.once("error", onError);
    server.listen(port, host, () => {
      server.off("error", onError);
      resolvePromise();
    });
  });
  return server;
}

async function serveStaticRequest(request, response, distDir) {
  if (request.method !== "GET" && request.method !== "HEAD") {
    response.writeHead(405, { Allow: "GET, HEAD" });
    response.end();
    return;
  }

  let pathname;
  try {
    pathname = decodeURIComponent(new URL(request.url ?? "/", "http://web.local").pathname);
  } catch {
    writeJSONError(response, 400, "INVALID_REQUEST", "request is invalid", requestId(request));
    return;
  }
  if (pathname.includes("\0")) {
    writeJSONError(response, 400, "INVALID_REQUEST", "request is invalid", requestId(request));
    return;
  }

  const candidate = resolve(distDir, `.${pathname}`);
  const withinDist = candidate === distDir || candidate.startsWith(`${distDir}${sep}`);
  const directFile = withinDist ? await regularFile(candidate) : null;
  const hasExtension = Boolean(extname(pathname));
  const file = directFile ?? (!hasExtension ? await regularFile(resolve(distDir, "index.html")) : null);
  if (!file) {
    response.writeHead(404, {
      "Cache-Control": "no-store",
      "Content-Type": "text/plain; charset=utf-8",
    });
    response.end("Not Found");
    return;
  }

  const extension = extname(file.path).toLowerCase();
  const isIndex = file.path === resolve(distDir, "index.html");
  response.writeHead(200, {
    "Cache-Control": isIndex ? "no-cache" : "public, max-age=31536000, immutable",
    "Content-Length": String(file.size),
    "Content-Type": MIME_TYPES.get(extension) ?? "application/octet-stream",
    "X-Content-Type-Options": "nosniff",
  });
  if (request.method === "HEAD") {
    response.end();
    return;
  }
  createReadStream(file.path).pipe(response);
}

async function regularFile(path) {
  try {
    const details = await stat(path);
    return details.isFile() ? { path, size: details.size } : null;
  } catch {
    return null;
  }
}

function proxyPlatformRequest(request, response, platformUrl) {
  const incoming = new URL(request.url ?? API_PREFIX, "http://web.local");
  const suffix = incoming.pathname.slice(API_PREFIX.length);
  const target = new URL(platformUrl);
  target.pathname = `${target.pathname.replace(/\/+$/, "")}${suffix}`;
  target.search = incoming.search;
  target.hash = "";
  target.username = "";
  target.password = "";

  const headers = sanitizeRequestHeaders(request.headers);
  headers.host = target.host;
  headers["x-request-id"] ??= requestId(request);
  const transport = target.protocol === "https:" ? https : http;
  const upstreamRequest = transport.request(target, {
    method: request.method,
    headers,
  }, (upstreamResponse) => {
    const responseHeaders = sanitizeResponseHeaders(upstreamResponse.headers);
    response.writeHead(upstreamResponse.statusCode ?? 502, responseHeaders);
    upstreamResponse.pipe(response);
    upstreamResponse.on("error", () => response.destroy());
  });

  upstreamRequest.on("error", () => {
    if (response.headersSent) {
      response.destroy();
      return;
    }
    writeJSONError(
      response,
      502,
      "UPSTREAM_UNAVAILABLE",
      "Platform API is unavailable",
      headers["x-request-id"],
    );
  });
  request.on("aborted", () => upstreamRequest.destroy());
  request.pipe(upstreamRequest);
}

function sanitizeRequestHeaders(source) {
  const headers = {};
  for (const [rawName, value] of Object.entries(source)) {
    const name = rawName.toLowerCase();
    if (
      value === undefined
      || name === "host"
      || HOP_BY_HOP_HEADERS.has(name)
      || SENSITIVE_REQUEST_HEADERS.has(name)
    ) {
      continue;
    }
    headers[name] = value;
  }
  return headers;
}

function sanitizeResponseHeaders(source) {
  const headers = {};
  for (const [rawName, value] of Object.entries(source)) {
    const name = rawName.toLowerCase();
    if (value === undefined || HOP_BY_HOP_HEADERS.has(name)) continue;
    headers[name] = value;
  }
  return headers;
}

function normalizePlatformUrl(value) {
  if (!value) throw new TypeError("BT_PLATFORM_BROWSER_URL is required");
  const url = new URL(value);
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new TypeError("BT_PLATFORM_BROWSER_URL must use HTTP or HTTPS");
  }
  url.username = "";
  url.password = "";
  url.search = "";
  url.hash = "";
  if (url.pathname.replace(/\/+$/, "") !== API_PREFIX) {
    throw new TypeError("BT_PLATFORM_BROWSER_URL must end with /api/v1");
  }
  url.pathname = API_PREFIX;
  return url;
}

function parseListenAddress(value) {
  const separator = value.lastIndexOf(":");
  const host = value.slice(0, separator).trim();
  const port = Number(value.slice(separator + 1));
  if (separator <= 0 || !host || !Number.isInteger(port) || port < 1 || port > 65_535) {
    throw new TypeError("BT_WEB_LISTEN_ADDR must be host:port");
  }
  return { host, port };
}

function requestId(request) {
  const provided = request.headers?.["x-request-id"];
  if (typeof provided === "string" && provided.trim()) return provided.trim();
  return `req_${randomUUID().replaceAll("-", "").slice(0, 20)}`;
}

function writeJSONError(response, status, code, message, correlationId) {
  const body = JSON.stringify({
    error: {
      code,
      message,
      details: {},
      retryable: status >= 500,
      requestId: correlationId,
    },
  });
  response.writeHead(status, {
    "Cache-Control": "no-store",
    "Content-Length": String(Buffer.byteLength(body)),
    "Content-Type": "application/json; charset=utf-8",
    "X-Content-Type-Options": "nosniff",
  });
  response.end(body);
}

const isMain = process.argv[1]
  && import.meta.url === pathToFileURL(resolve(process.argv[1])).href;

if (isMain) {
  startWebServer()
    .then((server) => {
      const address = server.address();
      const display = address && typeof address !== "string"
        ? `${address.address}:${address.port}`
        : String(address ?? DEFAULT_LISTEN_ADDRESS);
      process.stdout.write(`BolterTrader web listening on ${display}\n`);
    })
    .catch(() => {
      process.stderr.write("BolterTrader web failed to start\n");
      process.exitCode = 1;
    });
}
