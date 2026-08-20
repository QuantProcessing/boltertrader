package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type bybitFundingBoardLoader struct {
	settlement string
	settings   Settings
}

func NewBybitFundingBoardLoader(settlement string, settings Settings) exchange.FundingBoardLoader {
	return bybitFundingBoardLoader{settlement: settlement, settings: settings}
}

// LoadFundingBoard reads the whole linear ticker surface.
//
// Bybit is the venue that answers everything at once: rate, clock, interval,
// mark, index and open interest all ride on the ticker, so this loader needs a
// single call and fills every optional cell. It is the reason the board type
// has an open-interest column at all.
//
// The settlement filter is inferred from the symbol rather than read from the
// row, because tickers do not carry a settle coin — the linear category mixes
// USDT- and USDC-margined contracts, and a USDC row filed under the USDT scope
// would collide with a real USDT symbol on the board.
func (loader bybitFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk := newBybitPublicSDK(loader.settings)

	rows, err := sdk.GetTickers(ctx, "linear")
	if err != nil {
		meta := clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}
		return exchange.FundingBoardSnapshot{}, normErr(meta, fundingBoardOperation, err)
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		quote := quoteAsset(row.Symbol)
		if quote != loader.settlement {
			continue
		}
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		canonical := canonicalSymbol(row.Symbol, "", "")
		entries = append(entries, exchange.FundingBoardEntry{
			Symbol:          canonical,
			NativeSymbol:    row.Symbol,
			BaseAsset:       baseFromCanonical(canonical),
			QuoteAsset:      quote,
			Rate:            rate,
			IntervalHours:   fundingIntervalHours(row.FundingIntervalHour),
			NextFundingTime: fundingUnixMillis(row.NextFundingTime),
			MarkPrice:       fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:      fundingOptionalDecimal(row.IndexPrice),
			OpenInterest:    fundingOptionalNonNegative(row.OpenInterest),
			OpenInterestNotional: fundingOpenInterestNotional(
				row.OpenInterest, row.MarkPrice, decimal.NewFromInt(1),
			),
		})
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type bitgetFundingBoardLoader struct {
	productType string
	settings    Settings
}

func NewBitgetFundingBoardLoader(productType string, settings Settings) exchange.FundingBoardLoader {
	return bitgetFundingBoardLoader{productType: productType, settings: settings}
}

// LoadFundingBoard reads one Bitget product type's ticker surface.
//
// Unlike Bybit, Bitget scopes the request itself — USDT-FUTURES and
// USDC-FUTURES are separate categories — so no settlement filter is applied
// here beyond the one the category already performs.
//
// Bitget publishes no open interest in bulk, so that column stays empty for
// this venue. The interval is on the ticker for most symbols and on the
// instrument for the rest; the second call is best-effort because a board
// without an interval is still a board, while a board that failed to load is
// not.
func (loader bitgetFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk := newBitgetPublicSDK(loader.settings)

	rows, err := sdk.GetTickers(ctx, loader.productType)
	if err != nil {
		meta := clientMeta{venue: exchange.VenueBitget, product: exchange.ProductPerp}
		return exchange.FundingBoardSnapshot{}, normErr(meta, fundingBoardOperation, err)
	}
	intervals := map[string]exchange.OptionalInt{}
	if instruments, err := sdk.GetInstruments(ctx, loader.productType, ""); err == nil {
		for _, instrument := range instruments {
			if hours := fundingIntervalHours(instrument.FundInterval); hours.Valid {
				intervals[instrument.Symbol] = hours
			}
		}
	}

	marginCoin := bitgetMarginCoin(loader.productType)
	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		canonical := canonicalSymbol(row.Symbol, "", "")
		interval := fundingIntervalHours(row.FundingRateInterval)
		if !interval.Valid {
			interval = intervals[row.Symbol]
		}
		entries = append(entries, exchange.FundingBoardEntry{
			Symbol:          canonical,
			NativeSymbol:    row.Symbol,
			BaseAsset:       baseFromCanonical(canonical),
			QuoteAsset:      marginCoin,
			Rate:            rate,
			IntervalHours:   interval,
			NextFundingTime: fundingUnixMillis(row.NextFundingTime),
			MarkPrice:       fundingOptionalDecimal(row.MarkPrice),
			IndexPrice:      fundingOptionalDecimal(row.IndexPrice),
		})
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type bybitFundingHistoryLoader struct {
	settings Settings
}

func NewBybitFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return bybitFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one linear contract's settled rates.
//
// The category is the same literal the board uses: Bybit files both the USDT-
// and the USDC-margined perpetuals under linear, and inverse contracts are not
// a configured product here. There is therefore no settlement argument to take,
// where the board needed one to split a mixed ticker surface.
//
// Two hundred rows is Bybit's ceiling and the tightest of the venues that
// accept a window at all, so a thirty-day request against an hourly-settling
// contract comes back short. The series then starts later than asked, which the
// chart states by where its line begins.
func (loader bybitFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	since, until, err := fundingHistoryWindow(request)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk := newBybitPublicSDK(loader.settings)

	rows, err := sdk.GetFundingHistory(ctx, "linear", fundingHistoryNative(request), since, until, fundingHistoryLimit(request, bybitFundingHistoryMax))
	if err != nil {
		meta := clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}
		return exchange.FundingHistorySnapshot{}, normErr(meta, fundingHistoryOperation, err)
	}

	points := make([]exchange.FundingHistoryPoint, 0, len(rows))
	for _, row := range rows {
		rate, err := decimalOrSkip(row.FundingRate)
		if err != nil {
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{
			FundingTime: fundingUnixMillis(row.FundingRateTimestamp),
			Rate:        rate,
		})
	}
	return fundingHistorySnapshot(request, points), nil
}

type bitgetFundingHistoryLoader struct {
	productType string
	settings    Settings
}

func NewBitgetFundingHistoryLoader(productType string, settings Settings) exchange.FundingHistoryLoader {
	return bitgetFundingHistoryLoader{productType: productType, settings: settings}
}

// LoadFundingHistory walks Bitget's history pages back to the requested start.
//
// Bitget is the only one of the seven that takes no window: history-fund-rate
// pages backwards from the newest settlement and offers no way to say where to
// begin. Paging is therefore not an optimisation here but the only way to reach
// a start date, and the loop stops on the first page that reaches back past it.
//
// Rows older than the window are kept and handed to the finaliser rather than
// dropped in this loop. That is what lets the first in-window point measure its
// own interval against the settlement immediately before it, instead of being
// the one point on the chart with no interval.
//
// The page ceiling is a guard, not a budget: it bounds the walk if Bitget ever
// stops advancing the cursor, which would otherwise loop until the context
// expires.
func (loader bitgetFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	if _, _, err := fundingHistoryWindow(request); err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	sdk := newBitgetPublicSDK(loader.settings)
	native := fundingHistoryNative(request)

	points := []exchange.FundingHistoryPoint{}
	for page := 1; page <= bitgetFundingHistoryMaxPages; page++ {
		rows, err := sdk.GetHistoryFundRate(ctx, native, loader.productType, bitgetFundingHistoryPage, page)
		if err != nil {
			meta := clientMeta{venue: exchange.VenueBitget, product: exchange.ProductPerp}
			return exchange.FundingHistorySnapshot{}, normErr(meta, fundingHistoryOperation, err)
		}
		if len(rows) == 0 {
			break
		}
		reachedStart := false
		for _, row := range rows {
			settled := fundingUnixMillis(row.FundingTime)
			if settled.IsZero() {
				continue
			}
			if !settled.After(request.Since) {
				reachedStart = true
			}
			rate, err := decimalOrSkip(row.FundingRate)
			if err != nil {
				continue
			}
			points = append(points, exchange.FundingHistoryPoint{FundingTime: settled, Rate: rate})
		}
		if reachedStart || len(rows) < bitgetFundingHistoryPage {
			break
		}
	}
	return fundingHistorySnapshot(request, points), nil
}
