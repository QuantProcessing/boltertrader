// Wire types of the gateway /v1 API (see docs/rewrite/gateway-api-draft.md).

export type Product = "spot" | "perp";
export type Side = "buy" | "sell";

export interface Instrument {
  id: string;
  venue: string;
  product: Product;
  symbol: string;
  base: string;
  quote: string;
  settle?: string;
  tickSize: string;
  stepSize: string;
  minQty: string;
  minNotional?: string;
  marketFamily?: string;
}

export interface CredentialField {
  key: string;
  label: string;
  metadata?: boolean;
  required: boolean;
  valueType: "string" | "integer";
}

export interface VenueInfo {
  venue: string;
  markets: string[];
  fields: CredentialField[];
}

export interface Account {
  id: string;
  venue: string;
  label: string;
  createdAt: string;
  markets: string[];
  active: boolean;
}

export type Level = [string, string];

export interface Book {
  bids: Level[];
  asks: Level[];
  ts: number;
}

export interface Trade {
  p: string;
  q: string;
  side: Side;
  ts: number;
}

export interface Candle {
  t: number;
  o: string;
  h: string;
  l: string;
  c: string;
  v: string;
}

export interface Ticker {
  mark?: string;
  index?: string;
  funding?: string;
  nextFunding?: number;
  oi?: string;
  ts: number;
}

export interface Stats {
  last: string;
  open24h: string;
  high24h: string;
  low24h: string;
  volume24h: string;
  funding?: string;
  nextFunding?: number;
  mark?: string;
}

export interface Position {
  inst: string;
  side: "long" | "short";
  size: string;
  entryPrice: string;
  markPrice?: string;
  unrealizedPnl: string;
  liquidationPrice?: string;
  leverage?: string;
  margin?: string;
}

export interface Balance {
  market: string;
  asset: string;
  total: string;
  available: string;
  locked: string;
}

export type OrderStatus = "open" | "partially_filled" | "filled" | "canceled" | "rejected" | "pending";

export interface Order {
  inst: string;
  orderId: string;
  clientOrderId?: string;
  side: Side;
  type: "market" | "limit";
  price?: string;
  qty: string;
  filled: string;
  avgPrice?: string;
  tif?: string;
  reduceOnly?: boolean;
  status: OrderStatus;
  createdAt?: number;
  updatedAt?: number;
}

export interface Fill {
  inst: string;
  fillId: string;
  orderId: string;
  clientOrderId?: string;
  side: Side;
  price: string;
  qty: string;
  fee: string;
  feeAsset?: string;
  liquidity?: string;
  ts: number;
}

export interface PlaceRequest {
  inst: string;
  side: Side;
  type: "market" | "limit";
  qty: string;
  price?: string;
  tif?: "gtc" | "ioc" | "post_only";
  reduceOnly?: boolean;
  clientOrderId: string;
}

export interface PlaceResult {
  status: "accepted" | "rejected" | "unknown";
  order?: Order;
  venueCode?: string;
  venueMessage?: string;
  message?: string;
}

export interface LookupResult {
  status: "found" | "not_found" | "unconfirmed";
  order?: Order;
}

export interface AccountStatus {
  connected: boolean;
  markets: Record<string, boolean>;
}
