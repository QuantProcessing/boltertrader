import { afterEach, beforeEach, expect, it, vi } from "vitest";
import { advanceSessionEpoch } from "@/api/client";
import type { LedgerEntry } from "@/api/types";
import { recordLedgerSnapshot, selectLedger } from "./ledger-snapshot";

beforeEach(() => {
  advanceSessionEpoch();
  vi.useFakeTimers();
  vi.setSystemTime(2000);
});
afterEach(() => vi.useRealTimers());

it("keeps a newer pushed ledger over a read already in flight", () => {
  const fresh = { entryId: "new", accountId: "a" } as LedgerEntry;
  recordLedgerSnapshot({ ledger: [fresh] });
  expect(selectLedger([], { accountId: "a" }, 1000)).toEqual([fresh]);
  expect(selectLedger([], { accountId: "b" }, 1000)).toEqual([]);
});

it("allows a later REST repair to replace a snapshot retained through disconnection", () => {
  recordLedgerSnapshot({ ledger: [{ entryId: "old" } as LedgerEntry] });
  const repaired = [{ entryId: "new" } as LedgerEntry];
  expect(selectLedger(repaired, {}, 3000)).toEqual(repaired);
  advanceSessionEpoch();
  expect(selectLedger([])).toEqual([]);
});
