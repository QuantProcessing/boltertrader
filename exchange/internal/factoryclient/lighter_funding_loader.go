package factoryclient

import (
	"context"
	"strconv"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

// lighterFundingPercent converts Lighter's percentage-quoted history rate to
// the fraction every other venue reports and the rest of the system assumes.
//
// The board loader below needs no such divisor: GetAllFundingRates already
// answers in signed fractions. The two Lighter surfaces disagree on units, and
// this is the one that departs from the house convention.
var lighterFundingPercent = decimal.NewFromInt(100)

// Lighter settles every hour but applies one eighth of the displayed funding
// rate at each settlement. The public all-market comparison endpoint therefore
// carries an eight-hour-equivalent quote, which is the period consumers need
// when normalising the rate to a day.
const lighterFundingQuoteHours int64 = 8

type lighterFundingBoardLoader struct {
	settings Settings
}

func NewLighterFundingBoardLoader(settings Settings) exchange.FundingBoardLoader {
	return lighterFundingBoardLoader{settings: settings}
}

// LoadFundingBoard reads Lighter's funding surface.
//
// Lighter exposes the funding comparison and market statistics through two
// public endpoints. The first is authoritative for the rate; the second is a
// best-effort bootstrap for mark, index and open interest so price strategies
// do not need to wait for the first WebSocket frame. A failure of that
// enrichment never suppresses the funding row.
//
// The SDK already restricts the response to Lighter's own rates; the endpoint
// also carries other venues' rates for comparison, and those must not reach a
// column labelled Lighter.
func (loader lighterFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk := newLighterPublicSDK(loader.settings)

	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, lighterNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}
	type marketDetail struct {
		markPrice    string
		indexPrice   string
		openInterest string
	}
	details := map[int]marketDetail{}
	if response, detailErr := sdk.GetOrderBookDetails(ctx, nil, nil); detailErr == nil && response != nil {
		for _, detail := range response.OrderBookDetails {
			if detail == nil {
				continue
			}
			details[detail.MarketId] = marketDetail{
				markPrice: detail.MarkPrice, indexPrice: detail.IndexPrice,
				openInterest: strconv.FormatFloat(detail.OpenInterest, 'f', -1, 64),
			}
		}
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Symbol == "" {
			continue
		}
		entry := exchange.FundingBoardEntry{
			Symbol:       row.Symbol + "-USDC",
			NativeSymbol: row.Symbol,
			NativeID:     strconv.Itoa(row.MarketId),
			BaseAsset:    row.Symbol,
			QuoteAsset:   "USDC",
			Rate:         decimal.NewFromFloat(row.Rate),
			IntervalHours: exchange.OptionalInt{
				Value: lighterFundingQuoteHours,
				Valid: true,
			},
		}
		if detail, ok := details[row.MarketId]; ok {
			entry.MarkPrice = fundingOptionalDecimal(detail.markPrice)
			entry.IndexPrice = fundingOptionalDecimal(detail.indexPrice)
			entry.OpenInterest = fundingOptionalNonNegative(detail.openInterest)
			entry.OpenInterestNotional = fundingOpenInterestNotional(
				detail.openInterest, detail.markPrice, decimal.NewFromInt(1),
			)
			if entry.OpenInterestNotional.Valid {
				entry.OpenInterestNotionalAsset = "USDC"
			}
		}
		entries = append(entries, entry)
	}
	return fundingBoardSnapshot(scope, entries), nil
}

type lighterFundingHistoryLoader struct {
	settings Settings
}

func NewLighterFundingHistoryLoader(settings Settings) exchange.FundingHistoryLoader {
	return lighterFundingHistoryLoader{settings: settings}
}

// LoadFundingHistory reads one market's settled rates.
//
// Lighter is the only venue addressed by a numeric market id rather than by a
// symbol, so this is the one loader that requires NativeID and says so instead
// of falling back to the canonical spelling — a symbol sent where an integer is
// expected would come back as a parameter error, which reads as "the venue is
// down" rather than "the caller did not resolve the instrument".
//
// The clock is Lighter's: it settles hourly, which is why the resolution is
// stated rather than derived, and why count_back is one row per hour of the
// window. Lighter takes the window in seconds, matching how the candles reader
// in this package addresses the same API family. The endpoint returns at most
// 750 rows, which a thirty-day hourly window (721) fits under.
//
// Two conversions happen here that no other venue needs, and both were settled
// against the live feed because the API reference states units for neither.
//
// The rate is a percentage. Lighter publishes "0.0012" where it means 1.2e-5,
// and three independent readings agree: the row's own value field divided by
// rate/100 lands on the instrument's price; the funding-rates board reports
// 9.6e-05 for the same instrument at the same moment, which is exactly eight
// hourly 1.2e-5 settlements; and read as a fraction the series would imply
// 1051% a year against Hyperliquid's 11% on the same asset over the same week.
//
// The sign lives in direction, not in the rate. Four thousand rows across ten
// markets contained no negative rate string, while direction reported "short"
// on as many as forty percent of them — a funding series that never goes
// negative is not a funding series. "long" is longs paying shorts, the sense in
// which every other venue here reports a positive rate.
func (loader lighterFundingHistoryLoader) LoadFundingHistory(ctx context.Context, request exchange.FundingHistoryRequest) (exchange.FundingHistorySnapshot, error) {
	if _, _, err := fundingHistoryWindow(request); err != nil {
		return exchange.FundingHistorySnapshot{}, err
	}
	marketID, err := strconv.Atoi(request.NativeID)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, fundingHistoryInvalid("lighter funding history requires a numeric market id")
	}
	sdk := newLighterPublicSDK(loader.settings)

	// count_back is one row per hour of the window, clamped to what Lighter
	// will return. Thirty days is 721 and clears the ceiling; the clamp is
	// there so a wider window degrades to a shorter series rather than to
	// whatever a venue does with an over-large count.
	countBack := int64(request.Until.Sub(request.Since).Hours()) + 1
	if countBack > lighterFundingHistoryMax {
		countBack = lighterFundingHistoryMax
	}
	res, err := sdk.GetFundingHistory(ctx, marketID, "1h", request.Since.Unix(), request.Until.Unix(), countBack)
	if err != nil {
		return exchange.FundingHistorySnapshot{}, lighterNormalizeErr(exchange.ProductPerp, fundingHistoryOperation, err)
	}
	if res == nil {
		return fundingHistorySnapshot(request, nil), nil
	}

	points := make([]exchange.FundingHistoryPoint, 0, len(res.Fundings))
	for _, row := range res.Fundings {
		rate, err := decimalOrSkip(row.Rate)
		if err != nil {
			continue
		}
		rate = rate.Div(lighterFundingPercent)
		// An unrecognised direction is dropped rather than guessed. The two
		// known values decide a sign, and a third would mean Lighter changed
		// the field's meaning — plotting the magnitude with an assumed sign
		// would put a confident wrong number on the chart.
		switch row.Direction {
		case "long":
		case "short":
			rate = rate.Neg()
		default:
			continue
		}
		points = append(points, exchange.FundingHistoryPoint{
			FundingTime: fundingHistorySecondsTime(row.Timestamp),
			Rate:        rate,
		})
	}
	return fundingHistorySnapshot(request, points), nil
}
