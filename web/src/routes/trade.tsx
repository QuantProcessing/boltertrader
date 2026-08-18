import { useMemo, useState } from "react";

import type { CandleInterval, Instrument } from "@/api/types";
import { Blotter } from "@/features/trade/blotter";
import { OrderTicket } from "@/features/trade/order-ticket";
import { InstrumentStrip } from "@/features/market/instrument-strip";
import { OrderBook } from "@/features/market/order-book";
import { PriceChart } from "@/features/market/price-chart";
import {
  marketOf,
  tradableAccounts,
  useAccounts,
  useCandles,
  useInstruments,
  useMarketFact,
} from "@/features/market/queries";

/* ---------------------------------------------------------------------------
 * The trading terminal.
 *
 * One instrument, chosen once at the top, drives every panel below it. That is
 * the whole reason this is a page rather than four independent widgets: the
 * chart, the book and the ticket must be looking at the same market, and the
 * cheapest way to guarantee that is to give them no way to disagree.
 *
 * Layout follows the shape every terminal has converged on, because operators
 * arrive already knowing it: price on the left with the record of what happened
 * beneath it, the live book beside the price, and the ticket at the edge where
 * the hand rests.
 * ------------------------------------------------------------------------- */

const DEFAULT_INTERVAL: CandleInterval = "15m";
const CHART_BARS = 300;

export function TradePage() {
  const instruments = useInstruments();
  const accounts = useAccounts();

  const [selected, setSelected] = useState<Instrument | null>(null);
  const [interval, setInterval] = useState<CandleInterval>(DEFAULT_INTERVAL);
  // Lifted out of the ticket so a click in the book can write into it.
  const [limitPrice, setLimitPrice] = useState("");

  const catalog = useMemo(() => instruments.data?.items ?? [], [instruments.data]);

  // Falling back to the first tradable instrument rather than leaving the page
  // empty: an operator who opened the terminal wants to see a market, and the
  // catalog's own order is the venue's, which is close enough to sensible.
  const instrument = selected ?? catalog.find((candidate) => candidate.ready) ?? catalog[0] ?? null;

  const query = instrument ? marketOf(instrument) : null;
  const fact = useMarketFact(query);
  const candles = useCandles(query ? { ...query, interval, limit: CHART_BARS } : null);
  const today = useCandles(query ? { ...query, interval: "1d", limit: 1 } : null);

  const eligible = tradableAccounts(accounts.data ?? [], instrument);

  return (
    <div className="flex min-h-0 flex-1 flex-col">
      <InstrumentStrip
        instruments={catalog}
        instrument={instrument}
        onSelect={(next) => {
          setSelected(next);
          // The old instrument's price is meaningless against the new one, and
          // leaving it in the ticket is how a fat-finger order gets placed.
          setLimitPrice("");
        }}
        fact={fact.data?.value}
        today={today.data?.value}
        budget={fact.data?.rateLimit}
        loading={instruments.isPending}
      />

      <div className="flex min-h-0 flex-1">
        <div className="flex min-w-0 flex-1 flex-col">
          <PriceChart candles={candles.data?.value.candles} interval={interval} onIntervalChange={setInterval} />
          <div className="flex h-deck shrink-0 flex-col border-t border-line">
            <Blotter instrumentId={instrument?.instrumentId ?? null} />
          </div>
        </div>

        <div className="flex w-side shrink-0 flex-col border-l border-line">
          <OrderBook
            fact={fact.data?.value}
            instrument={instrument}
            quoteAsset={instrument?.quoteAsset}
            baseAsset={instrument?.baseAsset}
            onPickPrice={setLimitPrice}
          />
        </div>

        <div className="w-ticket shrink-0 overflow-y-auto border-l border-line p-3">
          <OrderTicket
            instrument={instrument}
            accounts={eligible}
            limitPrice={limitPrice}
            onLimitPriceChange={setLimitPrice}
          />
        </div>
      </div>
    </div>
  );
}
