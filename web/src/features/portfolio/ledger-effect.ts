import type { LedgerEntry } from "@/api/types";
import { decimalSign } from "@/lib/format";

/** Signed earnings effect: a positive fee is a cost, a negative fee a rebate. */
export function ledgerEffect(entry: Pick<LedgerEntry, "kind" | "amount" | "accountingStatus">): string | undefined {
  if (entry.accountingStatus && entry.accountingStatus !== "ready") return undefined;
  if (entry.kind !== "fee") return entry.amount;
  if (decimalSign(entry.amount) === 0) return "0";
  return entry.amount.startsWith("-") ? entry.amount.slice(1) : `-${entry.amount.replace(/^\+/, "")}`;
}
