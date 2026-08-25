import { useEffect, useState } from "react";

import type { Candle, CandleInterval, DepthLevel } from "@/api/types";
import { realtimeTopics, type RealtimeMessage } from "@/features/realtime/topic-manager";

export interface LiveOrderBook {
  bids: DepthLevel[];
  asks: DepthLevel[];
  observedAt: string;
}

export interface LiveMarkPrice {
  price: string;
  observedAt: string;
  stale: boolean;
}

interface StreamEvent {
  /** Kept optional for the pure book reducer's legacy fixtures. */
  kind?: "order_book" | "candles" | "mark_price";
  instrumentId?: string;
  eventType: "snapshot" | "delta" | "update" | "resync_required";
  observedAt: string;
  bids?: DepthLevel[];
  asks?: DepthLevel[];
  candle?: Candle;
  price?: string;
}

/** One public venue stream per resource, both carried through the same origin. */
export function usePublicMarketStreams(instrumentId: string | undefined, interval: CandleInterval, product?: string) {
  const [book, setBook] = useState<LiveOrderBook | null>(null);
  const [candle, setCandle] = useState<Candle | null>(null);
  const [mark, setMark] = useState<LiveMarkPrice | null>(null);

  useEffect(() => {
    setBook(null);
    setCandle(null);
    setMark(null);
    if (!instrumentId || typeof WebSocket === "undefined") return;

    let markTimer: ReturnType<typeof setTimeout> | undefined;
    const subscriptions = [
      realtimeTopics.subscribe({ topic: "market.order_book", instrumentId, depth: 100 }, (message) => {
        const event = bookEvent(message);
        if (event) setBook((current) => applyBookEvent(current, event));
        else if (message.type === "resync_required") setBook(null);
      }),
      realtimeTopics.subscribe({ topic: "market.candles", instrumentId, interval }, (message) => {
        const next = candleEvent(message);
        if (next) setCandle(next);
        else if (message.type === "resync_required") setCandle(null);
      }),
    ];
    if (product?.toLowerCase().includes("perp")) {
      subscriptions.push(realtimeTopics.subscribe({ topic: "market.mark_price", instrumentId }, (message) => {
        const data = objectData(message);
        if ((message.type === "snapshot" || message.type === "delta") && typeof data?.price === "string" && data.price !== "") {
          const observedAt = message.observedAt ?? new Date().toISOString();
          setMark({ price: data.price, observedAt, stale: false });
          if (markTimer) clearTimeout(markTimer);
          const delay = Math.max(0, Date.parse(observedAt) + 2_000 - Date.now());
          markTimer = setTimeout(() => setMark((current) => current?.observedAt === observedAt ? { ...current, stale: true } : current), delay);
        } else if (message.type === "resync_required") setMark((current) => current ? { ...current, stale: true } : null);
      }));
    }
    return () => {
      if (markTimer) clearTimeout(markTimer);
      for (const unsubscribe of subscriptions) unsubscribe();
    };
  }, [instrumentId, interval, product]);

  return { book, candle, mark };
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

function objectData(message: RealtimeMessage): Record<string, unknown> | null {
  return typeof message.data === "object" && message.data !== null ? message.data as Record<string, unknown> : null;
}

function bookEvent(message: RealtimeMessage): StreamEvent | null {
  if (message.type !== "snapshot" && message.type !== "delta") return null;
  const data = objectData(message);
  if (!data || !Array.isArray(data.bids) || !Array.isArray(data.asks)) return null;
  return {
    eventType: message.type,
    observedAt: message.observedAt ?? new Date().toISOString(),
    bids: data.bids as DepthLevel[],
    asks: data.asks as DepthLevel[],
  };
}

function candleEvent(message: RealtimeMessage): Candle | null {
  if (message.type !== "snapshot" && message.type !== "delta") return null;
  const data = objectData(message);
  if (message.type === "snapshot" && Array.isArray(data?.candles)) {
    return (data.candles as Candle[]).at(-1) ?? null;
  }
  return data?.candle && typeof data.candle === "object" ? data.candle as Candle : null;
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
