import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { accountData, manual, trading } from "@/api/endpoints";
import type { AccountBalance, AccountPositionSnapshot, Command, Fill, ManualOrderRequest, Position } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";

import { applyBalanceOverlays, applyOrderOverlays, applyPositionOverlays } from "./account-realtime-overlay";

/* ---------------------------------------------------------------------------
 * The order path, and the ledgers it moves.
 *
 * Placing is one command. The browser may show a local confirmation using the
 * catalog, position and public prices already on screen, while Trading checks
 * the current account, route, runtime fence and venue rules in the same request.
 *
 * Both `lineageId` and `idempotencyKey` are minted here rather than by the
 * server. The key is what makes a retry safe (a repeat returns 200 with the
 * original command instead of placing a second order), so it must be stable for
 * one intent and different for the next.
 * ------------------------------------------------------------------------- */

export const tradeKeys = {
  orders: () => ["manual", "orders"] as const,
  positions: () => ["trading", "positions"] as const,
  balances: (accountId: string) => ["trading", "balances", accountId] as const,
  accountPositions: (accountId: string, instrumentId: string) => ["trading", "account-positions", accountId, instrumentId] as const,
  fills: () => ["trading", "fills"] as const,
  portfolioPositions: () => ["trading", "portfolio-positions"] as const,
};

export function privateFallbackInterval(): number | false {
  // Reconciliation is deliberately periodic rather than event-triggered.
  // Private WebSocket events update the visible entities in-place; this REST
  // read repairs missed/de-duplicated events without coupling stream reconnects
  // to request bursts.
  return 60_000;
}

/**
 * All three ledgers below belong to a principal. Account events update their
 * client projections in-place; REST reconciles every 60 seconds regardless of
 * stream state.
 *
 * `enabled` is what keeps a read-only visitor from asking for them. Not because
 * the answer would leak anything — the server refuses an anonymous read of
 * someone's orders whatever the browser does — but because the alternative is a
 * 401 every minute, forever, and a blotter whose three tabs all say
 * something went wrong when nothing did.
 *
 * The gate is here rather than at the call sites so that a fourth ledger added
 * later inherits it by sitting next to these, and so no page has to remember
 * which of the hooks it calls are private.
 */
export function useOrders(): UseQueryResult<Command[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.orders(),
    queryFn: ({ signal }) => manual.orders(signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    refetchOnWindowFocus: "always",
    select: applyOrderOverlays,
  });
}

export function usePositions(): UseQueryResult<Position[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.positions(),
    queryFn: ({ signal }) => trading.positions({}, signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    refetchOnWindowFocus: "always",
  });
}

export function useAccountBalances(accountId: string): UseQueryResult<AccountBalance[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.balances(accountId),
    queryFn: async ({ signal }) => {
      const startedAt = new Date().toISOString();
      const balances = await accountData.balances(accountId, signal);
      return applyBalanceOverlays(balances, accountId, startedAt);
    },
    enabled: !readOnly && accountId !== "",
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    refetchOnWindowFocus: "always",
  });
}

export function useAccountPositions(accountId: string, instrumentId: string): UseQueryResult<AccountPositionSnapshot> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.accountPositions(accountId, instrumentId),
    queryFn: ({ signal }) => accountData.positions(accountId, instrumentId, signal),
    enabled: !readOnly && accountId !== "" && instrumentId !== "",
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    refetchOnWindowFocus: "always",
    select: (snapshot) => applyPositionOverlays(snapshot, { accountId, instrumentId }),
  });
}

export function useFills(): UseQueryResult<Fill[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.fills(),
    queryFn: ({ signal }) => trading.fills({}, signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    refetchOnWindowFocus: "always",
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

export function usePlaceOrder() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (order: ManualOrderRequest): Promise<Command> =>
      manual.command({
        action: "place",
        // A new lineage per order: this one is not a follow-up to anything.
        lineageId: freshId(),
        orderId: "",
        idempotencyKey: freshId(),
        order,
      }),
    onSuccess: () => invalidateTradeLedgers(queryClient),
  });
}

/**
 * Cancelling names both the command and its lineage; the server checks that
 * they agree and answers 404 if they do not, which is what stops one session
 * cancelling an order it is only half-looking at.
 */
export function useCancelOrder() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (order: Command): Promise<Command> =>
      manual.command({
        action: "cancel",
        lineageId: order.lineageId,
        orderId: order.commandId,
        idempotencyKey: freshId(),
      }),
    onSuccess: () => invalidateTradeLedgers(queryClient),
  });
}

export async function invalidateTradeLedgers(queryClient: ReturnType<typeof useQueryClient>): Promise<void> {
	await Promise.all([
		queryClient.invalidateQueries({ queryKey: tradeKeys.orders() }),
		queryClient.invalidateQueries({ queryKey: tradeKeys.positions() }),
		queryClient.invalidateQueries({ queryKey: ["trading", "account-positions"] }),
		queryClient.invalidateQueries({ queryKey: tradeKeys.portfolioPositions() }),
		queryClient.invalidateQueries({ queryKey: ["trading", "balances"] }),
		queryClient.invalidateQueries({ queryKey: tradeKeys.fills() }),
		queryClient.invalidateQueries({ queryKey: ["trading", "ledger"] }),
		queryClient.invalidateQueries({ queryKey: ["trading", "pnl"] }),
	]);
}
