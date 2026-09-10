import { useEffect, useRef, useState } from "react";

import type { Candle, CandleInterval, DepthLevel } from "@/api/types";
import { realtimeTopics, type RealtimeMessage } from "@/features/realtime/topic-manager";

export interface LiveOrderBook {
  bids: DepthLevel[];
  asks: DepthLevel[];
  observedAt: string;
}

export interface LiveMarkPrice {
  instrumentId: string;
  price: string;
  observedAt: string;
  stale: boolean;
}

export interface LiveCandleSubscriptionValue {
  instrumentId: string;
  interval: CandleInterval;
  candle: Candle;
}

export const DEFAULT_MARK_STALE_AFTER_MS = 5_000;

export interface StreamEvent {
  /** Kept optional for the pure book reducer's legacy fixtures. */
  kind?: "order_book" | "candles" | "mark_price";
  instrumentId?: string;
  eventType: "snapshot" | "delta" | "full" | "update" | "resync_required";
  observedAt: string;
  bids?: DepthLevel[];
  asks?: DepthLevel[];
  candle?: Candle;
  price?: string;
  sourceSequence?: string;
  previous?: string;
}

/** Public venue streams terminate at Gateway and recover from native WS snapshots. */
export function usePublicMarketStreams(
  instrumentId: string | undefined,
  interval: CandleInterval,
  product?: string,
) {
  const [book, setBook] = useState<LiveOrderBook | null>(null);
  const [candleValue, setCandleValue] = useState<LiveCandleSubscriptionValue | null>(null);
  const [mark, setMark] = useState<LiveMarkPrice | null>(null);
  const bookSync = useRef(new OrderBookSynchronizer());

  useEffect(() => {
    setBook(null);
    setCandleValue(null);
    setMark(null);
    if (!instrumentId || typeof WebSocket === "undefined") return;
    bookSync.current = new OrderBookSynchronizer();

    let markTimer: ReturnType<typeof setTimeout> | undefined;
    const subscriptions = [
      realtimeTopics.subscribe({ topic: "market.order_book", instrumentId, depth: 100 }, (message) => {
        const event = bookEvent(message);
        if (event) {
          const result = bookSync.current.push(event);
          if (result.book !== undefined) setBook(result.book);
        } else if (message.type === "resync_required") {
          bookSync.current.reset();
          setBook(null);
        }
      }),
      realtimeTopics.subscribe({ topic: "market.candles", instrumentId, interval }, (message) => {
        const next = candleEvent(message);
        if (next) setCandleValue({ instrumentId, interval, candle: next });
        else if (message.type === "resync_required") setCandleValue(null);
      }),
    ];
    if (product?.toLowerCase().includes("perp")) {
      subscriptions.push(realtimeTopics.subscribe({ topic: "market.mark_price", instrumentId }, (message) => {
        const data = objectData(message);
        if ((message.type === "snapshot" || message.type === "delta") && typeof data?.price === "string" && data.price !== "") {
          const observedAt = message.observedAt ?? new Date().toISOString();
          if (markTimer) clearTimeout(markTimer);
          const delay = markStaleDelay(observedAt, data.staleAfterMs, data.ageMs);
          const stale = delay <= 0;
          setMark({ instrumentId, price: data.price, observedAt, stale });
          if (!stale) {
            markTimer = setTimeout(
              () => setMark((current) => current?.observedAt === observedAt ? { ...current, stale: true } : current),
              delay,
            );
          }
        } else if (message.type === "resync_required") setMark((current) => current ? { ...current, stale: true } : null);
      }));
    }
    return () => {
      if (markTimer) clearTimeout(markTimer);
      for (const unsubscribe of subscriptions) unsubscribe();
    };
  }, [instrumentId, interval, product]);

  return {
    book,
    candle: candleForSubscription(candleValue, instrumentId, interval),
    mark,
  };
}

/** Late callbacks from a released topic can arrive after an interval switch. */
export function candleForSubscription(
  value: LiveCandleSubscriptionValue | null,
  instrumentId: string | undefined,
  interval: CandleInterval,
): Candle | null {
  if (!value || value.instrumentId !== instrumentId || value.interval !== interval) return null;
  return value.candle;
}

/** The Gateway owns source cadence; the browser only evaluates its deadline. */
export function markStaleDelay(observedAt: string, staleAfterMs: unknown, receivedAgeMs?: unknown, now = Date.now()): number {
  const configured = typeof staleAfterMs === "number" && Number.isFinite(staleAfterMs) && staleAfterMs >= 1_000
    ? staleAfterMs
    : DEFAULT_MARK_STALE_AFTER_MS;
  // Gateway computes age with the same clock that receives venue data. This
  // uses its local receipt clock, avoiding both venue and browser clock skew
  // turning every fresh packet into a brief
  // fresh-then-stale flash. Older servers omit ageMs, so retain the timestamp
  // calculation only as a compatibility fallback.
  if (typeof receivedAgeMs === "number" && Number.isFinite(receivedAgeMs) && receivedAgeMs >= 0) {
    return Math.max(0, configured - receivedAgeMs);
  }
  const observed = Date.parse(observedAt);
  const deadline = Number.isFinite(observed) ? observed + configured : now + configured;
  return Math.max(0, deadline - now);
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
  if (message.type !== "snapshot" && message.type !== "delta" && message.type !== "full") return null;
  const data = objectData(message);
  if (!data || !Array.isArray(data.bids) || !Array.isArray(data.asks)) return null;
  return {
    eventType: message.type,
    observedAt: message.observedAt ?? new Date().toISOString(),
    bids: data.bids as DepthLevel[],
    asks: data.asks as DepthLevel[],
    sourceSequence: message.sourceSequence,
    previous: typeof data.previous === "string" ? data.previous : undefined,
  };
}

export interface BookSyncResult {
  book?: LiveOrderBook | null;
  needsSnapshot: boolean;
}

/**
 * Rebuild depth in the browser from a WS snapshot and native deltas. A full
 * event is a later self-contained replacement; no REST book is ever joined to
 * a different WebSocket sequence.
 */
export class OrderBookSynchronizer {
  private book: LiveOrderBook | null = null;
  private sourceSequence = "";
  private waitingForSnapshot = true;
  private pending: StreamEvent[] = [];

  reset(): void {
    this.book = null;
    this.sourceSequence = "";
    this.waitingForSnapshot = true;
    this.pending = [];
  }

  seed(snapshot: StreamEvent): BookSyncResult {
    if (snapshot.eventType !== "snapshot") return { needsSnapshot: true };
    this.book = applyBookEvent(null, snapshot);
    if (!this.book) return this.fail();
    this.sourceSequence = snapshot.sourceSequence ?? "";
    this.waitingForSnapshot = false;
    return this.drain();
  }

  push(event: StreamEvent): BookSyncResult {
    if (event.eventType === "snapshot") return this.seed(event);
    if (event.eventType === "full") {
      this.book = applyBookEvent(null, event);
      if (!this.book) return this.fail();
      this.sourceSequence = event.sourceSequence ?? "";
      this.waitingForSnapshot = false;
      this.pending = [];
      return { book: this.book, needsSnapshot: false };
    }
    if (event.eventType !== "delta") return { needsSnapshot: false };
    if (this.waitingForSnapshot) {
      this.pending.push(event);
      if (this.pending.length > 2_000) return this.fail();
      return { needsSnapshot: false };
    }
    return this.applyDelta(event);
  }

  private drain(): BookSyncResult {
    const pending = this.pending;
    this.pending = [];
    for (const event of pending) {
      const result = this.applyDelta(event);
      if (result.needsSnapshot) return result;
    }
    return { book: this.book, needsSnapshot: false };
  }

  private applyDelta(event: StreamEvent): BookSyncResult {
    if (!this.book) return this.fail();
    const incoming = event.sourceSequence ?? "";
    if (this.sourceSequence && incoming) {
      const order = compareIntegerSequences(incoming, this.sourceSequence);
      if (incoming === this.sourceSequence || (order !== null && order <= 0)) {
        return { book: this.book, needsSnapshot: false };
      }
      // Some venues expose only a monotonic sequence. When a predecessor is
      // available it must join exactly; otherwise the adapter/Hub can only
      // guarantee monotonicity and a later native snapshot resets the book.
      if (event.previous && event.previous !== this.sourceSequence) return this.fail();
    }
    const next = applyBookEvent(this.book, event);
    if (!next) return this.fail();
    this.book = next;
    if (incoming) this.sourceSequence = incoming;
    return { book: this.book, needsSnapshot: false };
  }

  private fail(): BookSyncResult {
    this.reset();
    return { book: null, needsSnapshot: true };
  }
}

function compareIntegerSequences(left: string, right: string): number | null {
  if (!/^\d+$/.test(left) || !/^\d+$/.test(right)) return null;
  const a = BigInt(left);
  const b = BigInt(right);
  return a < b ? -1 : a > b ? 1 : 0;
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
  if (event.eventType === "snapshot" || event.eventType === "full" || current === null) {
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
