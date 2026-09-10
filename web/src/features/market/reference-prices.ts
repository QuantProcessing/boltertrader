import { useEffect, useState } from "react";
import { realtimeTopics } from "@/features/realtime/topic-manager";
import {
  referenceStaleDelay,
  type LiveReferencePrice,
} from "./reference-freshness";

/** Logical subscriptions share the page socket and retain the last valid quote. */
export function useReferencePrices(
  instrumentIds: string[],
  kind: "index" | "mark" = "index",
): Record<string, LiveReferencePrice> {
  const key = [...new Set(instrumentIds.filter(Boolean))].sort().join("\n");
  const [prices, setPrices] = useState<Record<string, LiveReferencePrice>>({});
  useEffect(() => {
    const ids = key ? key.split("\n") : [];
    setPrices((current) =>
      Object.fromEntries(
        Object.entries(current).filter(([id]) => ids.includes(id)),
      ),
    );
    const timers = new Map<string, ReturnType<typeof setTimeout>>();
    const stale = (id: string) =>
      setPrices((current) =>
        current[id]
          ? { ...current, [id]: { ...current[id], stale: true } }
          : current,
      );
    const releases = ids.map((instrumentId) =>
      realtimeTopics.subscribe(
        {
          topic: kind === "index" ? "market.index_price" : "market.mark_price",
          instrumentId,
        },
        (message) => {
          if (message.type === "resync_required" || message.type === "error") {
            stale(instrumentId);
            return;
          }
          const data = message.data as
            | (Partial<LiveReferencePrice> & {
                staleAfterMs?: number;
                ageMs?: number;
              })
            | undefined;
          if (
            (message.type !== "snapshot" &&
              message.type !== "delta" &&
              message.type !== "full") ||
            !data ||
            typeof data.price !== "string" ||
            !/^\d+(?:\.\d+)?$/.test(data.price) ||
            Number(data.price) <= 0
          )
            return;
          const observedAt = message.observedAt ?? new Date().toISOString();
          const delay = referenceStaleDelay(
            observedAt,
            data.staleAfterMs,
            data.ageMs,
          );
          clearTimeout(timers.get(instrumentId));
          setPrices((current) => ({
            ...current,
            [instrumentId]: {
              fundingRate: data.fundingRate,
              openInterest: data.openInterest,
              fundingObservedAt: data.fundingObservedAt,
              openInterestObservedAt: data.openInterestObservedAt,
              nextFundingTime: data.nextFundingTime,
              instrumentId,
              price: data.price!,
              observedAt,
              stale: delay <= 0,
            },
          }));
          if (delay > 0)
            timers.set(
              instrumentId,
              setTimeout(() => stale(instrumentId), delay),
            );
        },
      ),
    );
    const connection = realtimeTopics.watchConnection((state) => {
      if (state === "reconnecting" || state === "idle") ids.forEach(stale);
    });
    return () => {
      releases.forEach((release) => release());
      connection();
      timers.forEach(clearTimeout);
    };
  }, [key, kind]);
  return prices;
}
