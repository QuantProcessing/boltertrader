import { useT } from "@/lib/i18n";
import { useReferencePrices } from "@/features/market/reference-prices";
import { useEffect, useMemo, useState } from "react";

import type { CandleInterval, Instrument } from "@/api/types";
import { useReadOnly } from "@/app/mode";
import { Blotter, ReadOnlyBlotter } from "@/features/trade/blotter";
import { OrderTicket } from "@/features/trade/order-ticket";
import { useAccountPositions } from "@/features/trade/queries";
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
  const t = useT();
  const readOnly = useReadOnly();
  const instruments = useInstruments();
  const accounts = useAccounts();

  const [selected, setSelected] = useState<Instrument | null>(null);
  const [interval, setInterval] = useState<CandleInterval>(DEFAULT_INTERVAL);
  // Lifted out of the ticket so a click in the book can write into it.
  const [limitPrice, setLimitPrice] = useState("");
  const [accountId, setAccountId] = useState("");

  const catalog = useMemo(
    () => instruments.data?.items ?? [],
    [instruments.data],
  );

  // Open on a liquid, recognisable public market. Catalog order is grouped by
  // connector rather than by usefulness, so taking its first row made the
  // terminal routinely open on a thin altcoin with no depth and made a healthy
  // read-only gateway look empty.
  const instrument = selected ?? preferredInstrument(catalog);

  const query = instrument ? marketOf(instrument) : null;
  const fact = useMarketFact(query);
  const candles = useCandles(
    query ? { ...query, interval, limit: CHART_BARS } : null,
  );
  const today = useCandles(
    query ? { ...query, interval: "1d", limit: 1 } : null,
  );
  const restFact = fact.data?.value;
  const stream = usePublicMarketStreams(
    instrument?.instrumentId,
    interval,
    instrument?.product,
  );
  const [marketView, setMarketView] = useState<"chart" | "book">("chart");
  const indices = useReferencePrices(
    instrument?.product === "perp" ? [instrument.instrumentId] : [],
  );
  const liveIndex = instrument ? indices[instrument.instrumentId] : undefined;
  const liveMark =
    stream.mark?.instrumentId === instrument?.instrumentId ? stream.mark : null;
  const currentRestFact =
    restFact?.instrumentId === instrument?.instrumentId ? restFact : undefined;

  // Disabling a private query does not erase a previously cached response.
  // Filter at the write surface as well so signing out cannot retain an account
  // (or a sealed preview) from the authenticated session.
  const eligible = useMemo(
    () => (readOnly ? [] : tradableAccounts(accounts.data ?? [], instrument)),
    [accounts.data, instrument, readOnly],
  );
  useEffect(() => {
    if (eligible.some((account) => account.accountId === accountId)) return;
    setAccountId(eligible[0]?.accountId ?? "");
  }, [accountId, eligible]);

  const accountPositions = useAccountPositions(
    accountId,
    instrument?.instrumentId ?? "",
  );
  const positionItems = accountPositions.data?.items ?? [];
  const marketFact = useMemo(() => {
    if (!instrument) return undefined;
    return {
      venue: instrument?.venue ?? currentRestFact?.venue ?? "",
      environment:
        instrument?.environment ?? currentRestFact?.environment ?? "live",
      product: instrument?.product ?? currentRestFact?.product ?? "",
      instrumentId:
        instrument?.instrumentId ?? currentRestFact?.instrumentId ?? "",
      bestBid: stream.book?.bids[0]?.price ?? currentRestFact?.bestBid ?? "",
      bestBidSize:
        stream.book?.bids[0]?.quantity ?? currentRestFact?.bestBidSize ?? "",
      bestAsk: stream.book?.asks[0]?.price ?? currentRestFact?.bestAsk ?? "",
      bestAskSize:
        stream.book?.asks[0]?.quantity ?? currentRestFact?.bestAskSize ?? "",
      bids: stream.book?.bids ?? currentRestFact?.bids ?? [],
      asks: stream.book?.asks ?? currentRestFact?.asks ?? [],
      markPrice: liveMark?.price ?? currentRestFact?.markPrice,
      indexPrice: liveIndex?.price,
      fundingRate: liveIndex?.fundingRate ?? currentRestFact?.fundingRate,
      openInterest: liveIndex?.openInterest ?? currentRestFact?.openInterest,
      fundingObservedAt:
        liveIndex?.fundingObservedAt ?? currentRestFact?.fundingObservedAt,
      fundingTime: currentRestFact?.fundingTime,
      nextFundingTime:
        liveIndex?.nextFundingTime ?? currentRestFact?.nextFundingTime,
      observedAt:
        liveIndex?.observedAt ??
        liveMark?.observedAt ??
        stream.book?.observedAt ??
        currentRestFact?.observedAt ??
        "",
    };
  }, [currentRestFact, instrument, liveMark, liveIndex, stream.book]);
  const midPrice = midpointPrice(
    marketFact?.bestBid,
    marketFact?.bestAsk,
    decimalsOf(instrument?.rules.priceIncrement),
  );

  return (
    <div className="trade-workspace">
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
        today={
          stream.dayCandle
            ? ({
                ...today.data?.value,
                candles: [stream.dayCandle],
              } as import("@/api/types").CandlePage)
            : today.data?.value
        }
        loading={instruments.isPending}
      />

      <div className="trade-market-tabs" aria-label={t("workspace.marketView")}>
        <button
          type="button"
          className="rounded-control px-3 py-2 text-meta"
          aria-pressed={marketView === "chart"}
          onClick={() => setMarketView("chart")}
        >
          {t("workspace.chart")}
        </button>
        <button
          type="button"
          className="rounded-control px-3 py-2 text-meta"
          aria-pressed={marketView === "book"}
          onClick={() => setMarketView("book")}
        >
          {t("workspace.book")}
        </button>
      </div>
      <div className="trade-grid" data-market-view={marketView}>
        <div className="trade-chart">
          <PriceChart
            candles={candles.data?.value.candles}
            liveCandle={stream.candle}
            mark={liveMark}
            instrument={instrument}
            positions={positionItems}
            interval={interval}
            onIntervalChange={setInterval}
          />
        </div>

        <div className="trade-book">
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

        <div className="trade-blotter">
          {readOnly ? (
            <ReadOnlyBlotter />
          ) : (
            <Blotter
              instrumentId={instrument?.instrumentId ?? null}
              positions={positionItems}
              positionsLoading={
                accountPositions.isFetching &&
                accountPositions.data === undefined
              }
              positionsError={accountPositions.isError}
              positionAccounts={accountPositions.data?.accounts ?? []}
              liveMark={liveMark}
              liveIndex={liveIndex}
              instrument={instrument}
            />
          )}
        </div>

        <div className="trade-ticket">
          <OrderTicket
            instrument={instrument}
            accounts={eligible}
            accountId={accountId}
            onAccountIdChange={setAccountId}
            positions={positionItems}
            limitPrice={limitPrice}
            onLimitPriceChange={setLimitPrice}
            markPrice={
              liveMark
                ? liveMark.stale
                  ? undefined
                  : liveMark.price
                : currentRestFact?.markPrice
            }
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
    candidate.baseAsset.toUpperCase() === "BTC" ||
    candidate.symbol.toUpperCase().startsWith("BTC-");
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
