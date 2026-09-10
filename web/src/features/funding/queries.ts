import { useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";
import { useEffect } from "react";

import { funding } from "@/api/endpoints";
import type { FundingBoardSnapshot } from "@/api/types";
import { realtimeTopics } from "@/features/realtime/topic-manager";

export const fundingKeys = {
  board: () => ["funding", "board"] as const,
};

/**
 * REST supplies the first matrix; the unified realtime socket replaces it with
 * coalesced projection snapshots and deltas. Countdowns tick locally off
 * `nextFundingTime`.
 */
export function useFundingBoard(): UseQueryResult<FundingBoardSnapshot> {
  const queryClient = useQueryClient();
  const query = useQuery({
    queryKey: fundingKeys.board(),
    queryFn: ({ signal }) => funding.board(signal),
    // Keep the matrix on screen through a refetch. Blanking a table this wide
    // reads as "the venues went away", which is the one thing the stale and
    // unavailable markers exist to say precisely instead of by implication.
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
  useEffect(() => realtimeTopics.subscribe({ topic: "funding.board" }, (message) => {
    if ((message.type === "snapshot" || message.type === "delta") && validFundingBoard(message.data)) {
      queryClient.setQueryData(fundingKeys.board(), message.data);
    } else if (message.type === "resync_required") {
      void queryClient.invalidateQueries({ queryKey: fundingKeys.board() });
    }
  }), [queryClient]);
  return query;
}

function validFundingBoard(value: unknown): value is FundingBoardSnapshot {
  if (typeof value !== "object" || value === null) return false;
  const candidate = value as Partial<FundingBoardSnapshot>;
  return typeof candidate.observedAt === "string" && Array.isArray(candidate.venues);
}
