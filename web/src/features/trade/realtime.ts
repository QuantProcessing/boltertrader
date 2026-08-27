import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import type { AccountPositionSnapshot, Command } from "@/api/types";
import { realtimeTopics } from "@/features/realtime/topic-manager";

import { applyOrderOverlays, applyPositionOverlays, recordAccountRealtimeEvent } from "./account-realtime-overlay";
import { tradeKeys } from "./queries";

export const TRADE_REALTIME_EVENTS = [
  "account.order",
  "account.fill",
  "account.balance",
  "account.position",
  "account.account_health",
] as const;

/**
 * Gateway account events overlay newer entity facts onto the current REST
 * snapshot. They never trigger REST. The query layer reconciles on a fixed
 * 60-second cadence and the overlay compares source timestamps plus entity
 * identity so an older in-flight REST response cannot roll state backward.
 */
export function useTradeRealtime(enabled: boolean): void {
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!enabled) return;
    const unsubscribe = realtimeTopics.subscribe({ topic: "account.events" }, (message) => {
      if (message.type !== "delta" || typeof message.data !== "object" || message.data === null) return;
      const kind = (message.data as { kind?: unknown }).kind;
      if (typeof kind !== "string" || !TRADE_REALTIME_EVENTS.includes(kind as (typeof TRADE_REALTIME_EVENTS)[number])) return;
      const resource = recordAccountRealtimeEvent(message.data);
      if (resource === "order") {
        queryClient.setQueryData<Command[]>(tradeKeys.orders(), (current) => current ? applyOrderOverlays(current) : current);
      }
      if (resource === "position") {
        for (const query of queryClient.getQueryCache().findAll({ queryKey: ["trading", "account-positions"] })) {
          const [, , accountId, instrumentId] = query.queryKey as readonly unknown[];
          queryClient.setQueryData<AccountPositionSnapshot>(query.queryKey, (current) => current ? applyPositionOverlays(current, {
            accountId: typeof accountId === "string" ? accountId : undefined,
            instrumentId: typeof instrumentId === "string" ? instrumentId : undefined,
          }) : current);
        }
        for (const query of queryClient.getQueryCache().findAll({ queryKey: ["trading", "portfolio-positions"] })) {
          const filter = query.queryKey[2];
          queryClient.setQueryData<AccountPositionSnapshot>(query.queryKey, (current) => current ? applyPositionOverlays(
            current,
            typeof filter === "object" && filter !== null ? filter as { accountId?: string; venue?: string; product?: string } : {},
          ) : current);
        }
      }
    });

    return unsubscribe;
  }, [enabled, queryClient]);
}
