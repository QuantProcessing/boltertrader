import { useQuery, useQueryClient } from "@tanstack/react-query";
import { createContext, useCallback, useContext, useEffect, useMemo, useState, type ReactNode } from "react";

import { api } from "@/api/client";
import type { Account, Fill, Instrument, Order } from "@/api/types";
import { useAccountStream, useInstruments, type AccountLive } from "./hooks";
import { useToast } from "@/ui/toast";
import { instrumentName, num, venueLabel } from "@/lib/format";

const DEFAULT_INSTRUMENT = "binance:perp:BTC-USDT";

interface Workspace {
  instrumentId: string;
  setInstrumentId: (id: string) => void;
  accounts: Account[];
  account: Account | undefined;
  setAccountId: (id: string) => void;
  live: AccountLive;
  openOrders: Order[];
  fills: Fill[];
  limitPriceHint: { price: string; at: number } | null;
  hintLimitPrice: (price: string) => void;
  connectOpen: boolean;
  setConnectOpen: (open: boolean) => void;
}

const WorkspaceContext = createContext<Workspace | null>(null);

function stored(key: string, fallback: string): string {
  try {
    return localStorage.getItem(key) ?? fallback;
  } catch {
    return fallback;
  }
}

const VENUE_ORDER = ["binance", "okx", "bybit", "hyperliquid", "bitget", "gate", "lighter"];

/**
 * When the saved instrument is not in the catalog (its venue is unreachable
 * or the market was delisted), show the same base and product elsewhere,
 * else the first BTC perp. The saved choice is kept, so it comes back once
 * its venue does.
 */
export function fallbackInstrument(id: string, instruments: Instrument[]): Instrument | undefined {
  if (instruments.length === 0 || instruments.some((item) => item.id === id)) return undefined;
  const [, product = "perp", symbol = ""] = id.split(":");
  const base = symbol.split("-")[0]?.toUpperCase() ?? "";
  const rank = (item: Instrument) => {
    const venue = VENUE_ORDER.indexOf(item.venue);
    return (venue < 0 ? VENUE_ORDER.length : venue) + (item.quote.toUpperCase() === "USDT" ? 0 : 0.5);
  };
  const pick = (match: (item: Instrument) => boolean) =>
    instruments.filter(match).sort((a, b) => rank(a) - rank(b) || a.symbol.localeCompare(b.symbol))[0];
  return pick((item) => item.product === product && item.base.toUpperCase() === base && !item.symbol.includes(":"))
    ?? pick((item) => item.product === "perp" && item.base.toUpperCase() === "BTC")
    ?? instruments[0];
}

function terminal(status: Order["status"]) {
  return status === "filled" || status === "canceled" || status === "rejected";
}

export function WorkspaceProvider({ children }: { children: ReactNode }) {
  const toast = useToast();
  const queryClient = useQueryClient();
  const [savedInstrument, setInstrument] = useState(() => stored("bolter.instrument", DEFAULT_INSTRUMENT));
  const { data: instruments } = useInstruments();
  const fallback = useMemo(() => fallbackInstrument(savedInstrument, instruments ?? []), [savedInstrument, instruments]);
  const instrumentId = fallback?.id ?? savedInstrument;
  const [accountId, setAccount] = useState(() => stored("bolter.account", ""));
  const [limitPriceHint, setHint] = useState<{ price: string; at: number } | null>(null);
  const [connectOpen, setConnectOpen] = useState(false);

  const accountsQuery = useQuery({ queryKey: ["accounts"], queryFn: api.accounts });
  const accounts = accountsQuery.data ?? [];
  const account = accounts.find((item) => item.id === accountId) ?? accounts[0];

  const setInstrumentId = useCallback((id: string) => {
    setInstrument(id);
    localStorage.setItem("bolter.instrument", id);
  }, []);
  const setAccountId = useCallback((id: string) => {
    setAccount(id);
    localStorage.setItem("bolter.account", id);
  }, []);

  // Open orders and fills: one REST snapshot per account, then stream events.
  const ordersQuery = useQuery({ queryKey: ["orders", account?.id], queryFn: () => api.openOrders(account!.id), enabled: !!account, refetchInterval: 60_000 });
  const fillsQuery = useQuery({ queryKey: ["fills", account?.id], queryFn: () => api.fills(account!.id), enabled: !!account, refetchInterval: 120_000 });

  const onOrder = useCallback((order: Order) => {
    queryClient.setQueryData<Order[]>(["orders", account?.id], (current = []) => {
      const rest = current.filter((item) => item.orderId !== order.orderId);
      return terminal(order.status) ? rest : [order, ...rest];
    });
  }, [account?.id, queryClient]);

  const onFill = useCallback((fill: Fill) => {
    queryClient.setQueryData<Fill[]>(["fills", account?.id], (current = []) =>
      current.some((item) => item.fillId === fill.fillId) ? current : [fill, ...current].slice(0, 200));
    toast.push({
      kind: fill.side === "buy" ? "long" : "short",
      title: `${fill.side === "buy" ? "Bought" : "Sold"} ${num(fill.qty)} ${instrumentName(fill.inst)}`,
      detail: `at ${num(fill.price)}`,
    });
  }, [account?.id, queryClient, toast]);

  const live = useAccountStream(account?.id ?? "", onOrder, onFill);

  useEffect(() => {
    if (fallback) toast.push({ kind: "info", title: `${instrumentName(savedInstrument)} is unavailable`, detail: `${venueLabel(savedInstrument.split(":")[0] ?? "")} cannot be reached from this network. Showing ${fallback.symbol} on ${venueLabel(fallback.venue)}.` });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [fallback?.id, savedInstrument]);

  useEffect(() => {
    if (accountId && accounts.length && !accounts.some((item) => item.id === accountId)) setAccount("");
  }, [accountId, accounts]);

  const value = useMemo<Workspace>(() => ({
    instrumentId, setInstrumentId, accounts, account, setAccountId, live,
    openOrders: ordersQuery.data ?? [], fills: fillsQuery.data ?? [],
    limitPriceHint, hintLimitPrice: (price) => setHint({ price, at: Date.now() }),
    connectOpen, setConnectOpen,
  }), [instrumentId, setInstrumentId, accounts, account, setAccountId, live, ordersQuery.data, fillsQuery.data, limitPriceHint, connectOpen]);

  return <WorkspaceContext.Provider value={value}>{children}</WorkspaceContext.Provider>;
}

export function useWorkspace(): Workspace {
  const value = useContext(WorkspaceContext);
  if (!value) throw new Error("WorkspaceProvider missing");
  return value;
}
