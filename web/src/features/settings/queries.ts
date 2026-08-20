import { useMutation, useQuery, useQueryClient, type UseQueryResult } from "@tanstack/react-query";

import { accounts, manual, trading, type CreateAccountRequest } from "@/api/endpoints";
import type { Account, AccountRuntime, Venue } from "@/api/types";
import { PRIVATE_QUERY_META } from "@/app/query-privacy";
import { marketKeys } from "@/features/market/queries";

/* Account credentials are immutable: create and remove are idempotent, and a
 * replacement is always remove followed by a fresh create. */

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
    meta: PRIVATE_QUERY_META,
    staleTime: 5 * 60_000,
  });
}

/**
 * Trading's view of one account's connection readiness and capabilities.
 * Two seconds because creation and removal are asynchronous.
 */
export function useAccountRuntimes(accountId: string | null): UseQueryResult<AccountRuntime[]> {
  return useQuery({
    queryKey: settingsKeys.runtimes(accountId ?? ""),
    queryFn: ({ signal }) => trading.accountRuntimes(accountId as string, signal),
    enabled: accountId !== null,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 2_000,
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

function useLifecycleMutation<TVariables>(
  run: (variables: TVariables) => Promise<Account>,
  accountIdOf: (variables: TVariables) => string,
) {
  const queryClient = useQueryClient();
  return useMutation({
    mutationFn: run,
    // Refetch after both success and failure so asynchronous lifecycle changes
    // and server-side blockers are reflected immediately.
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
    onSuccess: (account) => {
      // Put the accepted creating record on screen immediately. The lifecycle
      // poll then replaces it with the authoritative active/failed state; the
      // form callback can safely select it without waiting for another GET.
      queryClient.setQueryData<Account[]>(marketKeys.accounts(), (current = []) => {
        const without = current.filter((item) => item.accountId !== account.accountId);
        return [...without, account];
      });
      void queryClient.invalidateQueries({ queryKey: marketKeys.accounts() });
    },
  });
}

export function useDisableAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.disable(account.accountId),
    (account) => account.accountId,
  );
}

export function useRemoveAccount() {
  return useLifecycleMutation(
    (account: Account) => accounts.remove(account.accountId, freshId()),
    (account) => account.accountId,
  );
}
