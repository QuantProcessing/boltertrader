export interface LiveReferencePrice {
  fundingRate?: string;
  openInterest?: string;
  fundingObservedAt?: string;
  openInterestObservedAt?: string;
  nextFundingTime?: string;
  instrumentId: string;
  price: string;
  observedAt: string;
  stale: boolean;
}

export const DEFAULT_REFERENCE_STALE_AFTER_MS = 5_000;

/** The Gateway owns source cadence; the browser only evaluates its deadline. */
export function referenceStaleDelay(
  observedAt: string,
  staleAfterMs: unknown,
  receivedAgeMs?: unknown,
  now = Date.now(),
): number {
  const configured =
    typeof staleAfterMs === "number" &&
    Number.isFinite(staleAfterMs) &&
    staleAfterMs >= 1_000
      ? staleAfterMs
      : DEFAULT_REFERENCE_STALE_AFTER_MS;
  // Gateway computes age with the same clock that receives venue data. This
  // uses its local receipt clock, avoiding both venue and browser clock skew
  // turning every fresh packet into a brief
  // fresh-then-stale flash. Older servers omit ageMs, so retain the timestamp
  // calculation only as a compatibility fallback.
  if (
    typeof receivedAgeMs === "number" &&
    Number.isFinite(receivedAgeMs) &&
    receivedAgeMs >= 0
  ) {
    return Math.max(0, configured - receivedAgeMs);
  }
  const observed = Date.parse(observedAt);
  const deadline = Number.isFinite(observed)
    ? observed + configured
    : now + configured;
  return Math.max(0, deadline - now);
}
