import type {
  Account, Balance, Book, Candle, Fill, Instrument, LookupResult, Order, PlaceRequest, PlaceResult, Position, Stats, VenueInfo,
} from "./types";

export class ApiError extends Error {
  constructor(public status: number, public code: string, message: string, public venueCode?: string) {
    super(message);
  }
}

let onUnauthenticated: () => void = () => {};
export function setUnauthenticatedHandler(handler: () => void) {
  onUnauthenticated = handler;
}

async function request<T>(method: string, path: string, body?: unknown, signal?: AbortSignal): Promise<T> {
  const headers: Record<string, string> = { "X-Bolter": "1" };
  if (body !== undefined) headers["Content-Type"] = "application/json";
  const response = await fetch(path, {
    method, headers, signal, credentials: "same-origin",
    body: body === undefined ? undefined : JSON.stringify(body),
  });
  if (response.status === 204) return undefined as T;
  const payload = await response.json().catch(() => ({}));
  if (response.status === 401) onUnauthenticated();
  // Place returns a typed outcome on 202/422 as well.
  if (!response.ok && !(path.endsWith("/orders") && method === "POST" && payload.status)) {
    const error = payload.error ?? {};
    throw new ApiError(response.status, error.code ?? "HTTP_" + response.status, error.message ?? response.statusText, error.venueCode);
  }
  return payload as T;
}

const gw = (path: string) => "/api/gw/v1" + path;
const query = (params: Record<string, string | number | undefined>) => {
  const search = new URLSearchParams();
  for (const [key, value] of Object.entries(params)) if (value !== undefined && value !== "") search.set(key, String(value));
  const text = search.toString();
  return text ? "?" + text : "";
};
const acc = (id: string, path: string) => gw(`/accounts/${encodeURIComponent(id)}${path}`);

export const api = {
  session: () => request<{ authenticated: boolean }>("GET", "/api/auth/session"),
  login: (password: string) => request<{ authenticated: boolean }>("POST", "/api/auth/login", { password }),
  logout: () => request<{ authenticated: boolean }>("POST", "/api/auth/logout", {}),

  venues: () => request<VenueInfo[]>("GET", gw("/venues")),
  instruments: () => request<Instrument[]>("GET", gw("/instruments")),
  orderBook: (inst: string) => request<Book>("GET", gw("/orderbook" + query({ inst, depth: 20 }))),
  candles: (inst: string, interval: string, end?: number, signal?: AbortSignal) =>
    request<Candle[]>("GET", gw("/candles" + query({ inst, interval, end, limit: 500 })), undefined, signal),
  stats: (inst: string, signal?: AbortSignal) => request<Stats>("GET", gw("/ticker" + query({ inst })), undefined, signal),

  accounts: () => request<Account[]>("GET", gw("/accounts")),
  createAccount: (venue: string, label: string, credentials: Record<string, string>) =>
    request<Account>("POST", gw("/accounts"), { venue, label, credentials }),
  deleteAccount: (id: string) => request<void>("DELETE", acc(id, "")),

  positions: (id: string) => request<Position[]>("GET", acc(id, "/positions")),
  balances: (id: string) => request<Balance[]>("GET", acc(id, "/balances")),
  openOrders: (id: string, inst?: string) => request<Order[]>("GET", acc(id, "/orders" + query({ inst }))),
  fills: (id: string, inst?: string) => request<Fill[]>("GET", acc(id, "/fills" + query({ inst, limit: 100 }))),
  place: (id: string, order: PlaceRequest) => request<PlaceResult>("POST", acc(id, "/orders"), order),
  lookup: (id: string, inst: string, clientOrderId: string) =>
    request<LookupResult>("GET", acc(id, "/orders/lookup" + query({ inst, clientOrderId }))),
  cancel: (id: string, inst: string, orderId: string) =>
    request<{ ok: boolean }>("DELETE", acc(id, `/orders/${encodeURIComponent(orderId)}` + query({ inst }))),
  setLeverage: (id: string, inst: string, leverage: number) =>
    request<{ leverage: number }>("PUT", acc(id, "/leverage"), { inst, leverage }),
};

/** Client order ids: (unix ms << 7) | random(0..127), a positive uint48. */
let lastId = 0;
export function newClientOrderId(): string {
  let id = Date.now() * 128 + Math.floor(Math.random() * 128);
  if (id <= lastId) id = lastId + 1;
  lastId = id;
  return String(id);
}
