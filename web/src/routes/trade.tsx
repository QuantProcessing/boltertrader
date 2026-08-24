import { useEffect, useMemo, useState } from "react";

import type { CandleInterval, Instrument } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { Blotter, ReadOnlyBlotter } from "@/features/trade/blotter";
import { OrderTicket } from "@/features/trade/order-ticket";
import { useAccountPositions } from "@/features/trade/queries";
import { useTradeRealtime } from "@/features/trade/realtime";
import { InstrumentStrip } from "@/features/market/instrument-strip";
import { OrderBook } from "@/features/market/order-book";
import { PriceChart } from "@/features/market/price-chart";
import { usePublicMarketStreams } from "@/features/market/stream";
import { decimalsOf, midpointPrice } from "@/features/market/precision";
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
  const readOnly = useReadOnly();
  const instruments = useInstruments();
  const accounts = useAccounts();

  const [selected, setSelected] = useState<Instrument | null>(null);
  const [interval, setInterval] = useState<CandleInterval>(DEFAULT_INTERVAL);
  // Lifted out of the ticket so a click in the book can write into it.
  const [limitPrice, setLimitPrice] = useState("");
  const [accountId, setAccountId] = useState("");

  const catalog = useMemo(() => instruments.data?.items ?? [], [instruments.data]);

  // Open on a liquid, recognisable public market. Catalog order is grouped by
  // connector rather than by usefulness, so taking its first row made the
  // terminal routinely open on a thin altcoin with no depth and made a healthy
  // read-only gateway look empty.
  const instrument = selected ?? preferredInstrument(catalog);

  const query = instrument ? marketOf(instrument) : null;
  const fact = useMarketFact(query);
  const candles = useCandles(query ? { ...query, interval, limit: CHART_BARS } : null);
  const today = useCandles(query ? { ...query, interval: "1d", limit: 1 } : null);
  const stream = usePublicMarketStreams(instrument?.instrumentId, interval, instrument?.product);

  // Disabling a private query does not erase a previously cached response.
  // Filter at the write surface as well so signing out cannot retain an account
  // (or a sealed preview) from the authenticated session.
  const eligible = useMemo(
    () => readOnly ? [] : tradableAccounts(accounts.data ?? [], instrument),
    [accounts.data, instrument, readOnly],
  );
  useEffect(() => {
    if (eligible.some((account) => account.accountId === accountId)) return;
    setAccountId(eligible[0]?.accountId ?? "");
  }, [accountId, eligible]);

  const accountPositions = useAccountPositions(accountId, instrument?.instrumentId ?? "");
  useTradeRealtime(!readOnly);
  const restFact = fact.data?.value;
  const marketFact = useMemo(() => {
    if (!stream.book) return restFact;
    return {
      venue: instrument?.venue ?? restFact?.venue ?? "",
      environment: instrument?.environment ?? restFact?.environment ?? "live",
      product: instrument?.product ?? restFact?.product ?? "",
      instrumentId: instrument?.instrumentId ?? restFact?.instrumentId ?? "",
      bestBid: stream.book.bids[0]?.price ?? restFact?.bestBid ?? "",
      bestBidSize: stream.book.bids[0]?.quantity ?? restFact?.bestBidSize ?? "",
      bestAsk: stream.book.asks[0]?.price ?? restFact?.bestAsk ?? "",
      bestAskSize: stream.book.asks[0]?.quantity ?? restFact?.bestAskSize ?? "",
      bids: stream.book.bids,
      asks: stream.book.asks,
      markPrice: stream.mark?.price ?? restFact?.markPrice,
      fundingRate: restFact?.fundingRate,
      openInterest: restFact?.openInterest,
      fundingObservedAt: restFact?.fundingObservedAt,
      fundingTime: restFact?.fundingTime,
      nextFundingTime: restFact?.nextFundingTime,
      observedAt: stream.mark?.observedAt ?? stream.book.observedAt,
    };
  }, [instrument, restFact, stream.book, stream.mark]);
  const midPrice = midpointPrice(
    marketFact?.bestBid,
    marketFact?.bestAsk,
    decimalsOf(instrument?.rules.priceIncrement),
  );

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
        fact={marketFact}
        today={today.data?.value}
        budget={fact.data?.rateLimit}
        loading={instruments.isPending}
      />

      <div className="flex min-h-0 flex-1">
        <div className="flex min-w-0 flex-1 flex-col">
          <PriceChart
            candles={candles.data?.value.candles}
            liveCandle={stream.candle}
            instrument={instrument}
            livePrice={midPrice}
            positions={accountPositions.data ?? []}
            interval={interval}
            onIntervalChange={setInterval}
          />
          <div className="flex h-deck shrink-0 flex-col border-t border-line">
            {readOnly ? (
              <ReadOnlyBlotter instrumentId={instrument?.instrumentId ?? null} />
            ) : (
              <Blotter
                instrumentId={instrument?.instrumentId ?? null}
                positions={accountPositions.data ?? []}
                positionsLoading={accountPositions.isFetching && accountPositions.data === undefined}
                positionsFetching={accountPositions.isFetching}
                positionsError={accountPositions.isError}
                positionsUpdatedAt={accountPositions.dataUpdatedAt}
                liveMarkPrice={marketFact?.markPrice}
              />
            )}
          </div>
        </div>

        <div className="flex w-side shrink-0 flex-col border-l border-line">
          <OrderBook
            fact={marketFact}
            midPrice={midPrice}
            instrument={instrument}
            quoteAsset={instrument?.quoteAsset}
            baseAsset={instrument?.baseAsset}
            unavailable={fact.isError}
            onPickPrice={setLimitPrice}
          />
        </div>

        <div className="w-ticket shrink-0 overflow-y-auto border-l border-line bg-panel">
          <OrderTicket
            instrument={instrument}
            accounts={eligible}
            accountId={accountId}
            onAccountIdChange={setAccountId}
            positions={accountPositions.data ?? []}
            limitPrice={limitPrice}
            onLimitPriceChange={setLimitPrice}
            bestBid={marketFact?.bestBid}
            bestAsk={marketFact?.bestAsk}
          />
        </div>
      </div>
    </div>
  );
}

/** A stable first impression without taking the choice away from the picker. */
export function preferredInstrument(catalog: Instrument[]): Instrument | null {
  const ready = catalog.filter((candidate) => candidate.ready);
  const isBtc = (candidate: Instrument) =>
    candidate.baseAsset.toUpperCase() === "BTC" || candidate.symbol.toUpperCase().startsWith("BTC-");
  return (
    ready.find(
      (candidate) =>
        candidate.venue.toLowerCase() === "binance" &&
        candidate.product.toLowerCase().includes("perp") &&
        isBtc(candidate),
    ) ??
    ready.find(isBtc) ??
    ready[0] ??
    catalog[0] ??
    null
  );
}
