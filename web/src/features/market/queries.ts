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
  Mode,
} from "@/api/types";

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

/** The four fields that name a market. An instrument carries all of them. */
export function marketOf(instrument: Instrument): MarketDataQuery {
  return {
    venue: instrument.venue,
    environment: instrument.environment,
    product: instrument.product,
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

export function useAccounts(): UseQueryResult<Account[]> {
  return useQuery({
    queryKey: marketKeys.accounts(),
    queryFn: ({ signal }) => accounts.list(signal),
    staleTime: 30_000,
  });
}

/**
 * Top of book, depth, mark price and funding for one instrument.
 *
 * Two seconds is the floor that still leaves an operator room to place orders
 * against the same budget. The book is a decision aid here, not a feed — a
 * venue websocket is the right answer for tick-level depth and is not on this
 * surface yet.
 */
export function useMarketFact(query: MarketDataQuery | null): UseQueryResult<Metered<MarketFact>> {
  return useQuery({
    queryKey: marketKeys.fact(query),
    queryFn: ({ signal }) => market.data(query as MarketDataQuery, signal),
    enabled: query !== null,
    refetchInterval: 2_000,
    // Keep the previous instrument's book on screen while the new one loads
    // rather than blanking the panel: an empty book reads as "no liquidity".
    placeholderData: (previous) => previous,
    staleTime: 0,
  });
}

/**
 * How often a series is worth re-reading, given how often it can change.
 *
 * Only the newest bar moves between requests, so anything faster than a
 * fraction of the bar width redraws the same candles.
 */
const CANDLE_POLL_MS: Record<CandleInterval, number> = {
  "1m": 10_000,
  "5m": 20_000,
  "15m": 30_000,
  "30m": 30_000,
  "1h": 60_000,
  "4h": 60_000,
  "12h": 60_000,
  "1d": 60_000,
};

export function useCandles(query: CandleQuery | null): UseQueryResult<Metered<CandlePage>> {
  return useQuery({
    queryKey: marketKeys.candles(query),
    queryFn: ({ signal }) => market.candles(query as CandleQuery, signal),
    enabled: query !== null,
    refetchInterval: query ? CANDLE_POLL_MS[query.interval] : false,
    placeholderData: (previous) => previous,
  });
}

/**
 * Which accounts can actually trade this instrument.
 *
 * Two fences, both server-enforced anyway: the venue has to match, or the order
 * has nowhere to go; and the mode has to match the session's, or the preview is
 * rejected with a 400. Filtering here means the operator never picks an account
 * that cannot work, rather than finding out at the preview.
 */
export function tradableAccounts(all: Account[], instrument: Instrument | null, mode: Mode): Account[] {
  if (!instrument) return [];
  return all.filter((account) => account.venue === instrument.venue && account.mode === mode);
}
