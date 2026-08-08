import type { ActivityEvent, Capability, RuntimeView } from "./types";

export type StateTone = "neutral" | "info" | "success" | "warning" | "danger";

export function environmentLabel(mode: string, _venueEnvironment?: string): "Test" | "Live" {
  return mode === "live" ? "Live" : "Test";
}

export function formatDecimal(value: string | null | undefined, currency?: string): string {
  if (value === null || value === undefined || value === "") {
    return "-";
  }
  return currency ? `${value} ${currency}` : value;
}

export function mergePositionUpdate<T extends Record<string, unknown>>(current: T, update: Partial<T>): T {
  const defined = Object.fromEntries(Object.entries(update).filter(([, value]) => value !== undefined));
  return { ...current, ...defined };
}

export function canSubmitPreview(preview: { canSubmit?: boolean; blockers?: string[] } | null | undefined): boolean {
  return Boolean(preview?.canSubmit && (preview.blockers?.length ?? 0) === 0);
}

export function flattenRuntimeCapabilities(runtimes: Record<string, RuntimeView[]>): Capability[] {
  return Object.entries(runtimes).flatMap(([accountId, accountRuntimes]) =>
    accountRuntimes.flatMap((runtime) => runtime.capabilities.flatMap((capability) =>
      Object.entries(capability.operations).map(([operation, supported]) => ({
        accountId,
        rowId: capability.rowId,
        venue: capability.venue,
        product: capability.product,
        settlement: capability.settlement,
        writeReady: capability.writeReady,
        operation,
        supported,
      })),
    )),
  );
}

export function runtimeDisplay(runtime: Pick<RuntimeView, "readiness" | "writeReadiness" | "blockedReason"> | undefined): { state: string; reason?: string } {
  if (!runtime) return { state: "unknown" };
  if (runtime.blockedReason) return { state: "blocked", reason: runtime.blockedReason };
  if (!runtime.readiness.ready) return { state: "unavailable", reason: runtime.readiness.reason };
  if (!runtime.writeReadiness.ready) return { state: "read_only", reason: runtime.writeReadiness.reason };
  return { state: "ready" };
}

export function activityDisplay(event: Pick<ActivityEvent, "kind" | "payload">): { summary: string; status: string } {
  const summary = typeof event.payload.summary === "string" && event.payload.summary.trim() ? event.payload.summary : event.kind;
  const status = typeof event.payload.status === "string" && event.payload.status.trim() ? event.payload.status : "recorded";
  return { summary, status };
}

export function stateTone(state: string): StateTone {
  switch (state.toLowerCase()) {
    case "active":
    case "ready":
    case "filled":
    case "hedged":
    case "completed":
      return "success";
    case "accepted":
    case "submitted":
    case "pending":
    case "working":
    case "creating":
    case "rotating":
    case "draining":
      return "info";
    case "uncertain":
    case "degraded":
    case "partially_filled":
    case "partially_hedged":
    case "recovering":
    case "read_only":
      return "warning";
    case "manual_intervention":
    case "blocked":
    case "denied":
    case "rejected":
    case "failed":
    case "unavailable":
      return "danger";
    default:
      return "neutral";
  }
}
