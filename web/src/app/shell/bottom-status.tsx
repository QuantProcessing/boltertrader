import { useQuery } from "@tanstack/react-query";
import { ChevronUp } from "lucide-react";
import { useEffect, useState } from "react";

import { system, type VenueConnectivity } from "@/api/endpoints";
import { PopoverContent, PopoverRoot, PopoverTrigger } from "@/components/ui/popover";
import { StateBadge, type StateTone } from "@/components/ui/state-badge";
import { realtimeTopics } from "@/features/realtime/topic-manager";
import { formatTimestamp } from "@/lib/format";
import { useT, type Key } from "@/lib/i18n";
import { cn } from "@/lib/utils";

const STATUS_KEY = ["system", "status"] as const;

export type VenueConnectivityState = VenueConnectivity["state"];

export function BottomStatus() {
  const t = useT();
  const status = useQuery({
    queryKey: STATUS_KEY,
    queryFn: ({ signal }) => system.status(signal),
    refetchInterval: 10_000,
    retry: 1,
  });
  const connectivity = useQuery({
    queryKey: ["venues", "connectivity"],
    queryFn: ({ signal }) => system.venueConnectivity(signal),
    retry: 1,
  });
  const [connections, setConnections] = useState<VenueConnectivity[]>([]);
  const [connectionError, setConnectionError] = useState(false);

  useEffect(() => realtimeTopics.subscribe({ topic: "system.venue_connectivity" }, (message) => {
    if ((message.type === "snapshot" || message.type === "delta") && validConnectivity(message.data)) {
      setConnections([...message.data].sort((left, right) => statePriority(left.state) - statePriority(right.state) || left.venue.localeCompare(right.venue)));
      setConnectionError(false);
    } else if (message.type === "error" || message.type === "resync_required") {
      setConnectionError(true);
    }
  }), []);

  useEffect(() => {
    if (connectivity.data) {
      setConnections([...connectivity.data].sort((left, right) => statePriority(left.state) - statePriority(right.state) || left.venue.localeCompare(right.venue)));
      setConnectionError(false);
    } else if (connectivity.isError) {
      setConnectionError(true);
    }
  }, [connectivity.data, connectivity.isError]);

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
  const venues = summarizeVenues(connections);

  return (
    <footer className="flex h-status shrink-0 items-center gap-3 border-t border-line bg-panel px-3 text-meta text-muted">
      <StateBadge tone={tone} label={t(label)} aria-live="polite" />
      <span className="text-dim" aria-hidden="true">/</span>
      <span>{t("utility.mainnet")}</span>
      <div className="ml-auto flex min-w-0 items-center gap-3" aria-live="polite">
        <PopoverRoot>
          <PopoverTrigger asChild>
            <button type="button" className="group flex h-status min-w-0 items-center gap-3 rounded-control px-1.5 outline-none transition-colors hover:bg-fill focus-visible:ring-1 focus-visible:ring-accent" aria-label={t("utility.venueDetails")}>
              <span className="hidden text-dim sm:inline">{t("utility.venueConnections")}</span>
              {connections.length === 0 ? (
                <StateBadge tone={connectionError ? "danger" : "pending"} label={t(connectionError ? "utility.venueUnavailable" : "utility.venueConnecting")} />
              ) : (
                <>
                  {venues.online.length > 0 ? <StateBadge tone="ok" label={t("utility.venueConnected", { count: venues.online.length })} detail={venues.online.join(", ")} /> : null}
                  {(venues.delayed.length + venues.connecting.length) > 0 ? <StateBadge tone="warn" label={t("utility.venueDelayed", { count: venues.delayed.length + venues.connecting.length })} detail={[...venues.delayed, ...venues.connecting].join(", ")} /> : null}
                  {venues.offline.length > 0 ? <StateBadge tone="danger" label={t("utility.venueOffline", { count: venues.offline.length })} detail={venues.offline.join(", ")} /> : null}
                </>
              )}
              <ChevronUp aria-hidden className="size-3 shrink-0 text-dim transition-transform duration-200 group-data-[state=open]:rotate-180" />
            </button>
          </PopoverTrigger>
          <PopoverContent side="top" align="end" className="w-[23rem]">
            <VenueConnectionsPanel connections={connections} unavailable={connectionError} />
          </PopoverContent>
        </PopoverRoot>
      </div>
    </footer>
  );
}

function VenueConnectionsPanel({ connections, unavailable }: { connections: VenueConnectivity[]; unavailable: boolean }) {
  const t = useT();
  return (
    <div>
      <header className="border-b border-line px-3 py-2.5">
        <h2 className="text-title font-semibold text-text">{t("utility.venueConnections")}</h2>
        <p className="mt-0.5 text-meta text-dim">{t("utility.venueProbeScope")}</p>
      </header>
      <div className="max-h-[min(28rem,60vh)] overflow-y-auto p-1.5">
        {connections.length === 0 ? (
          <div className={cn("px-2 py-6 text-center text-meta", unavailable ? "text-danger" : "text-dim")}>
            {t(unavailable ? "utility.venueUnavailable" : "utility.venueConnecting")}
          </div>
        ) : connections.map((connection) => (
          <div key={connection.venue} className="rounded-control px-2 py-2 hover:bg-fill">
            <div className="flex items-center justify-between gap-3">
              <StateBadge tone={connectionTone(connection.state)} label={venueName(connection.venue)} className="font-medium text-text" />
              <span className={cn("text-meta font-medium", connectionStateClass(connection.state))}>{t(connectionStateKey(connection.state))}</span>
            </div>
            <div className="mt-1 flex items-center justify-between gap-3 pl-3 text-micro text-dim">
              <span>{t("utility.venueLatency", { value: formatLatency(connection.latencyMs) })}</span>
              <span>{connection.lastSuccessAt ? t("utility.venueLastSuccess", { time: formatTimestamp(connection.lastSuccessAt) }) : t("utility.venueNeverUpdated")}</span>
            </div>
            <p className={cn("mt-1 pl-3 text-micro", connection.state === "online" ? "text-dim" : connectionStateClass(connection.state))}>
              {connection.state === "connecting"
                ? t("utility.venueReason.connecting")
                : connection.consecutiveFailures > 0
                  ? t("utility.venueFailures", { count: connection.consecutiveFailures })
                  : t("utility.venueEWMA", { value: formatLatency(connection.ewmaLatencyMs) })}
            </p>
          </div>
        ))}
      </div>
      <p className="border-t border-line px-3 py-2.5 text-micro leading-relaxed text-dim">{t("utility.venueProbeRule")}</p>
    </div>
  );
}

export function summarizeVenues(connections: VenueConnectivity[]) {
  const result = { online: [] as string[], delayed: [] as string[], connecting: [] as string[], offline: [] as string[], total: connections.length };
  for (const connection of connections) result[connection.state].push(connection.venue);
  for (const values of [result.online, result.delayed, result.connecting, result.offline]) values.sort();
  return result;
}

function validConnectivity(value: unknown): value is VenueConnectivity[] {
  return Array.isArray(value) && value.every((item) => {
    if (typeof item !== "object" || item === null) return false;
    const candidate = item as Partial<VenueConnectivity>;
    return typeof candidate.venue === "string"
      && ["connecting", "online", "delayed", "offline"].includes(candidate.state ?? "")
      && typeof candidate.latencyMs === "number"
      && typeof candidate.ewmaLatencyMs === "number"
      && typeof candidate.consecutiveFailures === "number";
  });
}

function statePriority(state: VenueConnectivityState): number {
  return { offline: 0, delayed: 1, connecting: 2, online: 3 }[state];
}

function formatLatency(value: number): string {
  if (!Number.isFinite(value) || value <= 0) return "—";
  return `${Math.round(value)} ms`;
}

function connectionTone(state: VenueConnectivityState): StateTone {
  if (state === "online") return "ok";
  if (state === "offline") return "danger";
  return "warn";
}

function connectionStateClass(state: VenueConnectivityState): string {
  if (state === "online") return "text-ok";
  if (state === "offline") return "text-danger";
  return "text-warn";
}

function connectionStateKey(state: VenueConnectivityState): Key {
  return `utility.venueState.${state}` as Key;
}

function venueName(venue: string): string {
  const names: Record<string, string> = {
    aster: "Aster", binance: "Binance", bitget: "Bitget", bybit: "Bybit", gate: "Gate",
    hyperliquid: "Hyperliquid", lighter: "Lighter", nado: "Nado", okx: "OKX",
  };
  return names[venue.toLowerCase()] ?? venue;
}
