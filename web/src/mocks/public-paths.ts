/**
 * The reads a signed-out browser may make.
 *
 * This mirrors the `Public` flag in the auth edge's forwarding table
 * (`services/authedge/proxy/routes.go`). Every venue behind these routes
 * publishes the same numbers to anyone who asks — a book, a candle, a funding
 * rate — so guarding them with a login protects nothing while costing the
 * product the only screen worth opening on a fresh install.
 *
 * Gateway's market socket is public. Its account socket is deliberately
 * absent because it carries private venue events.
 *
 * It lives in its own module rather than inside `handlers.ts` so the drift test
 * can import it without waking the mock database. The list is duplicated across
 * two languages on purpose — the mock is a separate implementation, not a
 * generated copy — and `public-paths.test.ts` is what stops the duplication
 * becoming a disagreement.
 */
export const PUBLIC_PATHS: ReadonlySet<string> = new Set([
  "/api/v1/system/status",
  "/api/v1/system/readiness",
  "/api/v1/catalog/instruments",
  "/api/v1/market-data",
  "/api/v1/candles",
  "/api/v1/market/realtime/ws",
  "/api/v1/funding-board",
  "/api/v1/funding-history",
  "/api/v1/venues/connectivity",
]);
