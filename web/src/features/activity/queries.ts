import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { trading, type ActivityQuery } from "@/api/endpoints";
import type { ActivityEvent } from "@/api/types";

/* ---------------------------------------------------------------------------
 * The event log.
 *
 * `ActivityLog` in trading is a bounded in-memory ring, not a table: it holds
 * the last N events this process published and starts empty after a restart.
 * So an empty log means "nothing since trading came up", which is a different
 * statement from "nothing ever happened" — the page says so rather than
 * implying the operator has never traded.
 *
 * Newest first is the server's order, and it is preserved here.
 * ------------------------------------------------------------------------- */

export const activityKeys = {
  list: (query: ActivityQuery) => ["trading", "activity", query] as const,
};

export function useActivity(query: ActivityQuery): UseQueryResult<ActivityEvent[]> {
  return useQuery({
    queryKey: activityKeys.list(query),
    queryFn: ({ signal }) => trading.activity(query, signal),
    refetchInterval: 10_000,
    placeholderData: (previous) => previous,
  });
}
