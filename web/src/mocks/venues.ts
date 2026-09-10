import type {
  Candle,
  CandleInterval,
  FundingBoardRow,
  FundingBoardSnapshot,
  FundingBoardVenue,
  FundingHistoryPoint,
  FundingHistorySeries,
  Instrument,
  MarketFact,
} from "../api/types";

/* ---------------------------------------------------------------------------
 * Real market data, in the mock.
 *
 * The mock exists so that placing an order does not reach an exchange. Market
 * data is not an order: a book, a chart and a funding rate are public on every
 * venue here, need no key, and describe a market that exists whether or not we
 * are looking at it. Synthesising them bought nothing and cost the thing the
 * screen is for — the numbers were drawn around anchors written down once, so
 * BTC sat at 114,250 long after the market left, and the three instruments
 * nobody had written an anchor for priced at exactly 100.
 *
 * So this module is the venue half of the mock, and only the venue half. It
 * answers the market plane from each exchange's own public REST API. Everything
 * a key would be needed for — accounts, previews, commands, the audit chain —
 * stays in `db.ts` and never leaves the page.
 *
 * Two properties this file has to keep:
 *
 *   No credentials. Every request below is one an anonymous browser can make.
 *   Nothing here reads a secret, and nothing here signs anything, so the mock
 *   still cannot place an order even if a handler is wired wrong.
 *
 *   No re-entry. These are absolute `https://` URLs and the service worker's
 *   handlers all match relative `/api/v1/…` paths, so a fetch from here goes to
 *   the network rather than back into the mock.
 * ------------------------------------------------------------------------- */

/** How many levels of depth the terminal's book shows. */
// Binance accepts a fixed set of depth limits; 20 is the smallest value that
// covers the terminal's 12 visible levels.
const DEPTH = 20;

/**
 * Per-venue rate limits are generous for public reads, but the terminal polls
 * the book every 2s and React Query fires on focus, so an unlucky sequence can
 * still stack up. A short cache collapses those without making the screen stale
 * — 1.5s is under the poll interval, so a real poll always gets a real read.
 */
const CACHE_MS = 1_500;

const cache = new Map<string, { at: number; value: Promise<unknown> }>();

function cached<T>(key: string, load: () => Promise<T>, ttl = CACHE_MS): Promise<T> {
  const hit = cache.get(key);
  if (hit && Date.now() - hit.at < ttl) {
    return hit.value as Promise<T>;
  }
  const value = load().catch((error: unknown) => {
    // A failed read must not be cached, or one flaky moment sticks for as long
    // as the entry lives and the screen stays broken after the venue recovers.
    cache.delete(key);
    throw error;
  });
  const entry = { at: Date.now(), value: value as Promise<unknown> };
  cache.set(key, entry);
  void value.then(() => {
    // Slow venue requests should receive their cache lifetime after they land,
    // not spend the whole lifetime while still in flight.
    if (cache.get(key) === entry) entry.at = Date.now();
  });
  return value;
}

async function getJSON<T>(url: string): Promise<T> {
  const controller = new AbortController();
  const timeout = window.setTimeout(() => controller.abort(), 10_000);
  try {
    const response = await fetch(url, { headers: { accept: "application/json" }, signal: controller.signal });
    if (!response.ok) {
      throw new Error(`${new URL(url).host} answered ${response.status}`);
    }
    return (await response.json()) as T;
  } finally {
    window.clearTimeout(timeout);
  }
}

/* --- Symbols -------------------------------------------------------------- */

/**
 * The venue's own name for the instrument.
 *
 * The catalog carries a normalised `BTC-USDT`; no venue does. Binance and bybit
 * concatenate, okx hyphenates and suffixes swaps with `-SWAP`, and lighter
 * indexes markets by an integer whose mapping is fetched, not guessed.
 */
function nativeSymbol(instrument: Instrument): string {
  const bare = instrument.symbol.replace("-", "");
  switch (instrument.venue) {
    case "binance":
    case "bybit":
      return bare;
    case "okx":
      return instrument.product === "perp" ? `${instrument.symbol}-SWAP` : instrument.symbol;
    default:
      return instrument.symbol;
  }
}

/** lighter's market ids, read from the venue rather than hardcoded. */
let lighterMarkets: Promise<Map<string, number>> | undefined;

/** Keyed by base asset: lighter names a market after it alone, so ETH-USDC is "ETH". */
function lighterMarketId(baseAsset: string): Promise<number> {
  lighterMarkets ??= getJSON<{ order_books: { symbol: string; market_id: number }[] }>(
    "https://mainnet.zklighter.elliot.ai/api/v1/orderBooks",
  ).then((body) => new Map(body.order_books.map((book) => [book.symbol, book.market_id])));

  return lighterMarkets.then((markets) => {
    const id = markets.get(baseAsset);
    if (id === undefined) {
      throw new Error(`lighter does not list ${baseAsset}`);
    }
    return id;
  });
}

/* --- Intervals ------------------------------------------------------------ */

/** Each venue spells the same durations differently, and two of them disagree. */
const INTERVALS: Record<string, Record<CandleInterval, string>> = {
  binance: { "1m": "1m", "5m": "5m", "15m": "15m", "30m": "30m", "1h": "1h", "4h": "4h", "12h": "12h", "1d": "1d" },
  // bybit counts minutes, then switches to letters at the day.
  bybit: { "1m": "1", "5m": "5", "15m": "15", "30m": "30", "1h": "60", "4h": "240", "12h": "720", "1d": "D" },
  // okx uppercases anything from an hour up, and only those.
  okx: { "1m": "1m", "5m": "5m", "15m": "15m", "30m": "30m", "1h": "1H", "4h": "4H", "12h": "12H", "1d": "1D" },
};

export const INTERVAL_MS: Record<CandleInterval, number> = {
  "1m": 60_000,
  "5m": 300_000,
  "15m": 900_000,
  "30m": 1_800_000,
  "1h": 3_600_000,
  "4h": 14_400_000,
  "12h": 43_200_000,
  "1d": 86_400_000,
};

/* --- Shapes --------------------------------------------------------------- */

/** A `[price, size]` pair, which is how three of the four venues send depth. */
type Level = [string, string, ...unknown[]];

interface Book {
  bids: Level[];
  asks: Level[];
  markPrice?: string;
  fundingRate?: string;
  openInterest?: string;
}

function level(pair: Level): { price: string; quantity: string } {
  return { price: pair[0], quantity: pair[1] };
}

/**
 * Assembles the fact the API returns from a venue's book.
 *
 * The best bid and ask are taken from the top of the book rather than from a
 * ticker endpoint, so the touch and the depth below it are the same
 * observation. A ticker read alongside a book read can disagree by a tick, and
 * a spread that crosses on screen is worse than one that is a moment old.
 */
function factOf(instrument: Instrument, book: Book, observedAt: string): MarketFact {
  const bids = book.bids.slice(0, DEPTH).map(level);
  const asks = book.asks.slice(0, DEPTH).map(level);
  if (bids.length === 0 || asks.length === 0) {
    throw new Error(`${instrument.venue} returned an empty book for ${instrument.symbol}`);
  }
  return {
    venue: instrument.venue,
    environment: instrument.environment,
    product: instrument.product,
    instrumentId: instrument.instrumentId,
    bestBid: bids[0]!.price,
    bestBidSize: bids[0]!.quantity,
    bestAsk: asks[0]!.price,
    bestAskSize: asks[0]!.quantity,
    bids,
    asks,
    ...(book.markPrice !== undefined ? { markPrice: book.markPrice } : {}),
    ...(book.fundingRate !== undefined ? { fundingRate: book.fundingRate } : {}),
    ...(book.openInterest !== undefined ? { openInterest: book.openInterest } : {}),
    observedAt,
  };
}

/**
 * Binance klines and bybit klines carry the same seven fields in the same
 * order, so both decode here. okx and lighter do not, and have their own.
 */
function klineOf(row: string[], closeTime: number, complete: boolean): Candle {
  return {
    openTime: new Date(Number(row[0])).toISOString(),
    closeTime: new Date(closeTime).toISOString(),
    open: row[1]!,
    high: row[2]!,
    low: row[3]!,
    close: row[4]!,
    volume: row[5]!,
    complete,
  };
}

/* --- Books ---------------------------------------------------------------- */

async function binanceBook(instrument: Instrument): Promise<Book> {
  const symbol = nativeSymbol(instrument);
  if (instrument.product === "spot") {
    return await getJSON<Book>(`https://api.binance.com/api/v3/depth?symbol=${symbol}&limit=${DEPTH}`);
  }
  // The perp's three extra fields come from two more reads. They are requested
  // together rather than in sequence: they describe the same instant, and a
  // waterfall would make the book older than the mark it is shown beside.
  const [depth, premium, openInterest] = await Promise.all([
    getJSON<Book>(`https://fapi.binance.com/fapi/v1/depth?symbol=${symbol}&limit=${DEPTH}`),
    getJSON<{ markPrice: string; lastFundingRate: string }>(
      `https://fapi.binance.com/fapi/v1/premiumIndex?symbol=${symbol}`,
    ),
    getJSON<{ openInterest: string }>(`https://fapi.binance.com/fapi/v1/openInterest?symbol=${symbol}`),
  ]);
  return {
    ...depth,
    markPrice: premium.markPrice,
    fundingRate: premium.lastFundingRate,
    openInterest: openInterest.openInterest,
  };
}

async function bybitBook(instrument: Instrument): Promise<Book> {
  const symbol = nativeSymbol(instrument);
  const category = instrument.product === "spot" ? "spot" : "linear";
  const [book, tickers] = await Promise.all([
    getJSON<{ result: { b: Level[]; a: Level[] } }>(
      `https://api.bybit.com/v5/market/orderbook?category=${category}&symbol=${symbol}&limit=${DEPTH}`,
    ),
    getJSON<{ result: { list: { markPrice?: string; fundingRate?: string; openInterest?: string }[] } }>(
      `https://api.bybit.com/v5/market/tickers?category=${category}&symbol=${symbol}`,
    ),
  ]);
  const ticker = tickers.result.list[0];
  return {
    bids: book.result.b,
    asks: book.result.a,
    ...(instrument.product === "perp" && ticker
      ? {
          markPrice: ticker.markPrice,
          fundingRate: ticker.fundingRate,
          openInterest: ticker.openInterest,
        }
      : {}),
  };
}

async function okxBook(instrument: Instrument): Promise<Book> {
  const instId = nativeSymbol(instrument);
  const books = getJSON<{ data: { bids: Level[]; asks: Level[] }[] }>(
    `https://www.okx.com/api/v5/market/books?instId=${instId}&sz=${DEPTH}`,
  );
  if (instrument.product === "spot") {
    const book = (await books).data[0];
    return { bids: book?.bids ?? [], asks: book?.asks ?? [] };
  }
  const [book, funding, openInterest, mark] = await Promise.all([
    books,
    getJSON<{ data: { fundingRate: string }[] }>(`https://www.okx.com/api/v5/public/funding-rate?instId=${instId}`),
    getJSON<{ data: { oiCcy: string }[] }>(`https://www.okx.com/api/v5/public/open-interest?instId=${instId}`),
    getJSON<{ data: { markPx: string }[] }>(
      `https://www.okx.com/api/v5/public/mark-price?instType=SWAP&instId=${instId}`,
    ),
  ]);
  return {
    bids: book.data[0]?.bids ?? [],
    asks: book.data[0]?.asks ?? [],
    ...(mark.data[0] ? { markPrice: mark.data[0].markPx } : {}),
    ...(funding.data[0] ? { fundingRate: funding.data[0].fundingRate } : {}),
    // oiCcy is open interest in the base asset, which is what the other three
    // venues report and what the column is labelled.
    ...(openInterest.data[0] ? { openInterest: openInterest.data[0].oiCcy } : {}),
  };
}

interface LighterOrder {
  price: string;
  remaining_base_amount: string;
}

async function lighterBook(instrument: Instrument): Promise<Book> {
  const marketId = await lighterMarketId(instrument.baseAsset);
  const [orders, details, rates] = await Promise.all([
    getJSON<{ bids: LighterOrder[]; asks: LighterOrder[] }>(
      `https://mainnet.zklighter.elliot.ai/api/v1/orderBookOrders?market_id=${marketId}&limit=${DEPTH}`,
    ),
    getJSON<{
      // `open_interest` arrives as a JSON number here while every other venue
      // sends a string, so it is stringified below rather than typed as one.
      order_book_details: { mark_price?: string; open_interest?: number }[];
    }>(`https://mainnet.zklighter.elliot.ai/api/v1/orderBookDetails?market_id=${marketId}`),
    // The rate lives on a different route from everything else, and that route
    // answers for every market at once — hence the filter rather than a query.
    instrument.product === "perp"
      ? getJSON<{ funding_rates: LighterRate[] }>(
          "https://mainnet.zklighter.elliot.ai/api/v1/funding-rates",
        ).catch(() => ({ funding_rates: [] as LighterRate[] }))
      : Promise.resolve({ funding_rates: [] as LighterRate[] }),
  ]);

  // lighter publishes resting orders rather than aggregated levels, so several
  // rows can share a price. Summing them is what makes this a book: without it
  // the top twelve rows might be one price twelve times.
  const aggregate = (side: LighterOrder[], descending: boolean): Level[] => {
    const byPrice = new Map<string, number>();
    for (const order of side) {
      byPrice.set(order.price, (byPrice.get(order.price) ?? 0) + Number(order.remaining_base_amount));
    }
    return [...byPrice.entries()]
      .sort((a, b) => (descending ? Number(b[0]) - Number(a[0]) : Number(a[0]) - Number(b[0])))
      .map(([price, size]) => [price, String(size)] as Level);
  };

  const detail = instrument.product === "perp" ? details.order_book_details[0] : undefined;
  const funding = rates.funding_rates.find(
    (row) => row.exchange === "lighter" && row.market_id === marketId,
  );
  return {
    bids: aggregate(orders.bids ?? [], true),
    asks: aggregate(orders.asks ?? [], false),
    ...(detail?.mark_price !== undefined ? { markPrice: detail.mark_price } : {}),
    // Already a signed fraction, so it needs no scaling — unlike lighter's
    // funding *history*, which is a percent whose sign is carried separately.
    ...(funding ? { fundingRate: decimalString(funding.rate) } : {}),
    ...(detail?.open_interest !== undefined ? { openInterest: String(detail.open_interest) } : {}),
  };
}

/* --- Candles -------------------------------------------------------------- */

async function binanceCandles(
  instrument: Instrument,
  interval: CandleInterval,
  limit: number,
): Promise<Candle[]> {
  const symbol = nativeSymbol(instrument);
  const bar = INTERVALS.binance![interval];
  const host = instrument.product === "spot" ? "https://api.binance.com/api/v3" : "https://fapi.binance.com/fapi/v1";
  const rows = await getJSON<string[][]>(`${host}/klines?symbol=${symbol}&interval=${bar}&limit=${limit}`);
  // Binance sends the close time, so the forming bar is the one whose close is
  // still ahead of us rather than the one that happens to be last.
  return rows.map((row) => klineOf(row, Number(row[6]), Number(row[6]) < Date.now()));
}

async function bybitCandles(instrument: Instrument, interval: CandleInterval, limit: number): Promise<Candle[]> {
  const symbol = nativeSymbol(instrument);
  const category = instrument.product === "spot" ? "spot" : "linear";
  const bar = INTERVALS.bybit![interval];
  const body = await getJSON<{ result: { list: string[][] } }>(
    `https://api.bybit.com/v5/market/kline?category=${category}&symbol=${symbol}&interval=${bar}&limit=${limit}`,
  );
  const step = INTERVAL_MS[interval];
  // bybit returns newest first and carries no close time.
  return body.result.list
    .map((row) => klineOf(row, Number(row[0]) + step - 1, Number(row[0]) + step < Date.now()))
    .reverse();
}

async function okxCandles(instrument: Instrument, interval: CandleInterval, limit: number): Promise<Candle[]> {
  const instId = nativeSymbol(instrument);
  const bar = INTERVALS.okx![interval];
  const body = await getJSON<{ data: string[][] }>(
    `https://www.okx.com/api/v5/market/candles?instId=${instId}&bar=${bar}&limit=${limit}`,
  );
  const step = INTERVAL_MS[interval];
  // okx returns newest first, and its ninth field is the venue's own answer to
  // "is this bar closed", so completeness is read rather than inferred.
  return body.data
    .map((row) => ({
      openTime: new Date(Number(row[0])).toISOString(),
      closeTime: new Date(Number(row[0]) + step - 1).toISOString(),
      open: row[1]!,
      high: row[2]!,
      low: row[3]!,
      close: row[4]!,
      volume: row[5]!,
      complete: row[8] === "1",
    }))
    .reverse();
}

/* --- Funding: the board --------------------------------------------------- */

/**
 * Every venue's funding surface, read in one call each.
 *
 * The four readers below are ports of the production loaders in
 * `exchange/internal/factoryclient/*_funding_loader.go`, and they are ports
 * rather than fresh work on purpose: those files record decisions that were
 * settled against the live feeds, and several of them are decisions to leave a
 * cell *empty*. Binance does not get an eight-hour default, lighter does not get
 * a mark price, okx keeps its `-SWAP` suffix. Filling any of those in here would
 * make the mock disagree with the server it stands in for — and disagree by
 * showing more, which is the direction nobody checks.
 *
 * `stale` is false throughout and `observedAt` is now: the gateway sets those
 * from the age of a cached snapshot, and nothing is cached here for longer than
 * `CACHE_MS`. A venue that fails to answer becomes an unavailable column with
 * neither rows nor an observation time, which is what `validateBoard` requires.
 */
const SETTLEMENT = "USDT";

interface BoardColumn {
  venue: string;
  settlement: string;
  routeProfileId: string;
  load: () => Promise<FundingBoardRow[]>;
}

/**
 * A JSON number as a decimal string.
 *
 * Two venues send funding as a number, and `String()` is not enough: JS drops to
 * exponent form below 1e-6, and `splitDecimal` rejects `"1e-7"` outright, so the
 * cell would render the raw token instead of a rate. `toFixed(12)` is below any
 * rate a venue quotes and above the precision either publishes.
 */
function decimalString(value: number): string {
  return value.toFixed(12).replace(/0+$/, "").replace(/\.$/, "");
}

/** binance: premiumIndex for the rate, fundingInfo for the clock, and no more. */
async function binanceBoard(): Promise<FundingBoardRow[]> {
  const [premiums, info] = await Promise.all([
    getJSON<
      { symbol: string; markPrice: string; indexPrice: string; lastFundingRate: string; nextFundingTime: number }[]
    >("https://fapi.binance.com/fapi/v1/premiumIndex"),
    // Best-effort: a board without intervals is still a board, and this call
    // only ever adds cells.
    getJSON<{ symbol: string; fundingIntervalHours: number }[]>(
      "https://fapi.binance.com/fapi/v1/fundingInfo",
    ).catch(() => []),
  ]);

  // Binance publishes an interval only for the symbols that deviate from eight
  // hours. The other 130 are Binance saying "the default" — which is not
  // invented here. The board renders the gap, exactly as the Go loader leaves it.
  const intervals = new Map(
    info.filter((row) => row.fundingIntervalHours > 0).map((row) => [row.symbol, row.fundingIntervalHours]),
  );

  const rows: FundingBoardRow[] = [];
  for (const premium of premiums) {
    // premiumIndex answers for delivery futures too, and those carry an expiry
    // in the symbol — BTCUSDT_240628 has no quote suffix to find.
    const quote = ["USDT", "USDC"].find(
      (candidate) => premium.symbol.length > candidate.length && premium.symbol.endsWith(candidate),
    );
    if (quote !== SETTLEMENT) continue;
    const base = premium.symbol.slice(0, -quote.length);
    if (!premium.lastFundingRate) continue;

    const interval = intervals.get(premium.symbol);
    rows.push({
      instrumentId: `binance:live:perp:${quote}:${base}-${quote}`,
      symbol: `${base}-${quote}`,
      baseAsset: base,
      quoteAsset: quote,
      marketFamily: "venue_perpetual",
      assetClass: "crypto",
      rate: premium.lastFundingRate,
      ...(interval !== undefined ? { intervalHours: interval } : {}),
      ...(premium.nextFundingTime > 0
        ? { nextFundingTime: new Date(premium.nextFundingTime).toISOString() }
        : {}),
      ...positive("markPrice", premium.markPrice),
      ...positive("indexPrice", premium.indexPrice),
      // No open interest: binance serves it one symbol at a time, and paying
      // several hundred requests for one column is not a trade a board makes.
    });
  }
  return rows;
}

/** bybit: one tickers call fills every cell, which is why the column exists. */
async function bybitBoard(): Promise<FundingBoardRow[]> {
  const body = await getJSON<{
    result: {
      list: {
        symbol: string;
        fundingRate: string;
        markPrice: string;
        indexPrice: string;
        openInterest: string;
        nextFundingTime: string;
        fundingIntervalHour: string;
      }[];
    };
  }>("https://api.bybit.com/v5/market/tickers?category=linear");

  const rows: FundingBoardRow[] = [];
  for (const ticker of body.result.list) {
    // Tickers carry no settle coin and the linear category mixes USDT- and
    // USDC-margined contracts, so settlement is inferred from the symbol.
    const quote = ["USDT", "USDC", "USD"].find(
      (candidate) => ticker.symbol.length > candidate.length && ticker.symbol.endsWith(candidate),
    );
    if (quote !== SETTLEMENT) continue;
    if (!ticker.fundingRate) continue;
    const base = ticker.symbol.slice(0, -quote.length);

    // 40 of bybit's 826 linear symbols send an empty interval. Zero is bybit's
    // spelling for "unset", and a row without an interval is the honest render.
    const interval = Number(ticker.fundingIntervalHour);
    const settles = Number(ticker.nextFundingTime);
    rows.push({
      instrumentId: `bybit:live:perp:${quote}:${base}-${quote}`,
      symbol: `${base}-${quote}`,
      baseAsset: base,
      quoteAsset: quote,
      marketFamily: "venue_perpetual",
      assetClass: "crypto",
      rate: ticker.fundingRate,
      ...(Number.isFinite(interval) && interval > 0 ? { intervalHours: Math.trunc(interval) } : {}),
      ...(Number.isFinite(settles) && settles > 0 ? { nextFundingTime: new Date(settles).toISOString() } : {}),
      ...positive("markPrice", ticker.markPrice),
      ...positive("indexPrice", ticker.indexPrice),
      ...nonNegative("openInterest", ticker.openInterest),
    });
  }
  return rows;
}

/** okx: one instId=ANY call; the interval is the gap between the two stamps. */
async function okxBoard(): Promise<FundingBoardRow[]> {
  const body = await getJSON<{
    data: {
      instId: string;
      instType: string;
      fundingRate: string;
      fundingTime: string;
      nextFundingTime: string;
    }[];
  }>("https://www.okx.com/api/v5/public/funding-rate?instId=ANY");

  const rows: FundingBoardRow[] = [];
  for (const row of body.data) {
    // The response mixes SWAP with dated FUTURES; only the swaps are perps.
    if (row.instType !== "SWAP") continue;
    const parts = row.instId.split("-");
    if (parts.length !== 3 || parts[1] !== SETTLEMENT) continue;
    // A blank rate means unknown. Reading it as zero would offer the operator a
    // spread against a number okx never quoted.
    if (!row.fundingRate) continue;

    const settles = Number(row.fundingTime);
    const next = Number(row.nextFundingTime);
    const hours = Number.isFinite(settles) && Number.isFinite(next) ? Math.trunc((next - settles) / 3_600_000) : 0;
    rows.push({
      instrumentId: `okx:live:perp:${parts[1]}:${parts[0]}-${parts[1]}`,
      // The raw instId, deliberately. The catalog files okx swaps under
      // BTC-USDT-SWAP, so canonicalising to BTC-USDT here would put these rates
      // under a symbol no other okx row uses.
      symbol: row.instId,
      baseAsset: parts[0]!,
      quoteAsset: parts[1]!,
      marketFamily: "venue_perpetual",
      assetClass: "crypto",
      rate: row.fundingRate,
      ...(hours > 0 ? { intervalHours: hours } : {}),
      ...(Number.isFinite(settles) && settles > 0 ? { nextFundingTime: new Date(settles).toISOString() } : {}),
      // No mark, no index, no open interest: this endpoint publishes none.
    });
  }
  return rows;
}

interface LighterRate {
  market_id: number;
  exchange: string;
  symbol: string;
  rate: number;
}

/** lighter: a rate and nothing else, which is all the venue publishes. */
async function lighterBoard(): Promise<FundingBoardRow[]> {
  const body = await getJSON<{ funding_rates: LighterRate[] }>(
    "https://mainnet.zklighter.elliot.ai/api/v1/funding-rates",
  );

  return (
    body.funding_rates
      // The endpoint carries other venues' rates alongside lighter's own, for
      // comparison. Those must not reach a column labelled lighter.
      .filter((row) => row.exchange === "lighter" && row.symbol !== "")
      .map((row) => ({
        instrumentId: `lighter:live:perp:USDC:${row.symbol}-USDC`,
        symbol: `${row.symbol}-USDC`,
        baseAsset: row.symbol,
        quoteAsset: "USDC",
        marketFamily: "venue_perpetual" as const,
        assetClass: "crypto" as const,
        // Already a signed fraction here, unlike lighter's history surface.
        rate: decimalString(row.rate),
        // The five optional cells stay absent. Lighter publishes no mark, no
        // index, no open interest, no settlement clock and no interval, and a
        // placeholder would put a number on screen the venue never said.
      }))
  );
}

const BOARD: BoardColumn[] = [
  { venue: "binance", settlement: SETTLEMENT, routeProfileId: "binance.perp.usdt.v1", load: binanceBoard },
  { venue: "bybit", settlement: SETTLEMENT, routeProfileId: "bybit.perp.usdt.v1", load: bybitBoard },
  { venue: "okx", settlement: SETTLEMENT, routeProfileId: "okx.perp.usdt.v1", load: okxBoard },
  { venue: "lighter", settlement: "USDC", routeProfileId: "lighter.perp.usdc.v1", load: lighterBoard },
];

/** A price cell, which is absent unless the venue quoted a positive number. */
function positive(field: "markPrice" | "indexPrice", value: string | undefined): Record<string, string> {
  const parsed = Number(value);
  return value && Number.isFinite(parsed) && parsed > 0 ? { [field]: value } : {};
}

/** A quantity cell, where zero is a real observation and negative is not. */
function nonNegative(field: "openInterest", value: string | undefined): Record<string, string> {
  const parsed = Number(value);
  return value && Number.isFinite(parsed) && parsed >= 0 ? { [field]: value } : {};
}

export async function fetchFundingBoard(): Promise<FundingBoardSnapshot> {
  return await cached("funding-board", async () => {
    // All four at once. They are independent venues and a waterfall would make
    // the first column a second older than the last for no reason.
    const columns = await Promise.all(
      BOARD.map(async (column): Promise<FundingBoardVenue> => {
        const base = {
          venue: column.venue,
          environment: "live",
          product: "perp",
          settlement: column.settlement,
          routeProfileId: column.routeProfileId,
        };
        try {
          const rows = await column.load();
          const observedAt = new Date().toISOString();
          return {
            ...base,
            observedAt,
            stale: false,
            unavailable: false,
            status: "live",
            lastSuccessAt: observedAt,
            rows: rows.map((row) => ({
              ...row,
              ...(row.rate !== undefined ? { fundingObservedAt: observedAt } : {}),
              ...(row.markPrice !== undefined ? { markObservedAt: observedAt, priceObservedAt: observedAt } : {}),
              ...(row.indexPrice !== undefined ? { indexObservedAt: observedAt } : {}),
              ...(row.openInterest !== undefined ? { openInterestObservedAt: observedAt } : {}),
            })),
          };
        } catch {
          // Unavailable means no rows *and* no observation time — a column that
          // failed to load has not observed anything. The server's validator
          // rejects a snapshot that says otherwise.
          return { ...base, stale: false, unavailable: true, status: "unavailable", rows: [] };
        }
      }),
    );
    return { observedAt: new Date().toISOString(), venues: columns };
  }, 60_000);
}

/* --- Funding: the history ------------------------------------------------- */

/** What each venue will return in one call. Thirty days hourly is 721 rows. */
const HISTORY_LIMIT: Record<string, number> = {
  binance: 1000,
  bybit: 200,
  okx: 100,
  lighter: 750,
};

async function binanceHistory(symbol: string, since: number, until: number): Promise<FundingHistoryPoint[]> {
  const rows = await getJSON<{ fundingTime: number; fundingRate: string }[]>(
    `https://fapi.binance.com/fapi/v1/fundingRate?symbol=${symbol}&startTime=${since}&endTime=${until}` +
      `&limit=${HISTORY_LIMIT.binance}`,
  );
  // An unknown symbol is a 200 with an empty array, not an error, so "no rows"
  // is the only signal there is — and the caller reads it as "not listed".
  return rows.map((row) => ({ fundingTime: new Date(row.fundingTime).toISOString(), rate: row.fundingRate }));
}

async function bybitHistory(symbol: string, since: number, until: number): Promise<FundingHistoryPoint[]> {
  const body = await getJSON<{
    retCode: number;
    retMsg: string;
    result: { list: { fundingRateTimestamp: string; fundingRate: string }[] };
  }>(
    `https://api.bybit.com/v5/market/funding/history?category=linear&symbol=${symbol}` +
      `&startTime=${since}&endTime=${until}&limit=${HISTORY_LIMIT.bybit}`,
  );
  // bybit answers 200 and puts the failure in the body, so an unknown symbol
  // reaches here as a successful response carrying retCode 10001.
  if (body.retCode !== 0) return [];
  return body.result.list.map((row) => ({
    fundingTime: new Date(Number(row.fundingRateTimestamp)).toISOString(),
    rate: row.fundingRate,
  }));
}

async function okxHistory(instId: string, since: number, until: number): Promise<FundingHistoryPoint[]> {
  // okx's two cursors are named for the row rather than for the clock: `before`
  // selects rows newer than the stamp and `after` rows older, so the window maps
  // across inverted. The other way round returns an empty list rather than an
  // error, which reads exactly like "this instrument has no history".
  const body = await getJSON<{
    code: string;
    data: { fundingTime: string; fundingRate: string; realizedRate?: string }[];
  }>(
    `https://www.okx.com/api/v5/public/funding-rate-history?instId=${instId}` +
      `&before=${since}&after=${until}&limit=${HISTORY_LIMIT.okx}`,
  );
  if (body.code !== "0") return [];
  return body.data.flatMap((row) => {
    // The realised rate is what was actually charged, and a chart of what was
    // paid is the one an operator can reconcile against a funding statement.
    // The prediction is the fallback for the rows okx leaves blank.
    const rate = row.realizedRate || row.fundingRate;
    if (!rate) return [];
    return [{ fundingTime: new Date(Number(row.fundingTime)).toISOString(), rate }];
  });
}

async function lighterHistory(marketId: number, since: number, until: number): Promise<FundingHistoryPoint[]> {
  // lighter takes seconds, settles hourly, and wants a row count rather than a
  // page size — so count_back is one row per hour of the window, clamped to what
  // it will return.
  const hours = Math.min(Math.round((until - since) / 3_600_000) + 1, HISTORY_LIMIT.lighter!);
  const body = await getJSON<{ fundings?: { timestamp: number; rate: string; direction: string }[] }>(
    `https://mainnet.zklighter.elliot.ai/api/v1/fundings?market_id=${marketId}&resolution=1h` +
      `&start_timestamp=${Math.floor(since / 1000)}&end_timestamp=${Math.floor(until / 1000)}` +
      `&count_back=${hours}`,
  );

  return (body.fundings ?? []).flatMap((row) => {
    const rate = Number(row.rate);
    if (!Number.isFinite(rate)) return [];
    // Two conversions no other venue needs, both settled against the live feed.
    //
    // The rate is a percentage: lighter publishes "0.0012" where it means
    // 1.2e-5, and its own board reports 9.6e-05 for the same market at the same
    // moment — exactly eight hourly settlements of 1.2e-5.
    //
    // The sign lives in `direction`, not in the rate. No rate string is ever
    // negative here while `direction` reports "short" on a large share of rows,
    // and a funding series that never goes negative is not a funding series. An
    // unrecognised third value is dropped rather than guessed.
    const signed = row.direction === "long" ? rate / 100 : row.direction === "short" ? -rate / 100 : null;
    if (signed === null) return [];
    return [{ fundingTime: new Date(row.timestamp * 1000).toISOString(), rate: decimalString(signed) }];
  });
}

/**
 * Sort, dedupe, measure each interval, then clip — and in that order.
 *
 * No venue publishes the interval alongside a historical rate, so it is measured
 * from the gap to the point before it, which is exact where a hardcoded eight
 * hours would be wrong. That is also why the clip happens last: the first
 * in-window point takes its interval from a row just outside the window, and
 * clipping first would strip the leading point of every series of its interval
 * for no reason other than call order.
 *
 * The interval matters more than it looks. `spread-chart.tsx` drops any point
 * without one, so a series that lost its intervals renders as "no settlements"
 * rather than as a gap.
 */
function settle(points: FundingHistoryPoint[], since: number, until: number): FundingHistoryPoint[] {
  const ordered = points
    .filter((point) => !Number.isNaN(Date.parse(point.fundingTime)))
    .sort((a, b) => Date.parse(a.fundingTime) - Date.parse(b.fundingTime));

  const deduped: FundingHistoryPoint[] = [];
  for (const point of ordered) {
    const last = deduped[deduped.length - 1];
    // Strictly ascending: the server refuses a series that repeats a stamp.
    if (last && Date.parse(point.fundingTime) <= Date.parse(last.fundingTime)) continue;
    deduped.push(point);
  }

  const kept: FundingHistoryPoint[] = [];
  for (const [index, point] of deduped.entries()) {
    const at = Date.parse(point.fundingTime);
    const previous = deduped[index - 1];
    let measured: FundingHistoryPoint = point;
    if (previous && point.intervalHours === undefined) {
      const hours = (at - Date.parse(previous.fundingTime)) / 3_600_000;
      const rounded = Math.round(hours);
      // A gap that is not a whole number of hours is not an interval — it is a
      // venue that skipped a settlement, and rounding it would annualise the
      // gap as if it were the schedule.
      if (rounded >= 1 && Math.abs(hours - rounded) <= 0.01) {
        measured = { ...point, intervalHours: rounded };
      }
    }
    if (at < since || at > until) continue;
    kept.push(measured);
  }
  return kept;
}

/**
 * One venue's settled funding over a window.
 *
 * Returns null when the venue lists no such instrument, which every venue here
 * reports as an HTTP 200 — an empty array, or a failure code in the body — so it
 * is detected from the payload rather than from the status.
 */
export async function fetchFundingHistory(
  instrument: Instrument,
  since: Date,
  until: Date,
): Promise<FundingHistorySeries | null> {
  const { venue, symbol } = instrument;
  const points = await (async (): Promise<FundingHistoryPoint[]> => {
    switch (venue) {
      case "binance":
        return await binanceHistory(symbol.replace("-", ""), since.getTime(), until.getTime());
      case "bybit":
        return await bybitHistory(symbol.replace("-", ""), since.getTime(), until.getTime());
      case "okx":
        // The board files okx rows under their raw instId, so the symbol
        // arriving here already carries `-SWAP` and needs no suffix added.
        return await okxHistory(symbol, since.getTime(), until.getTime());
      default: {
        // lighter is the one venue addressed by a numeric market id; a symbol
        // sent where an integer is expected comes back as a parameter error,
        // which reads as "the venue is down" rather than "unknown instrument".
        const markets = await lighterMarketId(symbol.split("-")[0]!);
        return await lighterHistory(markets, since.getTime(), until.getTime());
      }
    }
  })();

  if (points.length === 0) {
    return null;
  }
  return {
    instrumentId: instrument.instrumentId,
    venue,
    since: since.toISOString(),
    until: until.toISOString(),
    observedAt: new Date().toISOString(),
    points: settle(points, since.getTime(), until.getTime()),
  };
}

/* --- The reads the handlers make ------------------------------------------ */

/** True when the venue publishes candles over public REST at all. */
export function hasCandles(instrument: Instrument): boolean {
  // lighter's candlestick route answers 403 to an anonymous caller; its chart
  // is a websocket subscription, which is what the live gateway uses too. So
  // there is no public read to make here, and the chart says so rather than
  // showing a shape invented for it.
  return instrument.venue !== "lighter";
}

export async function fetchMarketFact(instrument: Instrument): Promise<MarketFact> {
  return await cached(`book:${instrument.instrumentId}`, async () => {
    const book = await (instrument.venue === "binance"
      ? binanceBook(instrument)
      : instrument.venue === "bybit"
        ? bybitBook(instrument)
        : instrument.venue === "okx"
          ? okxBook(instrument)
          : lighterBook(instrument));
    return factOf(instrument, book, new Date().toISOString());
  });
}

export async function fetchCandles(
  instrument: Instrument,
  interval: CandleInterval,
  limit: number,
): Promise<Candle[]> {
  return await cached(`candles:${instrument.instrumentId}:${interval}:${limit}`, async () => {
    if (!hasCandles(instrument)) {
      throw new Error(`${instrument.venue} publishes no public candle history`);
    }
    return await (instrument.venue === "binance"
      ? binanceCandles(instrument, interval, limit)
      : instrument.venue === "bybit"
        ? bybitCandles(instrument, interval, limit)
        : okxCandles(instrument, interval, limit));
  });
}
