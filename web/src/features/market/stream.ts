import { useEffect, useState } from "react";

import { paths } from "@/api/endpoints";
import type { Candle, CandleInterval, DepthLevel } from "@/api/types";

export interface LiveOrderBook {
  bids: DepthLevel[];
  asks: DepthLevel[];
  observedAt: string;
}

interface StreamEvent {
  kind: "order_book" | "candles";
  eventType: "snapshot" | "delta" | "update" | "resync_required";
  instrumentId: string;
  observedAt: string;
  bids?: DepthLevel[];
  asks?: DepthLevel[];
  candle?: Candle;
}

/** One public venue stream per resource, both carried through the same origin. */
export function usePublicMarketStreams(instrumentId: string | undefined, interval: CandleInterval) {
  const [book, setBook] = useState<LiveOrderBook | null>(null);
  const [candle, setCandle] = useState<Candle | null>(null);

  useEffect(() => {
    setBook(null);
    setCandle(null);
    if (!instrumentId || typeof WebSocket === "undefined") return;

    let disposed = false;
    const sockets = new Set<WebSocket>();
    const timers = new Set<number>();

    const connect = (kind: StreamEvent["kind"], streamInterval?: CandleInterval) => {
      let attempts = 0;
      const open = () => {
        if (disposed) return;
        const socket = new WebSocket(streamURL(instrumentId, kind, streamInterval));
        sockets.add(socket);
        socket.addEventListener("open", () => {
          attempts = 0;
        });
        socket.addEventListener("message", (message) => {
          const event = parseStreamEvent(message.data);
          if (!event || event.instrumentId !== instrumentId || event.kind !== kind) return;
          if (event.eventType === "resync_required") {
            socket.close();
            return;
          }
          if (kind === "order_book") {
            setBook((current) => applyBookEvent(current, event));
          } else if (event.candle) {
            setCandle(event.candle);
          }
        });
        socket.addEventListener("close", () => {
          sockets.delete(socket);
          if (disposed) return;
          const delay = Math.min(10_000, 500 * 2 ** Math.min(attempts, 5));
          attempts += 1;
          const timer = window.setTimeout(() => {
            timers.delete(timer);
            open();
          }, delay);
          timers.add(timer);
        });
        // `error` is followed by `close`; closing here makes every browser use
        // the same bounded reconnect path instead of leaving a half-open socket.
        socket.addEventListener("error", () => socket.close());
      };
      open();
    };

    connect("order_book");
    connect("candles", interval);
    return () => {
      disposed = true;
      for (const timer of timers) window.clearTimeout(timer);
      for (const socket of sockets) socket.close();
    };
  }, [instrumentId, interval]);

  return { book, candle };
}

export function mergeLiveCandle(candles: Candle[] | undefined, live: Candle | null, limit: number): Candle[] | undefined {
  if (!candles || !live) return candles;
  const next = [...candles];
  const at = next.findIndex((candidate) => candidate.openTime === live.openTime);
  if (at >= 0) next[at] = live;
  else next.push(live);
  next.sort((left, right) => Date.parse(left.openTime) - Date.parse(right.openTime));
  return next.slice(-limit);
}

function streamURL(instrumentId: string, kind: StreamEvent["kind"], interval?: CandleInterval): string {
  const url = new URL(paths.publicMarketSocket, window.location.href);
  url.protocol = url.protocol === "https:" ? "wss:" : "ws:";
  url.searchParams.set("instrumentId", instrumentId);
  url.searchParams.set("kind", kind);
  if (interval) url.searchParams.set("interval", interval);
  return url.toString();
}

function parseStreamEvent(raw: unknown): StreamEvent | null {
  if (typeof raw !== "string") return null;
  try {
    const value = JSON.parse(raw) as Partial<StreamEvent>;
    if (
      (value.kind !== "order_book" && value.kind !== "candles") ||
      typeof value.eventType !== "string" ||
      typeof value.instrumentId !== "string" ||
      typeof value.observedAt !== "string"
    ) return null;
    return value as StreamEvent;
  } catch {
    return null;
  }
}

export function applyBookEvent(current: LiveOrderBook | null, event: StreamEvent): LiveOrderBook | null {
  // Venue deltas commonly touch one side only. Omitted means "no changes",
  // not "malformed"; an explicitly malformed side still rejects the event.
  const bids = event.bids === undefined ? [] : validLevels(event.bids);
  const asks = event.asks === undefined ? [] : validLevels(event.asks);
  if (!bids || !asks) return current;
  if (event.eventType === "snapshot" || current === null) {
    return { bids: sortLevels(bids, true), asks: sortLevels(asks, false), observedAt: event.observedAt };
  }
  return {
    bids: patchLevels(current.bids, bids, true),
    asks: patchLevels(current.asks, asks, false),
    observedAt: event.observedAt,
  };
}

function validLevels(value: DepthLevel[] | undefined): DepthLevel[] | null {
  if (!Array.isArray(value)) return null;
  return value.every((level) => typeof level.price === "string" && typeof level.quantity === "string") ? value : null;
}

function patchLevels(current: DepthLevel[], changes: DepthLevel[], descending: boolean): DepthLevel[] {
  const levels = new Map(current.map((level) => [level.price, level]));
  for (const level of changes) {
    if (decimalIsZero(level.quantity)) levels.delete(level.price);
    else levels.set(level.price, level);
  }
  return sortLevels([...levels.values()], descending).slice(0, 100);
}

function sortLevels(levels: DepthLevel[], descending: boolean): DepthLevel[] {
  return [...levels].sort((left, right) => {
    const a = Number.parseFloat(left.price);
    const b = Number.parseFloat(right.price);
    if (!Number.isFinite(a) || !Number.isFinite(b)) return left.price.localeCompare(right.price);
    return descending ? b - a : a - b;
  });
}

function decimalIsZero(value: string): boolean {
  return /^[-+]?0+(?:\.0+)?$/.test(value.trim());
}
