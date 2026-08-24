import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { paths } from "@/api/endpoints";

import { invalidateTradeLedgers, setTradeRealtimeConnected, tradeKeys } from "./queries";

export const TRADE_REALTIME_EVENTS = [
  "command.accepted",
  "command.submitted",
  "command.uncertain",
  "command.filled",
  "command.canceled",
  "command.failed",
  "command.cancel_accepted",
  "fill.recorded",
  "portfolio.position.upsert",
  "portfolio.position.remove",
  "portfolio.account.state",
] as const;

export function realtimeEventMovesPositions(kind: string): boolean {
  return kind === "fill.recorded" || kind === "command.filled" || kind.startsWith("portfolio.position.") || kind === "portfolio.account.state";
}

/**
 * SSE is a change notification, not a second client-side source of truth.
 * Every event causes a fresh authoritative read; polling remains a fallback if
 * a proxy or browser temporarily drops the stream.
 */
export function useTradeRealtime(enabled: boolean, cursor?: string): void {
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!enabled || typeof EventSource === "undefined") return;
    const url = cursor ? `${paths.realtimeStream}?cursor=${encodeURIComponent(cursor)}` : paths.realtimeStream;
    const source = new EventSource(url);
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
    const onOpen = () => {
      setTradeRealtimeConnected(true);
      invalidateTradeLedgers(queryClient);
    };
    const onError = () => {
      setTradeRealtimeConnected(false);
      invalidateTradeLedgers(queryClient);
    };
    source.addEventListener("open", onOpen);
    source.addEventListener("error", onError);

    return () => {
      source.removeEventListener("open", onOpen);
      source.removeEventListener("error", onError);
      for (const [kind, listener] of listeners) source.removeEventListener(kind, listener);
      source.close();
      setTradeRealtimeConnected(false);
    };
  }, [cursor, enabled, queryClient]);
}
