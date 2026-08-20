/**
 * The gateway's canonical instrument id is an address, not a label:
 *
 *   binance:live:perp:USDT:BTC-USDT
 *
 * Keeping it intact is important for requests and audit trails, but printing it
 * in a table makes the operator decode storage syntax before they can identify
 * a market. This module is the one boundary where that address becomes a
 * user-facing identity. Callers still retain `raw` as a title for support and
 * diagnostics.
 */

export interface InstrumentIdentity {
  raw: string;
  symbol: string;
  venue?: string;
  environment?: string;
  product?: string;
  settlement?: string;
}

export interface InstrumentDisplaySource {
  instrumentId?: string | null;
  symbol?: string | null;
  baseAsset?: string | null;
  quoteAsset?: string | null;
}

const VENUES: Record<string, string> = {
  aster: "Aster",
  binance: "Binance",
  bybit: "Bybit",
  hyperliquid: "Hyperliquid",
  lighter: "Lighter",
  okx: "OKX",
};

const PRODUCTS: Record<string, string> = {
  future: "Futures",
  futures: "Futures",
  perp: "Perp",
  perpetual: "Perp",
  spot: "Spot",
};

/** Parse a canonical id while remaining useful for plain symbols and unknown venues. */
export function instrumentIdentity(value: string | null | undefined): InstrumentIdentity {
  const raw = value?.trim() ?? "";
  if (raw === "") return { raw, symbol: "—" };

  const parts = raw.split(":");
  if (parts.length < 4 || parts.some((part) => part === "")) {
    return { raw, symbol: raw };
  }

  const product = parts[2];
  return {
    raw,
    venue: parts[0],
    environment: parts[1],
    product,
    settlement: parts.length > 4 ? parts.slice(3, -1).join(":") : undefined,
    symbol: humanSymbol(parts.at(-1) ?? raw, product),
  };
}

/** Prefer structured assets; fall back to the canonical id, then the source symbol. */
export function instrumentDisplaySymbol(source: InstrumentDisplaySource): string {
  const base = source.baseAsset?.trim();
  const quote = source.quoteAsset?.trim();
  if (base && quote) return `${base}-${quote}`;
  if (source.instrumentId?.trim()) return instrumentSymbol(source.instrumentId);
  return humanSymbol(source.symbol?.trim() || "—");
}

/** The short label used wherever only the market itself is needed. */
export function instrumentSymbol(value: string | null | undefined): string {
  return instrumentIdentity(value).symbol;
}

/** Venue and product disambiguate identical symbols without exposing wire syntax. */
export function instrumentMeta(value: string | null | undefined): string | null {
  const identity = instrumentIdentity(value);
  if (!identity.venue && !identity.product) return null;

  const venue = identity.venue ? VENUES[identity.venue.toLowerCase()] ?? titleCase(identity.venue) : null;
  const product = identity.product ? PRODUCTS[identity.product.toLowerCase()] ?? titleCase(identity.product) : null;
  return [venue, product].filter(Boolean).join(" · ");
}

function titleCase(value: string): string {
  return value
    .split(/[-_\s]+/)
    .filter(Boolean)
    .map((part) => part.charAt(0).toUpperCase() + part.slice(1).toLowerCase())
    .join(" ");
}

function humanSymbol(symbol: string, product?: string): string {
  if (product === "perp" || product === "perpetual") {
    return symbol.replace(/(?:-|_)SWAP$/i, "").replace(/(?:-|_)PERP(?:ETUAL)?$/i, "");
  }
  return symbol;
}
