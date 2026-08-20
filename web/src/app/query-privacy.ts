/* ---------------------------------------------------------------------------
 * One marker for every query whose answer belongs to an account or principal.
 *
 * `enabled: false` stops a query from polling while signed out, but React
 * Query intentionally keeps its last successful response. That is useful for
 * public market data and wrong for an account surface: signing out must also
 * remove the account, position and order values already in memory. AppShell
 * clears every query carrying this marker at that boundary.
 * ------------------------------------------------------------------------- */

export const PRIVATE_QUERY_META = { privateData: true } as const;
