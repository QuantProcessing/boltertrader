import { useQuery } from "@tanstack/react-query";
import { ChevronUp } from "lucide-react";

import { system } from "@/api/endpoints";
import type { FundingBoardVenue } from "@/api/types";
import { PopoverContent, PopoverRoot, PopoverTrigger } from "@/components/ui/popover";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { useFundingBoard } from "@/features/funding/queries";
import { formatTimestamp } from "@/lib/format";
import { useT, type Key } from "@/lib/i18n";
import { cn } from "@/lib/utils";

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
  const connections = venueConnectionDetails(board.data?.venues ?? []);

  return (
    <footer className="flex h-status shrink-0 items-center gap-3 border-t border-line bg-panel px-3 text-meta text-muted">
      <StateBadge tone={tone} label={t(label)} aria-live="polite" />
      <span className="text-dim" aria-hidden="true">/</span>
      <span>{t("utility.mainnet")}</span>
      <div className="ml-auto flex min-w-0 items-center gap-3" aria-live="polite">
        <PopoverRoot>
          <PopoverTrigger asChild>
            <button
              type="button"
              className="group flex h-status min-w-0 items-center gap-3 rounded-control px-1.5 outline-none transition-colors hover:bg-fill focus-visible:ring-1 focus-visible:ring-accent"
              aria-label={t("utility.venueDetails")}
            >
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
              <ChevronUp aria-hidden className="size-3 shrink-0 text-dim transition-transform duration-200 group-data-[state=open]:rotate-180" />
            </button>
          </PopoverTrigger>
          <PopoverContent side="top" align="end" className="w-[23rem]">
            <VenueConnectionsPanel
              connections={connections}
              observedAt={board.data?.observedAt}
              loading={board.isPending}
              unavailable={board.isError || (!board.isPending && venues.total === 0)}
            />
          </PopoverContent>
        </PopoverRoot>
      </div>
    </footer>
  );
}

type VenueConnectionState = "live" | "degraded" | "unavailable";

export interface VenueConnectionDetail {
  venue: string;
  state: VenueConnectionState;
  profiles: number;
  scopes: string[];
  staleProfiles: number;
  reconnectingProfiles: number;
  unavailableProfiles: number;
  reconnects: number;
  lastSuccessAt?: string;
}

function VenueConnectionsPanel({
  connections,
  observedAt,
  loading,
  unavailable,
}: {
  connections: VenueConnectionDetail[];
  observedAt?: string;
  loading: boolean;
  unavailable: boolean;
}) {
  const t = useT();
  return (
    <div>
      <header className="flex items-start justify-between gap-4 border-b border-line px-3 py-2.5">
        <div>
          <h2 className="text-title font-semibold text-text">{t("utility.venueConnections")}</h2>
          <p className="mt-0.5 text-meta text-dim">{t("utility.venuePublicScope")}</p>
        </div>
        <span className="shrink-0 text-micro text-dim">{observedAt ? formatTimestamp(observedAt) : "—"}</span>
      </header>

      <div className="max-h-[min(28rem,60vh)] overflow-y-auto p-1.5">
        {loading ? (
          <div className="px-2 py-6 text-center text-meta text-dim">{t("utility.venueConnecting")}</div>
        ) : unavailable ? (
          <div className="px-2 py-6 text-center text-meta text-danger">{t("utility.venueUnavailable")}</div>
        ) : connections.map((connection) => (
          <div key={connection.venue} className="rounded-control px-2 py-2 hover:bg-fill">
            <div className="flex items-center justify-between gap-3">
              <StateBadge tone={connectionTone(connection.state)} label={venueName(connection.venue)} className="font-medium text-text" />
              <span className={cn("text-meta font-medium", connectionStateClass(connection.state))}>
                {t(connectionStateKey(connection.state))}
              </span>
            </div>
            <div className="mt-1 flex min-w-0 items-center justify-between gap-3 pl-3 text-micro text-dim">
              <span className="truncate">{connection.scopes.join(" / ") || t("utility.venueRoutes", { count: connection.profiles })}</span>
              <span className="shrink-0">
                {connection.lastSuccessAt
                  ? t("utility.venueLastSuccess", { time: formatTimestamp(connection.lastSuccessAt) })
                  : t("utility.venueNeverUpdated")}
              </span>
            </div>
            <p className={cn("mt-1 pl-3 text-micro", connection.state === "live" ? "text-dim" : connectionStateClass(connection.state))}>
              {connectionReason(connection, t)}
              {connection.reconnects > 0 ? ` · ${t("utility.venueReconnects", { count: connection.reconnects })}` : ""}
            </p>
          </div>
        ))}
      </div>

      <section className="border-t border-line px-3 py-2.5" aria-label={t("utility.venueRules")}>
        <h3 className="th">{t("utility.venueRules")}</h3>
        <div className="mt-1.5 space-y-1 text-micro leading-relaxed text-dim">
          <p><span className="font-medium text-warn">{t("utility.venueState.degraded")}</span> {t("utility.venueRule.degraded")}</p>
          <p><span className="font-medium text-danger">{t("utility.venueState.unavailable")}</span> {t("utility.venueRule.unavailable")}</p>
          <p>{t("utility.venueRule.refresh")}</p>
        </div>
      </section>
    </div>
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

/** One conservative, explainable public-market state per venue. */
export function venueConnectionDetails(columns: FundingBoardVenue[]): VenueConnectionDetail[] {
  const profiles = new Map<string, FundingBoardVenue[]>();
  for (const column of columns) profiles.set(column.venue, [...(profiles.get(column.venue) ?? []), column]);

  return [...profiles.entries()].map(([venue, entries]) => {
    const states = entries.map(profileState);
    const unavailableProfiles = states.filter((state) => state === "unavailable").length;
    const staleProfiles = entries.filter((entry) => entry.stale).length;
    const reconnectingProfiles = entries.filter((entry) => !entry.stale && entry.status === "degraded").length;
    const state: VenueConnectionState = unavailableProfiles === entries.length
      ? "unavailable"
      : states.some((candidate) => candidate !== "live")
        ? "degraded"
        : "live";
    return {
      venue,
      state,
      profiles: entries.length,
      scopes: [...new Set(entries.map(profileScope))].sort(),
      staleProfiles,
      reconnectingProfiles,
      unavailableProfiles,
      reconnects: entries.reduce((total, entry) => total + (entry.reconnects ?? 0), 0),
      lastSuccessAt: latestTimestamp(entries.map((entry) => entry.lastSuccessAt ?? entry.observedAt)),
    };
  }).sort((left, right) => {
    const priority: Record<VenueConnectionState, number> = { unavailable: 0, degraded: 1, live: 2 };
    return priority[left.state] - priority[right.state] || left.venue.localeCompare(right.venue);
  });
}

function profileState(profile: FundingBoardVenue): VenueConnectionState {
  if (profile.unavailable || profile.status === "unavailable") return "unavailable";
  if (profile.stale || profile.status !== "live") return "degraded";
  return "live";
}

function profileScope(profile: FundingBoardVenue): string {
  return [profile.product === "perp" ? "Perp" : profile.product, profile.settlement].filter(Boolean).join(" · ");
}

function latestTimestamp(values: Array<string | undefined>): string | undefined {
  return values.filter((value): value is string => Boolean(value)).sort((left, right) => Date.parse(right) - Date.parse(left))[0];
}

function connectionReason(connection: VenueConnectionDetail, t: ReturnType<typeof useT>): string {
  if (connection.state === "live") return t("utility.venueReason.live", { count: connection.profiles });
  if (connection.state === "unavailable") return t("utility.venueReason.unavailable");
  const reasons: string[] = [];
  if (connection.staleProfiles > 0) reasons.push(t("utility.venueReason.stale"));
  if (connection.reconnectingProfiles > 0) reasons.push(t("utility.venueReason.reconnecting"));
  if (connection.unavailableProfiles > 0) reasons.push(t("utility.venueReason.partial", { count: connection.unavailableProfiles }));
  return reasons.join(" · ") || t("utility.venueReason.reconnecting");
}

function connectionTone(state: VenueConnectionState): StateTone {
  if (state === "live") return "ok";
  if (state === "degraded") return "warn";
  return "danger";
}

function connectionStateClass(state: VenueConnectionState): string {
  if (state === "live") return "text-ok";
  if (state === "degraded") return "text-warn";
  return "text-danger";
}

function connectionStateKey(state: VenueConnectionState): Key {
  if (state === "live") return "utility.venueState.live";
  if (state === "degraded") return "utility.venueState.degraded";
  return "utility.venueState.unavailable";
}

function venueName(venue: string): string {
  const names: Record<string, string> = {
    aster: "Aster",
    binance: "Binance",
    bitget: "Bitget",
    bybit: "Bybit",
    gate: "Gate",
    hyperliquid: "Hyperliquid",
    lighter: "Lighter",
    nado: "Nado",
    okx: "OKX",
  };
  return names[venue.toLowerCase()] ?? venue;
}
