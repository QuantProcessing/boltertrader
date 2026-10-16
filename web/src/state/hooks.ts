import { useQuery } from "@tanstack/react-query";
import { useEffect, useState } from "react";

import { api } from "@/api/client";
import { realtime, type ConnectionState, type Message, type Topic } from "@/api/realtime";
import type { AccountStatus, Balance, Book, Candle, Fill, Instrument, Order, Position, Ticker, Trade } from "@/api/types";

/** Latest data of a retained channel. Resets when the topic changes. */
function useChannel<T>(topic: Topic | null, channel: string): T | undefined {
  const [value, setValue] = useState<T | undefined>(undefined);
  const key = topic ? JSON.stringify(topic) : "";
  useEffect(() => {
    setValue(undefined);
    if (!topic) return;
    return realtime.subscribe(topic, (message: Message) => {
      if (message.ch === channel) setValue(message.data as T);
    });
    // eslint-disable-next-line react-hooks/exhaustive-deps
  }, [key, channel]);
  return value;
}

export function useConnection(): ConnectionState {
  const [state, setState] = useState<ConnectionState>("offline");
  useEffect(() => realtime.onState(setState), []);
  return state;
}

export function useInstruments() {
  return useQuery({ queryKey: ["instruments"], queryFn: api.instruments, staleTime: 10 * 60_000 });
}

export function useInstrument(id: string): Instrument | undefined {
  const { data } = useInstruments();
  return data?.find((item) => item.id === id);
}

export function useBook(inst: string) {
  return useChannel<Book>(inst ? { ch: "book", inst } : null, "book");
}

export function useTicker(inst: string, perp: boolean) {
  return useChannel<Ticker>(inst && perp ? { ch: "ticker", inst } : null, "ticker");
}

export function useStats(inst: string) {
  return useQuery({ queryKey: ["stats", inst], queryFn: ({ signal }) => api.stats(inst, signal), enabled: !!inst, refetchInterval: 30_000 });
}

export function useTrades(inst: string, limit = 60): Trade[] {
  const [trades, setTrades] = useState<Trade[]>([]);
  useEffect(() => {
    setTrades([]);
    if (!inst) return;
    return realtime.subscribe({ ch: "trades", inst }, (message) => {
      if (message.ch !== "trades") return;
      const incoming = message.data as Trade[];
      setTrades((current) => [...incoming.slice().reverse(), ...current].slice(0, limit));
    });
  }, [inst, limit]);
  return trades;
}

/** Live last trade price, falling back to the 24h stats. */
export function useLastPrice(inst: string): string | undefined {
  const trades = useTrades(inst, 1);
  const stats = useStats(inst);
  return trades[0]?.p ?? stats.data?.last;
}

export function useCandleStream(inst: string, interval: string, onCandle: (candle: Candle) => void) {
  useEffect(() => {
    if (!inst) return;
    return realtime.subscribe({ ch: "candle", inst, interval }, (message) => {
      if (message.ch === "candle") onCandle(message.data as Candle);
    });
  }, [inst, interval, onCandle]);
}

export interface AccountLive {
  positions?: Position[];
  balances?: Balance[];
  status?: AccountStatus;
}

/**
 * Account stream: positions and balances arrive as complete lists and simply
 * replace state; order and fill events are forwarded to the callbacks.
 */
export function useAccountStream(
  accountId: string,
  onOrder?: (order: Order) => void,
  onFill?: (fill: Fill) => void,
): AccountLive {
  const [live, setLive] = useState<AccountLive>({});
  useEffect(() => {
    setLive({});
    if (!accountId) return;
    return realtime.subscribe({ ch: "account", account: accountId }, (message) => {
      switch (message.ch) {
        case "account.positions":
          setLive((current) => ({ ...current, positions: message.data as Position[] }));
          break;
        case "account.balances":
          setLive((current) => ({ ...current, balances: message.data as Balance[] }));
          break;
        case "account.status":
          setLive((current) => ({ ...current, status: message.data as AccountStatus }));
          break;
        case "account.orders":
          onOrder?.(message.data as Order);
          break;
        case "account.fills":
          onFill?.(message.data as Fill);
          break;
      }
    });
  }, [accountId, onOrder, onFill]);
  return live;
}

export function useNow(intervalMs = 1000): number {
  const [now, setNow] = useState(Date.now());
  useEffect(() => {
    const timer = setInterval(() => setNow(Date.now()), intervalMs);
    return () => clearInterval(timer);
  }, [intervalMs]);
  return now;
}
