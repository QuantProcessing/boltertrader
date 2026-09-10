import { expect, it } from "vitest";
import type { Command } from "@/api/types";
import { commandUncertaintyReason } from "./order-status";
import { en } from "@/lib/i18n/en";
import type { Translate } from "@/lib/i18n";

it("shows persisted lookup diagnostics without claiming the order failed", () => {
  const t: Translate = (key, values) => en[key].replace(/\{(\w+)\}/g, (_, name: string) => String(values?.[name] ?? ""));
  const command = { commandId: "c", state: "uncertain", updatedAt: "2026-09-10T10:00:00Z",
    reconciliation: { reason: "lookup_failed", attemptCount: 12, nextAttemptAt: "2026-09-10T10:01:00Z" },
    executionEvidence: { gatewayOrderId: "g", state: "partially_filled", resourceVersion: 4 },
  } as Command;
  const text = commandUncertaintyReason(command, t);
  expect(text).toContain("latest order lookup failed");
  expect(text).toContain("only queries");
  expect(text).toContain("partially_filled, version 4");
  expect(text).toContain("12");
  expect(text).toContain(command.updatedAt);
});
