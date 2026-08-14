import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { funding, type Metered } from "@/api/endpoints";
import type { FundingBoardSnapshot } from "@/api/types";

/* ---------------------------------------------------------------------------
 * The funding board's one read.
 *
 * It is the only market read on this surface that needs no account, no
 * instrument and no query at all — which is exactly what makes the board the
 * first useful screen on a fresh install. The key is a constant for the same
 * reason: there is one board, and every visitor asks for it identically.
 * ------------------------------------------------------------------------- */

export const fundingKeys = {
  board: () => ["funding", "board"] as const,
};

/**
 * The cross-venue matrix, re-read every fifteen seconds.
 *
 * Fifteen matches trading's own cache TTL, so a faster poll would spend
 * requests to be handed the identical document; a slower one would let the
 * projection age past the edition it is already willing to serve. Funding
 * settles hourly, so this is generous either way — the countdowns tick locally
 * off `nextFundingTime` rather than off the poll.
 */
export function useFundingBoard(): UseQueryResult<Metered<FundingBoardSnapshot>> {
  return useQuery({
    queryKey: fundingKeys.board(),
    queryFn: ({ signal }) => funding.board(signal),
    refetchInterval: 15_000,
    // Keep the matrix on screen through a refetch. Blanking a table this wide
    // reads as "the venues went away", which is the one thing the stale and
    // unavailable markers exist to say precisely instead of by implication.
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
}
