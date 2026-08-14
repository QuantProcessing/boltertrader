package factoryclient

import (
	"context"
	"strconv"

	"github.com/QuantProcessing/boltertrader/exchange"
	lighter "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

type lighterFundingBoardLoader struct {
	settings Settings
}

func NewLighterFundingBoardLoader(settings Settings) exchange.FundingBoardLoader {
	return lighterFundingBoardLoader{settings: settings}
}

// LoadFundingBoard reads Lighter's funding surface.
//
// This is the thinnest board on the venue list, and that is a property of the
// feed rather than of the loader: Lighter publishes a rate and nothing else —
// no mark, no index, no open interest, no settlement clock and no interval. All
// five optional cells are therefore left absent, which is the whole reason the
// board type makes them optional. Filling any of them with a placeholder would
// put a number on screen that Lighter never said.
//
// The SDK already restricts the response to Lighter's own rates; the endpoint
// also carries other venues' rates for comparison, and those must not reach a
// column labelled Lighter.
func (loader lighterFundingBoardLoader) LoadFundingBoard(ctx context.Context, scope exchange.CatalogScope) (exchange.FundingBoardSnapshot, error) {
	sdk := lighter.NewClient()
	if loader.settings.Environment == "testnet" {
		sdk.WithEnvironment(lighter.EnvironmentTestnet)
	} else {
		sdk.WithEnvironment(lighter.EnvironmentMainnet)
	}
	if loader.settings.Endpoint != "" {
		sdk.WithBaseURL(loader.settings.Endpoint)
	}
	sdk.HTTPClient = lighterTrackedHTTPClient(loader.settings.HTTPClient)

	rows, err := sdk.GetAllFundingRates(ctx)
	if err != nil {
		return exchange.FundingBoardSnapshot{}, lighterNormalizeErr(exchange.ProductPerp, fundingBoardOperation, err)
	}

	entries := make([]exchange.FundingBoardEntry, 0, len(rows))
	for _, row := range rows {
		if row == nil || row.Symbol == "" {
			continue
		}
		entries = append(entries, exchange.FundingBoardEntry{
			Symbol:       row.Symbol + "-USDC",
			NativeSymbol: row.Symbol,
			NativeID:     strconv.Itoa(row.MarketId),
			BaseAsset:    row.Symbol,
			QuoteAsset:   "USDC",
			Rate:         decimal.NewFromFloat(row.Rate),
		})
	}
	return fundingBoardSnapshot(scope, entries), nil
}
