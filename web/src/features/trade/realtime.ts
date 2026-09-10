import { useQueryClient } from "@tanstack/react-query";
import { useEffect } from "react";

import type {
  AccountBalance,
  AccountPositionSnapshot,
  Command,
  Fill,
  LedgerEntry,
  ActivityEvent,
  AccountRuntime,
} from "@/api/types";
import { realtimeTopics } from "@/features/realtime/topic-manager";

import {
  applyBalanceOverlays,
  applyPositionOverlays,
  recordAccountRealtimeEvent,
} from "./account-realtime-overlay";
import { mergeCommands, mergeFills } from "./order-snapshot";
import {
  recordLedgerSnapshot,
  selectLedger,
  selectActivity,
} from "./ledger-snapshot";
import { tradeKeys } from "./queries";

export const TRADE_REALTIME_EVENTS = [
  "account.balance",
  "account.position",
] as const;

/** One shell subscription keeps private read models current on every route. */
export function useTradeRealtime(enabled: boolean): void {
  const queryClient = useQueryClient();

  useEffect(() => {
    if (!enabled) return;
    const unsubscribe = realtimeTopics.subscribe(
      { topic: "account.events" },
      (message) => {
        if (
          message.type !== "delta" ||
          typeof message.data !== "object" ||
          message.data === null
        )
          return;
        const kind = (message.data as { kind?: unknown }).kind;
        if (kind === "account.account_connection") {
          const event = message.data as {
            accountId: string;
            payload?: { state?: string };
          };
          if (
            event.payload?.state === "connected" ||
            event.payload?.state === "disconnected"
          )
            queryClient.setQueriesData<AccountRuntime[]>(
              { queryKey: ["trading", "account-runtimes", event.accountId] },
              (items) =>
                items?.map((item) => ({
                  ...item,
                  connected: event.payload!.state === "connected",
                })),
            );
        }
        if (
          typeof kind !== "string" ||
          !TRADE_REALTIME_EVENTS.includes(
            kind as (typeof TRADE_REALTIME_EVENTS)[number],
          )
        )
          return;
        const resource = recordAccountRealtimeEvent(message.data);
        if (resource === "balance") {
          for (const query of queryClient
            .getQueryCache()
            .findAll({ queryKey: ["trading", "balances"] })) {
            const accountId = query.queryKey[2];
            if (typeof accountId !== "string") continue;
            queryClient.setQueryData<AccountBalance[]>(
              query.queryKey,
              (current) =>
                current ? applyBalanceOverlays(current, accountId) : current,
            );
          }
        }
        if (resource === "position") {
          for (const query of queryClient
            .getQueryCache()
            .findAll({ queryKey: ["trading", "account-positions"] })) {
            const [, , accountId, instrumentId] =
              query.queryKey as readonly unknown[];
            queryClient.setQueryData<AccountPositionSnapshot>(
              query.queryKey,
              (current) =>
                current
                  ? applyPositionOverlays(current, {
                      accountId:
                        typeof accountId === "string" ? accountId : undefined,
                      instrumentId:
                        typeof instrumentId === "string"
                          ? instrumentId
                          : undefined,
                    })
                  : current,
            );
          }
          for (const query of queryClient
            .getQueryCache()
            .findAll({ queryKey: ["trading", "portfolio-positions"] })) {
            const filter = query.queryKey[2];
            queryClient.setQueryData<AccountPositionSnapshot>(
              query.queryKey,
              (current) =>
                current
                  ? applyPositionOverlays(
                      current,
                      typeof filter === "object" && filter !== null
                        ? (filter as {
                            accountId?: string;
                            venue?: string;
                            product?: string;
                          })
                        : {},
                    )
                  : current,
            );
          }
        }
      },
    );

    let disconnected = false;
    const connection = realtimeTopics.watchConnection((state) => {
      if (state === "reconnecting") disconnected = true;
      if (state === "online" && disconnected) {
        disconnected = false;
        for (const key of [
          "balances",
          "account-positions",
          "portfolio-positions",
        ])
          void queryClient.invalidateQueries({ queryKey: ["trading", key] });
      }
    });
    const orders = realtimeTopics.subscribe(
      { topic: "trading.orders" },
      (message) => {
        if (
          message.type !== "snapshot" ||
          !message.data ||
          typeof message.data !== "object"
        )
          return;
        const data = message.data as {
          orders?: Command[];
          fills?: Fill[];
          ledger?: LedgerEntry[];
          activity?: ActivityEvent[];
        };
        if (!Array.isArray(data.orders) || !Array.isArray(data.fills)) return;
        queryClient.setQueryData(
          tradeKeys.orders(),
          mergeCommands(data.orders),
        );
        queryClient.setQueryData(tradeKeys.fills(), mergeFills(data.fills));
        recordLedgerSnapshot(data);
        for (const query of queryClient
          .getQueryCache()
          .findAll({ queryKey: ["trading", "ledger"] }))
          queryClient.setQueryData<LedgerEntry[]>(query.queryKey, (current) =>
            selectLedger(
              current ?? [],
              query.queryKey[2] as Record<string, string>,
            ),
          );
        for (const query of queryClient
          .getQueryCache()
          .findAll({ queryKey: ["trading", "activity"] }))
          queryClient.setQueryData<ActivityEvent[]>(query.queryKey, (current) =>
            selectActivity(
              current ?? [],
              query.queryKey[2] as Record<string, string>,
            ),
          );
      },
    );
    return () => {
      unsubscribe();
      orders();
      connection();
    };
  }, [enabled, queryClient]);
}
