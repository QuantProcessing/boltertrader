import { watchSessionEpoch } from "@/api/client";
import type { Command, Fill } from "@/api/types";

const commands = new Map<string, Command>();
const fills = new Map<string, Fill>();
watchSessionEpoch(() => {
  commands.clear();
  fills.clear();
});

/** A late REST read may fill gaps but cannot roll back a newer persisted fact. */
export function mergeCommands(incoming: Command[]): Command[] {
  for (const command of incoming) {
    const current = commands.get(command.commandId);
    if (current && terminal(current) && !terminal(command)) continue;
    if (
      !current ||
      Date.parse(command.updatedAt) > Date.parse(current.updatedAt) ||
      (Date.parse(command.updatedAt) === Date.parse(current.updatedAt) &&
        (command.executionEvidence?.resourceVersion ?? 0) >=
          (current.executionEvidence?.resourceVersion ?? 0))
    ) {
      commands.set(command.commandId, command);
    }
  }
  return [...commands.values()].sort(
    (a, b) => Date.parse(b.createdAt) - Date.parse(a.createdAt),
  );
}

function terminal(command: Command): boolean {
  return (
    command.state === "filled" ||
    command.state === "canceled" ||
    command.state === "failed"
  );
}
export function mergeFills(incoming: Fill[]): Fill[] {
  for (const fill of incoming)
    fills.set(`${fill.commandId}\0${fill.fillId}`, fill);
  return [...fills.values()].sort(
    (a, b) => Date.parse(b.occurredAt) - Date.parse(a.occurredAt),
  );
}
