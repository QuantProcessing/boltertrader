import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { accounts, catalog, market, type Metered, type Projected } from "@/api/endpoints";
import type {
  Account,
  CandleInterval,
  CandlePage,
  CandleQuery,
  Instrument,
  MarketDataQuery,
  MarketFact,
} from "@/api/types";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";
import { useReadOnly } from "@/app/mode";

/* ---------------------------------------------------------------------------
 * Reads that describe a market.
 *
 * Every one of these spends the venue's rate-limit budget, not ours — the
 * `rateLimit` sibling on the response is what is left of the venue's allowance
 * for this account. That is why the polling intervals here are deliberate
 * numbers with reasons rather than "as fast as it looks smooth", and why the
 * remaining budget is carried up to the UI instead of being discarded.
 *
 * The catalog is the exception: it is trading's own projection, refreshed on a
 * revision, and costs a venue nothing.
 * ------------------------------------------------------------------------- */

export const marketKeys = {
  instruments: () => ["catalog", "instruments"] as const,
  accounts: () => ["accounts", "list"] as const,
  fact: (query: MarketDataQuery | null) => ["market", "fact", query] as const,
  candles: (query: CandleQuery | null) => ["market", "candles", query] as const,
};

/** The one public input identity. The catalog owns every route coordinate. */
export function marketOf(instrument: Instrument): MarketDataQuery {
  return {
    instrumentId: instrument.instrumentId,
  };
}

export function useInstruments(): UseQueryResult<Projected<Instrument>> {
  return useQuery({
    queryKey: marketKeys.instruments(),
    queryFn: ({ signal }) => catalog.instruments(signal),
    // The catalog moves when a venue publishes a new revision, which is hours
    // apart. Re-reading it on a tab focus would be pure noise.
    staleTime: 5 * 60_000,
  });
}

/**
 * The operator's own accounts — the one private read on this surface.
 *
 * Everything else here describes a market and is public at every venue, which
 * is why only this hook carries the read-only gate. A signed-out visitor gets
 * an empty list and a ticket that already knows how to say "no account for this
 * venue", rather than a 401 behind a panel that looks broken.
 */
const LIFECYCLE_IN_FLIGHT = new Set<Account["status"]>([
  "creating",
  "removing",
]);

/** Poll only while the server still owes the operator a lifecycle outcome. */
export function accountLifecyclePollInterval(accounts: Account[] | undefined): number | false {
  return accounts?.some((account) => LIFECYCLE_IN_FLIGHT.has(account.status)) === true
    ? 2_000
    : false;
}

export function useAccounts(options: { watchLifecycle?: boolean } = {}): UseQueryResult<Account[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: marketKeys.accounts(),
    queryFn: ({ signal }) => accounts.list(signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: options.watchLifecycle
      ? (query) => accountLifecyclePollInterval(query.state.data)
      : false,
    staleTime: 30_000,
  });
}

/**
 * Top of book, depth, mark price and funding for one instrument.
 *
 * REST supplies the first snapshot. The unified WebSocket owns later depth and
 * mark changes and performs its own generation-based resnapshot after a gap.
 */
export function useMarketFact(query: MarketDataQuery | null): UseQueryResult<Metered<MarketFact>> {
  return useQuery({
    queryKey: marketKeys.fact(query),
    queryFn: ({ signal }) => market.data(query as MarketDataQuery, signal),
    enabled: query !== null,
    // Keep the previous instrument's book on screen while the new one loads
    // rather than blanking the panel: an empty book reads as "no liquidity".
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
}

export function useCandles(query: CandleQuery | null): UseQueryResult<Metered<CandlePage>> {
  return useQuery({
    queryKey: marketKeys.candles(query),
    queryFn: ({ signal }) => market.candles(query as CandleQuery, signal),
    enabled: query !== null,
    placeholderData: (previous) => previous,
  });
}

/**
 * Which accounts can actually trade this instrument.
 *
 * One fence, server-enforced anyway: the venue has to match, or the order has
 * nowhere to go. Filtering here means the operator never picks an account that
 * cannot work, rather than finding out at the preview.
 */
export function tradableAccounts(all: Account[], instrument: Instrument | null): Account[] {
  if (!instrument) return [];
  return all.filter((account) => account.venue === instrument.venue);
}
