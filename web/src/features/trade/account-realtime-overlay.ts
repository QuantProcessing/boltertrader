import type { AccountPosition, AccountPositionSnapshot, Command, CommandState } from "@/api/types";

interface AccountWireEvent {
  eventId: string;
  accountId: string;
  resourceVersion: number;
  kind: string;
  observedAt: string;
  payload: unknown;
}

interface OptionalDecimalWire {
  value?: unknown;
  valid?: unknown;
}

interface PositionWire {
  position_slot?: unknown;
  side?: unknown;
  quantity?: unknown;
  entry_price?: unknown;
  mark_price?: unknown;
  unrealized_pnl?: unknown;
  liquidation_price?: OptionalDecimalWire;
  leverage?: OptionalDecimalWire;
  margin_used?: OptionalDecimalWire;
}

interface PositionPayloadWire {
  account_id?: unknown;
  route_id?: unknown;
  instrument_id?: unknown;
  position_slot?: unknown;
  operation?: unknown;
  position?: PositionWire;
}

interface OrderPayloadWire {
  command_id?: unknown;
  account_id?: unknown;
  state?: unknown;
}

interface PositionOverlay {
  event: AccountWireEvent;
  payload: PositionPayloadWire;
}

interface OrderOverlay {
  event: AccountWireEvent;
  payload: OrderPayloadWire;
}

export interface PositionOverlayFilter {
  accountId?: string;
  instrumentId?: string;
  venue?: string;
  product?: string;
}

const positionOverlays = new Map<string, PositionOverlay>();
const orderOverlays = new Map<string, OrderOverlay>();

/**
 * Accept one Gateway account notification into the browser read model.
 * Identity plus the venue/entity timestamp decides precedence; socket arrival
 * order and a locally generated sequence deliberately do not.
 */
export function recordAccountRealtimeEvent(value: unknown): "position" | "order" | "other" {
  const event = parseAccountEvent(value);
  if (!event) return "other";
  if (event.kind === "account.position") {
    const payload = parsePositionPayload(event.payload);
    if (!payload) return "other";
    const key = positionKey(event.accountId, payload);
    const current = positionOverlays.get(key);
    if (!current || newer(event, current.event)) positionOverlays.set(key, { event, payload });
    return "position";
  }
  if (event.kind === "account.order") {
    const payload = parseOrderPayload(event.payload);
    if (!payload) return "other";
    const key = String(payload.command_id);
    const current = orderOverlays.get(key);
    if (!current || newer(event, current.event)) orderOverlays.set(key, { event, payload });
    return "order";
  }
  return "other";
}

/** Apply newer WS facts over a periodic REST reconciliation result. */
export function applyPositionOverlays(snapshot: AccountPositionSnapshot, filter: PositionOverlayFilter = {}): AccountPositionSnapshot {
  const verifiedAt = epoch(snapshot.verifiedAt);
  let items = [...snapshot.items];
  for (const [key, overlay] of positionOverlays) {
    if (verifiedAt >= epoch(overlay.event.observedAt)) {
      positionOverlays.delete(key);
      continue;
    }
    const account = snapshot.accounts.find((candidate) => candidate.accountId === overlay.event.accountId);
    if (!account || !matchesFilter(account.venue, overlay, filter)) continue;
    const index = items.findIndex((item) => accountPositionKey(item) === key);
    const current = index >= 0 ? items[index] : undefined;
    if (current && !newerThanPosition(overlay.event, current)) continue;
    const remove = overlay.payload.operation === "remove" || decimalString(overlay.payload.position?.quantity) === "0";
    if (remove) {
      if (index >= 0) items.splice(index, 1);
      continue;
    }
    const projected = projectPosition(snapshot, overlay, current);
    if (!projected) continue;
    if (index >= 0) items[index] = projected;
    else items.push(projected);
  }
  if (items.length === snapshot.items.length && items.every((item, index) => item === snapshot.items[index])) return snapshot;
  return {
    ...snapshot,
    projectionRevision: snapshot.projectionRevision + 1,
    summary: { ...snapshot.summary, positionCount: items.length },
    items,
  };
}

function matchesFilter(venue: string, overlay: PositionOverlay, filter: PositionOverlayFilter): boolean {
  if (filter.accountId && filter.accountId !== overlay.event.accountId) return false;
  const instrumentId = stringValue(overlay.payload.instrument_id);
  if (filter.instrumentId && filter.instrumentId !== instrumentId) return false;
  if (filter.venue && filter.venue !== venue) return false;
  if (filter.product && filter.product !== instrumentId.split(":")[2]) return false;
  return true;
}

/** Apply newer order WS facts over one 60-second REST result. */
export function applyOrderOverlays(commands: Command[]): Command[] {
  let changed = false;
  const next = commands.map((command) => {
    const overlay = orderOverlays.get(command.commandId);
    if (!overlay) return command;
    if (epoch(command.updatedAt) >= epoch(overlay.event.observedAt)) {
      orderOverlays.delete(command.commandId);
      return command;
    }
    const state = commandState(overlay.payload.state);
    if (!state) return command;
    changed = true;
    return { ...command, state, updatedAt: overlay.event.observedAt };
  });
  return changed ? next : commands;
}

function parseAccountEvent(value: unknown): AccountWireEvent | null {
  if (!isObject(value)) return null;
  const event = {
    eventId: stringValue(value.eventId),
    accountId: stringValue(value.accountId),
    resourceVersion: numberValue(value.resourceVersion),
    kind: stringValue(value.kind),
    observedAt: stringValue(value.observedAt),
    payload: value.payload,
  };
  if (!event.eventId || !event.accountId || !event.kind || !event.observedAt || !Number.isFinite(Date.parse(event.observedAt))) return null;
  return event;
}

function parsePositionPayload(value: unknown): PositionPayloadWire | null {
  if (!isObject(value) || !isObject(value.position)) return null;
  const payload = value as PositionPayloadWire;
  if (!stringValue(payload.route_id) || !stringValue(payload.instrument_id)) return null;
  if (payload.operation !== "upsert" && payload.operation !== "remove") return null;
  return payload;
}

function parseOrderPayload(value: unknown): OrderPayloadWire | null {
  if (!isObject(value) || !stringValue(value.command_id) || !stringValue(value.state)) return null;
  return value as OrderPayloadWire;
}

function projectPosition(snapshot: AccountPositionSnapshot, overlay: PositionOverlay, current?: AccountPosition): AccountPosition | null {
  const position = overlay.payload.position;
  if (!position) return null;
  const account = snapshot.accounts.find((candidate) => candidate.accountId === overlay.event.accountId);
  if (!account) return null;
  const instrumentId = stringValue(overlay.payload.instrument_id);
  const quantity = decimalString(position.quantity);
  if (!instrumentId || quantity === "") return null;
  const parts = instrumentId.split(":");
  const side = position.side === "buy" || position.side === "sell"
    ? position.side
    : quantity.startsWith("-") ? "sell" : "buy";
  return {
    ...current,
    accountId: overlay.event.accountId,
    routeId: stringValue(overlay.payload.route_id),
    instrumentId,
    positionSlot: positionSlot(overlay.payload.position_slot ?? position.position_slot),
    accountLabel: current?.accountLabel ?? account.label,
    venue: current?.venue ?? account.venue,
    environment: current?.environment ?? account.environment,
    product: current?.product ?? parts[2],
    settlementAsset: current?.settlementAsset ?? (parts[2] === "perp" ? parts[3] : undefined),
    symbol: current?.symbol ?? parts.at(-1),
    side,
    quantity,
    entryPrice: nonZeroDecimal(position.entry_price),
    markPrice: nonZeroDecimal(position.mark_price),
    markPriceSource: nonZeroDecimal(position.mark_price) ? "venue_position" : "unavailable",
    unrealizedPnl: decimalString(position.unrealized_pnl) || "0",
    unrealizedPnlSource: "venue_position",
    liquidationPrice: positiveOptional(position.liquidation_price),
    leverage: nonNegativeOptional(position.leverage),
    marginUsed: nonNegativeOptional(position.margin_used),
    routeHealth: "live",
    freshness: "live",
    writeReady: current?.writeReady ?? account.writeReady,
    blockedReason: undefined,
    resourceVersion: overlay.event.resourceVersion,
    observedAt: overlay.event.observedAt,
    receivedAt: new Date().toISOString(),
  };
}

function positionKey(accountId: string, payload: PositionPayloadWire): string {
  return [accountId, stringValue(payload.route_id), stringValue(payload.instrument_id), positionSlot(payload.position_slot ?? payload.position?.position_slot)].join("\u0000");
}

function accountPositionKey(item: AccountPosition): string {
  return [item.accountId, item.routeId ?? "", item.instrumentId, item.positionSlot ?? "net"].join("\u0000");
}

function newer(left: AccountWireEvent, right: AccountWireEvent): boolean {
  const timeOrder = epoch(left.observedAt) - epoch(right.observedAt);
  if (timeOrder !== 0) return timeOrder > 0;
  if (left.resourceVersion !== right.resourceVersion) return left.resourceVersion > right.resourceVersion;
  return left.eventId > right.eventId;
}

function newerThanPosition(event: AccountWireEvent, current: AccountPosition): boolean {
  const currentAt = epoch(current.observedAt);
  const eventAt = epoch(event.observedAt);
  if (eventAt !== currentAt) return eventAt > currentAt;
  return event.resourceVersion >= (current.resourceVersion ?? 0);
}

function commandState(value: unknown): CommandState | null {
  switch (value) {
    case "prepared":
    case "accepted":
      return "accepted";
    case "dispatching":
    case "open":
    case "partially_filled":
      return "submitted";
    case "uncertain":
    case "filled":
    case "canceled":
      return value;
    case "rejected":
      return "failed";
    default:
      return null;
  }
}

function positionSlot(value: unknown): "net" | "long" | "short" {
  return value === "long" || value === "short" ? value : "net";
}

function positiveOptional(value: OptionalDecimalWire | undefined): string | undefined {
  if (value?.valid !== true) return undefined;
  const decimal = decimalString(value.value);
  return decimal !== "" && Number(decimal) > 0 ? decimal : undefined;
}

function nonNegativeOptional(value: OptionalDecimalWire | undefined): string | undefined {
  if (value?.valid !== true) return undefined;
  const decimal = decimalString(value.value);
  return decimal !== "" && Number(decimal) >= 0 ? decimal : undefined;
}

function nonZeroDecimal(value: unknown): string {
  const decimal = decimalString(value);
  return decimal !== "" && Number(decimal) !== 0 ? decimal : "";
}

function decimalString(value: unknown): string {
  return typeof value === "string" && value.trim() !== "" && Number.isFinite(Number(value)) ? value : "";
}

function epoch(value: string | undefined): number {
  const parsed = value ? Date.parse(value) : Number.NaN;
  return Number.isFinite(parsed) ? parsed : -Infinity;
}

function isObject(value: unknown): value is Record<string, unknown> {
  return typeof value === "object" && value !== null;
}

function stringValue(value: unknown): string {
  return typeof value === "string" ? value : "";
}

function numberValue(value: unknown): number {
  return typeof value === "number" && Number.isFinite(value) ? value : 0;
}
