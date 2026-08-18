import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { manual, trading } from "@/api/endpoints";
import type { Command, Fill, ManualPreview, ManualPreviewRequest, Position } from "@/api/types";
import { useReadOnly } from "@/app/mode";

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
  fills: () => ["trading", "fills"] as const,
};

/**
 * All three ledgers below belong to a principal, and all three poll.
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
    // Working orders change when the venue says so, and the reconciler is what
    // hears it. Five seconds is how long a fill can sit unseen.
    refetchInterval: 5_000,
  });
}

export function usePositions(): UseQueryResult<Position[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.positions(),
    queryFn: ({ signal }) => trading.positions({}, signal),
    enabled: !readOnly,
    refetchInterval: 5_000,
  });
}

export function useFills(): UseQueryResult<Fill[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: tradeKeys.fills(),
    queryFn: ({ signal }) => trading.fills({}, signal),
    enabled: !readOnly,
    refetchInterval: 10_000,
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
    onSuccess: () => invalidateLedgers(queryClient),
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
    onSuccess: () => invalidateLedgers(queryClient),
  });
}

function invalidateLedgers(queryClient: ReturnType<typeof useQueryClient>): void {
  void queryClient.invalidateQueries({ queryKey: tradeKeys.orders() });
  void queryClient.invalidateQueries({ queryKey: tradeKeys.positions() });
  void queryClient.invalidateQueries({ queryKey: tradeKeys.fills() });
}
