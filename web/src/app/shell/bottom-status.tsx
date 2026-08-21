import { useQuery } from "@tanstack/react-query";

import { system } from "@/api/endpoints";
import type { FundingBoardVenue } from "@/api/types";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useFundingBoard } from "@/features/funding/queries";
import { useT, type Key } from "@/lib/i18n";

const STATUS_KEY = ["system", "status"] as const;

export function BottomStatus() {
  const t = useT();
  const status = useQuery({
    queryKey: STATUS_KEY,
    queryFn: ({ signal }) => system.status(signal),
    refetchInterval: 10_000,
    retry: 1,
  });
  const board = useFundingBoard();

  let tone: StateTone = "pending";
  let label: Key = "utility.connecting";
  if (status.isError) {
    tone = "danger";
    label = "utility.offline";
  } else if (status.data?.ready === false) {
    tone = "warn";
    label = "utility.degraded";
  } else if (status.data?.ready) {
    tone = "ok";
    label = "utility.online";
  }
  const venues = summarizeVenues(board.data?.venues ?? []);

  return (
    <footer className="flex h-status shrink-0 items-center gap-3 border-t border-line bg-panel px-3 text-meta text-muted">
      <StateBadge tone={tone} label={t(label)} aria-live="polite" />
      <span className="text-dim" aria-hidden="true">/</span>
      <span>{t("utility.mainnet")}</span>
      <div className="ml-auto flex min-w-0 items-center gap-3" aria-live="polite">
        <span className="hidden text-dim sm:inline">{t("utility.venueConnections")}</span>
        {board.isPending ? (
          <StateBadge tone="pending" label={t("utility.venueConnecting")} />
        ) : board.isError || venues.total === 0 ? (
          <StateBadge tone="danger" label={t("utility.venueUnavailable")} />
        ) : (
          <>
            <StateBadge tone="ok" label={t("utility.venueConnected", { count: venues.live.length })} detail={venues.live.join(", ")} />
            {venues.degraded.length > 0 ? <StateBadge tone="warn" label={t("utility.venueDelayed", { count: venues.degraded.length })} detail={venues.degraded.join(", ")} /> : null}
            {venues.unavailable.length > 0 ? <StateBadge tone="danger" label={t("utility.venueOffline", { count: venues.unavailable.length })} detail={venues.unavailable.join(", ")} /> : null}
          </>
        )}
      </div>
    </footer>
  );
}

/**
 * Collapse route-profile status into one conservative state per venue. A venue
 * is live only when every one of its public profiles is live; a partial profile
 * failure is degraded, and only a venue with no usable profile is offline.
 */
export function summarizeVenues(columns: FundingBoardVenue[]) {
  const profiles = new Map<string, FundingBoardVenue[]>();
  for (const column of columns) profiles.set(column.venue, [...(profiles.get(column.venue) ?? []), column]);
  const result = { live: [] as string[], degraded: [] as string[], unavailable: [] as string[], total: profiles.size };
  for (const [venue, entries] of profiles) {
    if (entries.every((entry) => entry.unavailable || entry.status === "unavailable")) result.unavailable.push(venue);
    else if (entries.some((entry) => entry.stale || entry.status !== "live" || entry.unavailable)) result.degraded.push(venue);
    else result.live.push(venue);
  }
  result.live.sort();
  result.degraded.sort();
  result.unavailable.sort();
  return result;
}
