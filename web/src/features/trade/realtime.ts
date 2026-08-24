import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { paths } from "@/api/endpoints";

import { invalidateTradeLedgers, tradeKeys } from "./queries";

export const TRADE_REALTIME_EVENTS = [
  "command.accepted",
  "command.submitted",
  "command.uncertain",
  "command.filled",
  "command.canceled",
  "command.failed",
  "command.cancel_accepted",
  "fill.recorded",
] as const;

export function realtimeEventMovesPositions(kind: string): boolean {
  return kind === "fill.recorded" || kind === "command.filled";
}

/**
 * SSE is a change notification, not a second client-side source of truth.
 * Every event causes a fresh authoritative read; polling remains a fallback if
 * a proxy or browser temporarily drops the stream.
 */
export function useTradeRealtime(enabled: boolean): void {
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const source = new EventSource(paths.realtimeStream);
    const listeners = new Map<string, EventListener>();

    for (const kind of TRADE_REALTIME_EVENTS) {
      const listener = () => {
        void queryClient.invalidateQueries({ queryKey: tradeKeys.orders() });
        // Reservations and venue margin can move before a fill exists.
        void queryClient.invalidateQueries({ queryKey: ["trading", "balances"] });
        if (realtimeEventMovesPositions(kind)) invalidateTradeLedgers(queryClient);
      };
      listeners.set(kind, listener);
      source.addEventListener(kind, listener);
    }

    // Opening includes reconnecting after sleep. Resnapshot immediately so an
    // expired replay cursor cannot leave the tab showing yesterday's rows.
    const onOpen = () => invalidateTradeLedgers(queryClient);
    source.addEventListener("open", onOpen);

    return () => {
      source.removeEventListener("open", onOpen);
      for (const [kind, listener] of listeners) source.removeEventListener(kind, listener);
      source.close();
    };
  }, [enabled, queryClient]);
}
