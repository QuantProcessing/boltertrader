import { beforeEach, expect, it } from "vitest";
import { advanceSessionEpoch } from "@/api/client";
import type { Command, Fill } from "@/api/types";
import { mergeCommands, mergeFills } from "./order-snapshot";
beforeEach(() => advanceSessionEpoch());
const command = {
  commandId: "c",
  createdAt: "2026-09-10T00:00:00Z",
  updatedAt: "2026-09-10T00:00:00Z",
  state: "submitted",
  executionEvidence: { resourceVersion: 1 },
} as Command;
it("a delayed REST result cannot roll a filled order back to submitted", () => {
  mergeCommands([
    {
      ...command,
      state: "filled",
      updatedAt: "2026-09-10T00:00:01Z",
      executionEvidence: { resourceVersion: 2 },
    },
  ]);
  expect(mergeCommands([command])[0]?.state).toBe("filled");
});
it("never rolls a terminal command back even with equal evidence timestamps", () => {
  mergeCommands([{ ...command, state: "filled" }]);
  expect(mergeCommands([command])[0]?.state).toBe("filled");
});
it("deduplicates fills by command and fill identity, then clears on logout", () => {
  const fill = {
    fillId: "f",
    commandId: "c",
    occurredAt: "2026-09-10T00:00:00Z",
  } as Fill;
  mergeFills([fill]);
  expect(mergeFills([fill])).toHaveLength(1);
  advanceSessionEpoch();
  expect(mergeFills([])).toEqual([]);
  expect(mergeCommands([])).toEqual([]);
});
