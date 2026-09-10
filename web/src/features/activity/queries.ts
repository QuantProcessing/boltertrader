import { selectActivity } from "@/features/trade/ledger-snapshot";
import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { trading, type ActivityQuery } from "@/api/endpoints";
import type { ActivityEvent } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";

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
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: activityKeys.list(query),
    queryFn: async ({ signal }) => {
      const startedAt = Date.now();
      return selectActivity(await trading.activity(query, signal), {...query}, startedAt);
    },
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 60_000,
    placeholderData: (previous) => previous,
  });
}
