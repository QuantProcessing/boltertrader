import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { accountData, manual, trading } from "@/api/endpoints";
import type { AccountBalance, AccountPositionSnapshot, Command, Fill, ManualPreview, ManualPreviewRequest, Position } from "@/api/types";
import { isResumable } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";

/* ---------------------------------------------------------------------------
 * The order path, and the ledgers it moves.
 *
 * Placing is two calls, not one, and that is a property of the server rather
 * than a UI flourish: `POST execution-previews` prices and validates the intent
 * and hands back a token with a 30-second deadline sealed inside it, and
 * `POST commands` is the only thing that will act on such a token. There is no
 * single-shot route. The ticket is built around that shape instead of hiding it
 * — an operator who cannot see what they are about to send has not confirmed
 * anything.
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

let realtimeConnected = false;

export function setTradeRealtimeConnected(connected: boolean): void {
  realtimeConnected = connected;
}

export function privateFallbackInterval(): number | false {
  return realtimeConnected ? false : 5_000;
}

/**
 * All three ledgers below belong to a principal. Account events invalidate
 * their REST snapshots; polling is enabled only while realtime is unavailable.
 *
 * `enabled` is what keeps a read-only visitor from asking for them. Not because
 * the answer would leak anything — the server refuses an anonymous read of
 * someone's orders whatever the browser does — but because the alternative is a
 * 401 every five seconds, forever, and a blotter whose three tabs all say
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
    // The multiplexed account topic is the prompt path. Polling is only the
    // continuity fallback, and tightens while an order can still change state.
    refetchInterval: (query) =>
      realtimeConnected ? false : query.state.data?.some((order) => isResumable(order.state)) ? 2_000 : 15_000,
    refetchOnWindowFocus: "always",
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
    queryFn: ({ signal }) => accountData.balances(accountId, signal),
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

/** A sealed preview, plus the moment its clock started. */
export interface SealedPreview {
  preview: ManualPreview;
  request: ManualPreviewRequest;
  /** `Date.now()` when the response landed — the deadline is not in the body. */
  sealedAt: number;
}

export function usePreviewOrder() {
  return useMutation({
    mutationFn: async (request: ManualPreviewRequest): Promise<SealedPreview> => {
      const preview = await manual.preview(request);
      return { preview, request, sealedAt: Date.now() };
    },
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

export function usePlaceOrder() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (previewId: string): Promise<Command> =>
      manual.command({
        previewId,
        action: "place",
        // A new lineage per order: this one is not a follow-up to anything.
        lineageId: freshId(),
        orderId: "",
        idempotencyKey: freshId(),
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
        previewId: "",
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
		queryClient.invalidateQueries({ queryKey: ["arbitrage", "positions"] }),
	]);
}
