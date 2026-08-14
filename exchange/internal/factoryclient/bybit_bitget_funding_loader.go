package factoryclient

import (
	"context"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
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
	profile := bybit.MainnetEnvironmentProfile()
	switch loader.settings.Environment {
	case string(exchange.EnvironmentDemo):
		profile = bybit.DemoEnvironmentProfile()
	case string(exchange.EnvironmentTestnet):
		profile = bybit.TestnetEnvironmentProfile()
	}
	if loader.settings.Endpoint != "" {
		profile.RESTBaseURL = loader.settings.Endpoint
	}
	sdk := bybit.NewClient().WithEnvironmentProfile(profile)
	if loader.settings.HTTPClient != nil {
		sdk.WithHTTPClient(loader.settings.HTTPClient)
	}

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
	profile := bitget.MainnetEnvironmentProfile()
	if loader.settings.Environment == string(exchange.EnvironmentDemo) {
		profile = bitget.DemoEnvironmentProfile()
	}
	if loader.settings.Endpoint != "" {
		profile.RESTBaseURL = loader.settings.Endpoint
	}
	sdk := bitget.NewClient().WithEnvironmentProfile(profile)
	if loader.settings.HTTPClient != nil {
		sdk.WithHTTPClient(loader.settings.HTTPClient)
	}

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
