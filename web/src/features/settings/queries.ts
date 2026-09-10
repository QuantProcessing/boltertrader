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
 * Connection health is independent of exchange API permissions.
 */
export function useAccountRuntimes(accountId: string | null): UseQueryResult<AccountRuntime[]> {
  return useQuery({
    queryKey: settingsKeys.runtimes(accountId ?? ""),
    queryFn: ({ signal }) => trading.accountRuntimes(accountId as string, signal),
    enabled: accountId !== null,
    meta: PRIVATE_QUERY_META,
    refetchInterval: 60_000,
  });
}

function freshId(): string {
  return crypto.randomUUID();
}

export function useCreateAccount() {
  const queryClient = useQueryClient();
  return useMutation({
    gcTime: 0,
    onSettled: (_data, _error, request) => {
      for (const key of Object.keys(request.credentialBundle.secrets)) delete request.credentialBundle.secrets[key];
    },
    mutationFn: (request: CreateAccountRequest): Promise<Account> => accounts.create(request, freshId()),
    onSuccess: (account) => {
      // The response already represents a saved and validated connection.
      queryClient.setQueryData<Account[]>(marketKeys.accounts(), (current = []) => {
        const without = current.filter((item) => item.accountId !== account.accountId);
        return [...without, account];
      });
      void queryClient.invalidateQueries({ queryKey: marketKeys.accounts() });
    },
  });
}

export function useRemoveAccount() {
 const queryClient = useQueryClient();
 return useMutation({
  mutationFn: (account: Account) => accounts.remove(account.accountId),
  onSuccess: (_result, account) => {
   queryClient.setQueryData<Account[]>(marketKeys.accounts(), (current = []) => current.filter(item => item.accountId !== account.accountId));
   queryClient.removeQueries({queryKey: settingsKeys.runtimes(account.accountId)});
   void queryClient.invalidateQueries({queryKey: marketKeys.accounts()});
   void queryClient.invalidateQueries({queryKey: ["trading", "account-positions"]});
   void queryClient.invalidateQueries({queryKey: ["trading", "portfolio-positions"]});
  },
 });
}
