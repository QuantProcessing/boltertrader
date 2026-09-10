import { watchSessionEpoch } from "@/api/client";
import type { ActivityEvent, LedgerEntry } from "@/api/types";
let ledger: LedgerEntry[] | undefined;
let activity: ActivityEvent[] | undefined;
let ledgerAt = 0;
let activityAt = 0;
watchSessionEpoch(() => {
  ledger = undefined;
  activity = undefined;
  ledgerAt = activityAt = 0;
});
export function recordLedgerSnapshot(data: {
  ledger?: LedgerEntry[];
  activity?: ActivityEvent[];
}) {
  if (Array.isArray(data.ledger)) {
    ledger = data.ledger;
    ledgerAt = Date.now();
  }
  if (Array.isArray(data.activity)) {
    activity = data.activity;
    activityAt = Date.now();
  }
}
export function selectLedger(
  fallback: LedgerEntry[],
  query: Record<string, string | undefined> = {},
  requestStartedAt = -Infinity,
): LedgerEntry[] {
  return (ledger && ledgerAt > requestStartedAt ? ledger : fallback).filter(
    (item) => matches(item, query),
  );
}
export function selectActivity(
  fallback: ActivityEvent[],
  query: Record<string, string | undefined> = {},
  requestStartedAt = -Infinity,
): ActivityEvent[] {
  return (
    activity && activityAt > requestStartedAt ? activity : fallback
  ).filter((item) => matches(item, query));
}
function matches(item: object, query: Record<string, string | undefined>) {
  return Object.entries(query).every(
    ([key, value]) =>
      !value || (item as Record<string, unknown>)[key] === value,
  );
}
