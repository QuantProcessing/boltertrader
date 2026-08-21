import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import {
  arbitrage,
  funding,
  type ArbitrageCancelRequest,
  type ArbitrageCloseRequest,
  type Metered,
} from "@/api/endpoints";
import type {
  ArbitrageExecution,
  ArbitrageOpportunity,
  ArbitragePair,
  ArbitragePreview,
  ArbitragePreviewRequest,
  Command,
  FundingBoardSnapshot,
  FundingHistoryQuery,
  FundingHistorySeries,
} from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";
import { tradeKeys } from "@/features/trade/queries";

/* ---------------------------------------------------------------------------
 * The funding board's read, and the hedge built on top of it.
 *
 * The board is the only market read on this surface that needs no account, no
 * instrument and no query at all — which is exactly what makes it the first
 * useful screen on a fresh install. Its key is a constant for the same reason:
 * there is one board, and every visitor asks for it identically.
 *
 * The arbitrage reads below are the opposite: what they can offer depends on
 * which accounts the session holds, so the account ids are part of the key.
 * ------------------------------------------------------------------------- */

export const fundingKeys = {
  board: () => ["funding", "board"] as const,
  history: (query: FundingHistoryQuery | null) => ["funding", "history", query] as const,
  opportunities: (accountIds: string[]) => ["arbitrage", "opportunities", accountIds] as const,
  positions: () => ["arbitrage", "positions"] as const,
};

/**
 * The cross-venue matrix, re-read from Trading's resident projection every five
 * seconds. This never reaches a venue: Gateway collects once, Trading consumes
 * its internal stream, and browser requests only copy local state. Countdowns
 * tick locally off `nextFundingTime` rather than off the poll.
 */
export function useFundingBoard(): UseQueryResult<FundingBoardSnapshot> {
  return useQuery({
    queryKey: fundingKeys.board(),
    queryFn: ({ signal }) => funding.board(signal),
    refetchInterval: 5_000,
    // Keep the matrix on screen through a refetch. Blanking a table this wide
    // reads as "the venues went away", which is the one thing the stale and
    // unavailable markers exist to say precisely instead of by implication.
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
}

/**
 * One leg's settled funding over a named window.
 *
 * A pair page runs two of these, one per venue, and they are separate queries
 * rather than one paired read for a reason that shows on the screen: a venue
 * that publishes no history answers a permanent 404, and its leg has to be able
 * to say so beside a leg that drew. Coupling them would make the missing half
 * take the whole chart down.
 *
 * No `refetchInterval`. Every point on this curve has already settled, so it
 * only changes when the venue settles again — hourly at the fastest — and the
 * charge falls on that venue's own allowance, the same one the order ticket's
 * quotes draw from. Half an hour of staleness costs nothing and a poll would
 * cost the ticket.
 */
export function useFundingHistory(
  query: FundingHistoryQuery | null,
): UseQueryResult<Metered<FundingHistorySeries>> {
  return useQuery({
    queryKey: fundingKeys.history(query),
    queryFn: ({ signal }) => funding.history(query as FundingHistoryQuery, signal),
    enabled: query !== null,
    staleTime: 30 * 60_000,
  });
}

/* --- Arbitrage ------------------------------------------------------------ */

/**
 * The ranked cross-venue spreads.
 *
 * `accountIds` is what turns a reading into an offer: without it every pair is
 * still ranked, but none is executable, because the server has not been told
 * which venues this operator can actually reach. It is in the query key rather
 * than closed over, so adding an account re-ranks rather than serving the
 * account-less list from cache.
 *
 * Five seconds matches the product read cadence. Both this route and the board
 * route are local Trading reads, so polling does not multiply venue traffic.
 */
export function useArbitrageOpportunities(
  accountIds: string[],
): UseQueryResult<{ items: ArbitrageOpportunity[]; observedAt: string }> {
  return useQuery({
    queryKey: fundingKeys.opportunities(accountIds),
    queryFn: ({ signal }) => arbitrage.opportunities(accountIds, signal),
    refetchInterval: 5_000,
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
}

/**
 * Open pairs, both legs each.
 *
 * Five seconds, the same beat as the manual order ledger, because these are the
 * same commands moved by the same reconciler — a leg that fills is a leg this
 * poll is the only thing watching. It matters more here than there: a pair that
 * lands in `partially_open` is carrying directional exposure nobody chose, and
 * the sooner it is on screen the sooner it can be undone.
 */
export function useArbitragePositions(): UseQueryResult<ArbitragePair[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: fundingKeys.positions(),
    queryFn: ({ signal }) => arbitrage.positions(signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 5_000,
  });
}

/** A sealed arbitrage preview, plus the moment its 30-second clock started. */
export interface SealedArbitragePreview {
  preview: ArbitragePreview;
  request: ArbitragePreviewRequest;
  /** `Date.now()` when the response landed — the deadline is not in the body. */
  sealedAt: number;
}

export function usePreviewArbitrage() {
  return useMutation({
    mutationFn: async (request: ArbitragePreviewRequest): Promise<SealedArbitragePreview> => {
      const preview = await arbitrage.preview(request);
      return { preview, request, sealedAt: Date.now() };
    },
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

/**
 * Spends a sealed preview and places both legs.
 *
 * A new lineage per hedge: the two legs share it, and it is what every later
 * read — positions, the funding roll-up, the close — joins them on.
 */
export function useOpenArbitrage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (previewId: string): Promise<ArbitrageExecution> =>
      arbitrage.open({ previewId, lineageId: freshId(), idempotencyKey: freshId() }),
    onSuccess: () => invalidateArbitrage(queryClient),
  });
}

export function useCloseArbitrage() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (lineageId: string): Promise<ArbitrageExecution> =>
      arbitrage.close({ lineageId, idempotencyKey: freshId() } satisfies ArbitrageCloseRequest),
    onSuccess: () => invalidateArbitrage(queryClient),
  });
}

/**
 * Cancels one leg — the only way out of `partially_open`.
 *
 * Both identifiers are sent and the server checks they agree, which is what
 * stops a stale row cancelling a leg of some other pair.
 */
export function useCancelArbitrageLeg() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (leg: { lineageId: string; commandId: string }): Promise<Command> =>
      arbitrage.cancelLeg({
        lineageId: leg.lineageId,
        orderId: leg.commandId,
        idempotencyKey: freshId(),
      } satisfies ArbitrageCancelRequest),
    onSuccess: () => invalidateArbitrage(queryClient),
  });
}

/**
 * A hedge writes to more ledgers than its own: both legs land in the shared
 * position and fill projections that the Trade and Portfolio screens read, so
 * refreshing only this feature's key would leave those two showing a hedge that
 * has already moved.
 */
function invalidateArbitrage(queryClient: ReturnType<typeof useQueryClient>): void {
  void queryClient.invalidateQueries({ queryKey: fundingKeys.positions() });
  void queryClient.invalidateQueries({ queryKey: tradeKeys.positions() });
  void queryClient.invalidateQueries({ queryKey: tradeKeys.fills() });
}
