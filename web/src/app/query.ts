/* ---------------------------------------------------------------------------
 * The query client, and the retry policy it enforces.
 *
 * Two decisions here are about correctness rather than tuning:
 *
 *   - Nothing is retried unless the failure says it may be. The server computes
 *     `retryable` per error and the client must not second-guess it — a blanket
 *     three-attempt default would repeat a rejected order twice more.
 *   - Every failure passes the session store on its way out, so an expiry
 *     detected by any query signs the operator out instead of leaving one
 *     unexplained error on an otherwise live screen.
 * ------------------------------------------------------------------------- */

import { MutationCache, QueryCache, QueryClient } from "@tanstack/react-query";

import { ApiError, NetworkError, isAuthFailure, isProjectionWarmingUp, isStaleSession } from "@/api/errors";
import { PRIVATE_QUERY_META } from "./query-privacy";
import { sessionStore } from "./session";

/**
 * How long a projection that is still warming up is worth waiting for.
 *
 * Trading answers 503 with a `*_PROJECTION_UNAVAILABLE` code while a table is
 * catching up. That is a "not yet", not a "no", and the operator should see the
 * screen fill in rather than an error they would only resolve by reloading.
 */
const WARMUP_ATTEMPTS = 6;

function shouldRetry(failureCount: number, error: unknown): boolean {
  // The session moved; a repeat would be authorized as someone else at best.
  if (isStaleSession(error) || isAuthFailure(error)) return false;
  if (isProjectionWarmingUp(error)) return failureCount < WARMUP_ATTEMPTS;
  // Nothing answered. Safe to repeat for a read — this policy governs queries,
  // and mutations opt out below.
  if (error instanceof NetworkError) return failureCount < 2;
  if (error instanceof ApiError) return error.retryable && failureCount < 2;
  return false;
}

export function createQueryClient(): QueryClient {
  const onError = (error: unknown) => sessionStore.reportAuthFailure(error);

  const client = new QueryClient({
    queryCache: new QueryCache({ onError }),
    mutationCache: new MutationCache({ onError }),
    defaultOptions: {
      queries: {
        retry: shouldRetry,
        retryDelay: (attempt) => Math.min(1000 * 2 ** attempt, 8000),
        // Market data is fetched on its own interval by the views that need it.
        // A global refetch-on-focus would fire every panel at once whenever the
        // operator alt-tabs back, which spends the venue's rate-limit budget on
        // data that is about to be replaced anyway.
        refetchOnWindowFocus: false,
        staleTime: 5_000,
      },
      mutations: {
        // A mutation that failed without an answer may still have landed. The
        // caller decides — for orders that decision is the idempotency key.
        retry: false,
      },
    },
  });
  client.setQueryDefaults(["trading"], { meta: PRIVATE_QUERY_META });
  client.setQueryDefaults(["manual"], { meta: PRIVATE_QUERY_META });
  return client;
}
