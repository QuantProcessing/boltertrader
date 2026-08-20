import { describe, expect, it } from "vitest";

import type { Account, AccountStatus } from "@/api/types";
import { accountLifecyclePollInterval } from "@/features/market/queries";

function account(status: AccountStatus): Account {
  return { status } as Account;
}

describe("accountLifecyclePollInterval", () => {
  it.each(["creating", "removing"] as const)(
    "polls while an account is %s",
    (status) => {
      expect(accountLifecyclePollInterval([account(status)])).toBe(2_000);
    },
  );

  it("stops polling after every account reaches a stable state", () => {
    expect(accountLifecyclePollInterval([account("active"), account("disabled")])).toBe(false);
    expect(accountLifecyclePollInterval(undefined)).toBe(false);
  });
});
