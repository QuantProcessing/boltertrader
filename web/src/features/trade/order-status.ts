import type { Command } from "@/api/types";
import { compactIdentifier } from "@/lib/format";
import type { Key } from "@/lib/i18n";

type Translator = (key: Key, values?: Record<string, string | number>) => string;

/** The safe venue response already attached to a failed command. */
export function commandFailureReason(command: Command, t: Translator): string {
  if (command.blockers.some((blocker) => blocker.code === "HANDOFF_NOT_STARTED")) {
    return t("ticket.failure.handoffNotStarted");
  }
  const code = command.executionEvidence?.venueCode?.trim() ?? "";
  const message = command.executionEvidence?.venueMessage?.trim() ?? "";
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

export function describeCommandFailure(command: Command, t: Translator): string {
  return t("ticket.failure.detail", {
    reason: commandFailureReason(command, t),
    commandId: compactIdentifier(command.commandId),
  });
}

/** Recovery diagnostics come from the persisted task, never inferred from a timeout. */
export function commandUncertaintyReason(command: Command, t: Translator): string {
  const reasonKeys: Record<string, Key> = {
    ambiguous_handoff: "order.recovery.ambiguous",
    lookup_failed: "order.recovery.lookupFailed",
    evidence_invalid: "order.recovery.invalidEvidence",
    awaiting_terminal_evidence: "order.recovery.awaitingTerminal",
    awaiting_handoff: "order.recovery.awaitingHandoff",
  };
  const recovery = command.reconciliation;
  const evidence = command.executionEvidence;
  const parts = [t(reasonKeys[recovery?.reason ?? ""] ?? "order.recovery.unknown"), t("order.recovery.readOnly")];
  parts.push(t("order.recovery.identity", { commandId: command.commandId }));
  if (evidence?.gatewayOrderId) parts.push(t("order.recovery.order", { orderId: evidence.gatewayOrderId }));
  if (evidence?.state) parts.push(t("order.recovery.evidence", { state: evidence.state, version: evidence.resourceVersion ?? "?" }));
  if (recovery) parts.push(t("order.recovery.attempts", { count: recovery.attemptCount }));
  parts.push(t("order.recovery.updated", { time: command.updatedAt }));
  return parts.join(" · ");
}
