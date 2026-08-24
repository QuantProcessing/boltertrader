import type { Command } from "@/api/types";
import { compactIdentifier } from "@/lib/format";
import type { Key } from "@/lib/i18n";

const BINANCE_FAILURE_KEYS: Record<string, Key> = {
  "-2019": "ticket.failure.insufficientMargin",
  "-4164": "ticket.failure.minimumNotional",
  "-2022": "ticket.failure.reduceOnly",
  "-1111": "ticket.failure.precision",
  "-2027": "ticket.failure.leverage",
};

type Translator = (key: Key, values?: Record<string, string | number>) => string;

/** The safe venue response already attached to a failed command. */
export function commandFailureReason(command: Command, venue: string, t: Translator): string {
  const code = command.executionEvidence?.venueCode?.trim() ?? "";
  const message = command.executionEvidence?.venueMessage?.trim() ?? "";
  const mapped = venue.toLowerCase() === "binance" ? BINANCE_FAILURE_KEYS[code] : undefined;
  if (mapped) return t(mapped, { code });
  if (message && code) return t("ticket.failure.venue", { message, code });
  if (message) return message;
  if (code) return t("ticket.failure.venueCode", { code });
  if (command.blockers.length > 0) {
    return t("blotter.blocked", { codes: command.blockers.map((blocker) => blocker.code).join(", ") });
  }
  if (command.executionEvidence?.state && command.executionEvidence.state !== "rejected") {
    return t("ticket.failure.state", { state: command.executionEvidence.state });
  }
  return t("ticket.failure.unknown");
}

export function describeCommandFailure(command: Command, venue: string, t: Translator): string {
  return t("ticket.failure.detail", {
    reason: commandFailureReason(command, venue, t),
    commandId: compactIdentifier(command.commandId),
  });
}

export function venueOfCommand(command: Command): string {
  const payload = command.payload;
  if (typeof payload !== "object" || payload === null || !("instrumentId" in payload)) return "";
  const instrumentId = String((payload as { instrumentId?: unknown }).instrumentId ?? "");
  return instrumentId.includes(":") ? instrumentId.slice(0, instrumentId.indexOf(":")) : "";
}
