import type { Key, Translate } from "@/lib/i18n";

const LABELS: Record<string, Key> = {
  account_unreachable: "arb.blocker.accountUnreachable",
  board_stale: "arb.blocker.boardStale",
  funding_interval_unknown: "arb.blocker.fundingIntervalUnknown",
  instrument_identity_unresolved: "arb.blocker.instrumentIdentityUnresolved",
};

/** Opportunity blocker codes are API vocabulary; the board needs operator copy. */
export function arbitrageBlockerLabel(code: string, t: Translate): string {
  const key = LABELS[code];
  return key ? t(key) : code.replace(/_/g, " ");
}
