import { useQuery, type UseQueryResult } from "@tanstack/react-query";

import { accountData, trading, type LedgerQuery, type PortfolioPositionQuery } from "@/api/endpoints";
import type { AccountPositionSnapshot, LedgerEntry, PnLSnapshot, Position } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";
import { privateFallbackInterval } from "@/features/trade/queries";

/* ---------------------------------------------------------------------------
 * The ledgers behind a position.
 *
 * `/api/v1/pnl` is not a portfolio total and cannot be made into one:
 * `ledger.Service.PnL` rejects a query missing any of productId, lineageId,
 * accountId or instrumentId, and the handler turns that into a 400. It is a
 * drill-down into one lineage on one instrument, and calling it without a
 * selection is a guaranteed error rather than an empty result.
 *
 * That is a deliberate server-side position, not an oversight. The same service
 * answers `ErrCurrencyMismatch` when a lineage's entries span more than one
 * settlement currency, because summing across them would produce a number with
 * no unit. A client-side total over every position would do exactly that, in
 * silence — so this module does not offer one, and the page asks for a P&L only
 * once the operator has named which position they mean.
 * ------------------------------------------------------------------------- */

export const portfolioKeys = {
  positions: (query: PortfolioPositionQuery) => ["trading", "portfolio-positions", query] as const,
  ledger: (query: LedgerQuery) => ["trading", "ledger", query] as const,
  pnl: (query: PnLKey | null) => ["trading", "pnl", query] as const,
};

/** The complete attribution the P&L route demands. All four, or a 400. */
export type PnLKey = Required<LedgerQuery>;

/**
 * A position carries exactly the four fields, which is what makes selecting a
 * row the natural way to ask this question — there is nothing left to guess.
 */
export function pnlKeyOf(position: Position): PnLKey {
  return {
    productId: position.productId,
    lineageId: position.lineageId,
    accountId: position.accountId,
    instrumentId: position.instrumentId,
  };
}

export function usePortfolioPositions(query: PortfolioPositionQuery): UseQueryResult<AccountPositionSnapshot> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: portfolioKeys.positions(query),
    queryFn: ({ signal }) => accountData.portfolioPositions(query, signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: privateFallbackInterval,
    // Changing the account filter should swap the rows, not blank the table
    // and re-flash a loading state over a list that is mostly the same.
    placeholderData: (previous) => previous,
  });
}

/**
 * The ledger, filtered to whatever scope the page is showing.
 *
 * Fifteen seconds because entries are written when something already happened:
 * a fill, a fee, a funding payment. Nothing here is a decision aid, so polling
 * it at the book's rate would only cost requests.
 */
export function useLedger(query: LedgerQuery): UseQueryResult<LedgerEntry[]> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: portfolioKeys.ledger(query),
    queryFn: ({ signal }) => trading.ledger(query, signal),
    enabled: !readOnly,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 15_000,
    placeholderData: (previous) => previous,
  });
}

/** Disabled until every attribution field is known — see the note above. */
export function usePnL(key: PnLKey | null): UseQueryResult<PnLSnapshot> {
  const readOnly = useReadOnly();
  return useQuery({
    queryKey: portfolioKeys.pnl(key),
    queryFn: ({ signal }) => trading.pnl(key as PnLKey, signal),
    enabled: !readOnly && key !== null,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 15_000,
  });
}
