const DEFAULT_MAX_BODY_BYTES = 1024 * 1024;

const SENSITIVE_REQUEST_HEADERS = new Set([
  "authorization",
  "proxy-authorization",
  "x-api-key",
]);

const HOP_BY_HOP_HEADERS = new Set([
  "connection",
  "keep-alive",
  "proxy-authenticate",
  "te",
  "trailer",
  "transfer-encoding",
  "upgrade",
]);

const REQUEST_HEADERS_TO_DROP = new Set([
  ...SENSITIVE_REQUEST_HEADERS,
  ...HOP_BY_HOP_HEADERS,
  "accept-encoding",
  "content-length",
  "host",
]);

const RESPONSE_HEADERS_TO_DROP = new Set([
  ...HOP_BY_HOP_HEADERS,
  "content-encoding",
  "content-length",
]);

export function resolveBackendOrigin(env = process.env) {
  const raw = env.FT_BACKEND_ORIGIN?.trim();
  if (!raw) return null;
  const url = new URL(raw);
  if (url.username || url.password) {
    throw new Error("FT_BACKEND_ORIGIN must not include credentials");
  }
  if (url.protocol !== "http:" && url.protocol !== "https:") {
    throw new Error("FT_BACKEND_ORIGIN must use http or https");
  }
  url.pathname = "";
  url.search = "";
  url.hash = "";
  return url.toString().replace(/\/$/, "");
}

export function buildBackendUrl(requestUrl, origin) {
  const source = new URL(requestUrl);
  if (!source.pathname.startsWith("/api/")) {
    throw new Error("API proxy only supports /api/* paths");
  }
  const target = new URL(origin);
  target.pathname = source.pathname;
  target.search = source.search;
  target.hash = "";
  return target;
}

export async function proxyApiRequest(request, {
  env = process.env,
  fetchImpl = globalThis.fetch,
  maxBodyBytes = parseBodyLimit(env.FT_PROXY_MAX_BODY_BYTES),
} = {}) {
  if (typeof fetchImpl !== "function") {
    return jsonError(500, "proxy_fetch_unavailable", "fetch is unavailable");
  }

  let origin;
  let targetUrl;
  try {
    origin = resolveBackendOrigin(env);
    if (!origin) {
      return jsonError(503, "backend_origin_unconfigured", "FT_BACKEND_ORIGIN is not configured");
    }
    targetUrl = buildBackendUrl(request.url, origin);
  } catch (error) {
    return jsonError(500, "backend_origin_invalid", error?.message || "invalid backend origin");
  }

  let body;
  try {
    body = await bufferedRequestBody(request, maxBodyBytes);
  } catch (error) {
    return jsonError(error.status || 413, error.code || "request_body_too_large", error.message);
  }

  let upstream;
  try {
    upstream = await fetchImpl(targetUrl, {
      method: request.method,
      headers: proxyRequestHeaders(request.headers),
      body,
      redirect: "manual",
      cache: "no-store",
      signal: request.signal,
    });
  } catch {
    return jsonError(502, "backend_proxy_failed", "backend request failed");
  }

  return new Response(upstream.body, {
    status: upstream.status,
    statusText: upstream.statusText,
    headers: proxyResponseHeaders(upstream.headers),
  });
}

export function proxyRequestHeaders(sourceHeaders) {
  const headers = new Headers();
  for (const [name, value] of sourceHeaders.entries()) {
    const lower = name.toLowerCase();
    if (REQUEST_HEADERS_TO_DROP.has(lower)) continue;
    headers.append(name, value);
  }
  return headers;
}

export function proxyResponseHeaders(sourceHeaders) {
  const headers = new Headers();
  for (const [name, value] of sourceHeaders.entries()) {
    const lower = name.toLowerCase();
    if (RESPONSE_HEADERS_TO_DROP.has(lower)) continue;
    if (lower === "set-cookie") continue;
    headers.append(name, value);
  }

  const getSetCookie = sourceHeaders.getSetCookie;
  if (typeof getSetCookie === "function") {
    for (const cookie of getSetCookie.call(sourceHeaders)) {
      headers.append("set-cookie", cookie);
    }
  } else {
    const cookie = sourceHeaders.get("set-cookie");
    if (cookie) headers.set("set-cookie", cookie);
  }
  return headers;
}

async function bufferedRequestBody(request, maxBodyBytes) {
  const method = String(request.method || "GET").toUpperCase();
  if (method === "GET" || method === "HEAD") return undefined;

  const contentLength = request.headers.get("content-length");
  if (contentLength && Number(contentLength) > maxBodyBytes) {
    throw bodyLimitError(maxBodyBytes);
  }

  if (!request.body) return undefined;
  const reader = request.body.getReader();
  const chunks = [];
  let totalBytes = 0;
  try {
    while (true) {
      const { done, value } = await reader.read();
      if (done) break;
      const chunk = value instanceof Uint8Array ? value : new Uint8Array(value);
      totalBytes += chunk.byteLength;
      if (totalBytes > maxBodyBytes) {
        try {
          await reader.cancel();
        } catch {
          // Preserve the deterministic 413 response even if cancellation races.
        }
        throw bodyLimitError(maxBodyBytes);
      }
      chunks.push(chunk);
    }
  } finally {
    reader.releaseLock();
  }
  if (totalBytes === 0) return undefined;
  if (chunks.length === 1) return chunks[0];

  const body = new Uint8Array(totalBytes);
  let offset = 0;
  for (const chunk of chunks) {
    body.set(chunk, offset);
    offset += chunk.byteLength;
  }
  return body;
}

function parseBodyLimit(value) {
  if (!value) return DEFAULT_MAX_BODY_BYTES;
  const parsed = Number(value);
  if (!Number.isFinite(parsed) || parsed <= 0) return DEFAULT_MAX_BODY_BYTES;
  return Math.floor(parsed);
}

function bodyLimitError(maxBodyBytes) {
  const error = new Error(`request body exceeds ${maxBodyBytes} bytes`);
  error.status = 413;
  error.code = "request_body_too_large";
  return error;
}

function jsonError(status, code, message) {
  return Response.json({
    error: {
      code,
      message,
      retryable: status >= 500,
    },
  }, { status });
}
