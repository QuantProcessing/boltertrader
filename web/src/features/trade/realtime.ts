import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import { realtimeTopics } from "@/features/realtime/topic-manager";

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
	"account.order",
	"account.fill",
	"account.balance",
	"account.account_health",
] as const;

export function realtimeEventMovesPositions(kind: string): boolean {
  return kind === "fill.recorded" || kind === "command.filled" || kind === "account.fill" || kind.startsWith("portfolio.position.") || kind === "portfolio.account.state";
}

/**
 * Account events are change notifications, not a second client-side source of truth.
 * Every event causes a fresh authoritative read; polling remains a fallback if
 * a proxy or browser temporarily drops the stream.
 */
export function useTradeRealtime(enabled: boolean, cursor?: string): void {
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!enabled) return;
    let alive = true;
    let snapshotReady = false;
    let queuedEvent = false;
    let snapshotGeneration = 0;
    const resnapshot = async () => {
      const generation = ++snapshotGeneration;
      snapshotReady = false;
      setTradeRealtimeConnected(false);
      await invalidateTradeLedgers(queryClient);
      if (!alive || generation !== snapshotGeneration) return;
      snapshotReady = true;
      setTradeRealtimeConnected(true);
      if (queuedEvent) {
        queuedEvent = false;
        await invalidateTradeLedgers(queryClient);
      }
    };
    const unsubscribe = realtimeTopics.subscribe({ topic: "account.events", cursor }, (message) => {
      if (message.type === "checkpoint") {
		void resnapshot();
        return;
      }
      if (message.type === "resync_required" || message.type === "error") {
		++snapshotGeneration;
		snapshotReady = false;
		queuedEvent = false;
        setTradeRealtimeConnected(false);
		void invalidateTradeLedgers(queryClient);
        return;
      }
      if (message.type !== "delta" || typeof message.data !== "object" || message.data === null) return;
      const kind = (message.data as { kind?: unknown }).kind;
      if (typeof kind !== "string" || !TRADE_REALTIME_EVENTS.includes(kind as (typeof TRADE_REALTIME_EVENTS)[number])) return;
		if (!snapshotReady) {
			queuedEvent = true;
			return;
		}
		void queryClient.invalidateQueries({ queryKey: tradeKeys.orders() });
      void queryClient.invalidateQueries({ queryKey: ["trading", "balances"] });
		if (realtimeEventMovesPositions(kind)) void invalidateTradeLedgers(queryClient);
    });

    return () => {
		alive = false;
		++snapshotGeneration;
      unsubscribe();
      setTradeRealtimeConnected(false);
    };
  }, [cursor, enabled, queryClient]);
}
