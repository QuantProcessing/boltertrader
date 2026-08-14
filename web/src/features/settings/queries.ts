import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { accounts, manual, trading, type CreateAccountRequest } from "@/api/endpoints";
import { ApiError } from "@/api/errors";
import type { Account, AccountRuntime, CredentialBundle, Venue } from "@/api/types";
import { marketKeys } from "@/features/market/queries";

/* ---------------------------------------------------------------------------
 * The account lifecycle.
 *
 * Every mutation but create carries `If-Match`, built from the revision the
 * caller has already read. That makes a stale tab's write a 409 rather than a
 * silent overwrite of somebody else's change — so each hook takes the whole
 * `Account` it is acting on, never just an id, and the revision travels with
 * it. On a conflict the account list is refetched: the resolution is to look
 * again, not to retry the same write against the same revision.
 *
 * Create, rotate and remove also carry an `Idempotency-Key`, because each
 * writes credential material or destroys it and a retried request must not do
 * it twice.
 * ------------------------------------------------------------------------- */

export const settingsKeys = {
  venues: () => ["manual", "venues"] as const,
  runtimes: (accountId: string) => ["trading", "account-runtimes", accountId] as const,
};

/**
 * The venue catalog: credential schema and route profiles, which is everything
 * needed to build a create form without hardcoding a venue's field names.
 */
export function useVenues(): UseQueryResult<{ revision: string; items: Venue[] }> {
  return useQuery({
    queryKey: settingsKeys.venues(),
    queryFn: ({ signal }) => manual.venues(signal),
    staleTime: 5 * 60_000,
  });
}

/**
 * Trading's view of one account's runtime — readiness, capabilities, and the
 * generations that coexist during a rotation. Not the gateway's record.
 *
 * Five seconds because this is what an operator watches after acting: a
 * rotation or a drain is applied asynchronously and this is where it lands.
 */
export function useAccountRuntimes(accountId: string | null): UseQueryResult<AccountRuntime[]> {
  return useQuery({
    queryKey: settingsKeys.runtimes(accountId ?? ""),
    queryFn: ({ signal }) => trading.accountRuntimes(accountId as string, signal),
    enabled: accountId !== null,
    refetchInterval: 5_000,
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

/** True when the server refused because the caller's revision was stale. */
export function isRevisionConflict(error: unknown): boolean {
  return error instanceof ApiError && error.code === "DESIRED_STATE_REVISION_CONFLICT";
}

function useLifecycleMutation<TVariables>(
  run: (variables: TVariables) => Promise<Account>,
  accountIdOf: (variables: TVariables) => string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: run,
    // Settled rather than success: a conflict is exactly the case where the
    // cached revision is known to be wrong, so it must be refetched too.
    onSettled: (_account, _error, variables) => {
      void queryClient.invalidateQueries({ queryKey: marketKeys.accounts() });
      void queryClient.invalidateQueries({ queryKey: settingsKeys.runtimes(accountIdOf(variables)) });
    },
  });
}

export function useCreateAccount() {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: (request: CreateAccountRequest): Promise<Account> => accounts.create(request, freshId()),
    onSuccess: () => {
      void queryClient.invalidateQueries({ queryKey: marketKeys.accounts() });
    },
  });
}

export function useValidateAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.validate(account.accountId, account.desiredStateRevision),
    (account) => account.accountId,
  );
}

export function useDisableAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.disable(account.accountId, account.desiredStateRevision),
    (account) => account.accountId,
  );
}

export function useDrainAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.drain(account.accountId, account.desiredStateRevision),
    (account) => account.accountId,
  );
}

export function useRemoveAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.remove(account.accountId, account.desiredStateRevision, freshId()),
    (account) => account.accountId,
  );
}

/**
 * Rotation is not atomic and the UI must not pretend otherwise: the account
 * keeps serving on the active credential while the new one proves itself, and
 * `useAccountRuntimes` is where both generations are visible.
 */
export function useRotateCredential() {
  return useLifecycleMutation(
    ({ account, credentialBundle }: { account: Account; credentialBundle: CredentialBundle }) =>
      accounts.rotate(account.accountId, account.desiredStateRevision, credentialBundle, freshId()),
    ({ account }) => account.accountId,
  );
}
