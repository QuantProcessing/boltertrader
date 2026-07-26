package factoryclient

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	bitget "github.com/QuantProcessing/boltertrader/sdk/bitget"
	bybit "github.com/QuantProcessing/boltertrader/sdk/bybit"
)

type bybitInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewBybitInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return bybitInstrumentLoader{product: product, settings: settings}
}

func (loader bybitInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
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
	category := "spot"
	if loader.product == exchange.ProductPerp {
		category = "linear"
	}
	rows, err := sdk.GetInstruments(ctx, category)
	if err != nil {
		return exchange.InstrumentSnapshot{}, normErr(clientMeta{venue: exchange.VenueBybit, product: loader.product}, "InstrumentCatalog.Load", err)
	}
	instruments, err := bybitInstrumentsFromRows(loader.product, scope.Settlement, rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   filterInstrumentsForScope(instruments, scope),
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func bybitInstrumentsFromRows(product exchange.Product, settlement string, rows []bybit.Instrument) ([]exchange.Instrument, error) {
	meta := clientMeta{venue: exchange.VenueBybit, product: product}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if !stringsEqualFold(row.Status, "Trading") {
			continue
		}
		if product == exchange.ProductPerp {
			if settlement != "" && !stringsEqualFold(row.SettleCoin, settlement) {
				continue
			}
			if row.ContractType != "" && !stringsEqualFold(row.ContractType, "LinearPerpetual") {
				continue
			}
		}
		canonical := canonicalSymbol(row.Symbol, row.BaseCoin, row.QuoteCoin)
		if err := requireNativeMatchesMetadata(row.Symbol, row.BaseCoin, row.QuoteCoin); err != nil {
			return nil, err
		}
		priceIncrement, err := parseOptionalDecimal(meta, "InstrumentCatalog.Load", row.PriceFilter.TickSize)
		if err != nil {
			return nil, err
		}
		quantityIncrement, err := firstPositiveDecimalStrict(meta, "InstrumentCatalog.Load", row.LotSizeFilter.QtyStep, row.LotSizeFilter.BasePrecision)
		if err != nil {
			return nil, err
		}
		minQuantity, err := parseOptionalDecimal(meta, "InstrumentCatalog.Load", row.LotSizeFilter.MinOrderQty)
		if err != nil {
			return nil, err
		}
		minNotional, err := optionalPositiveStrict(meta, "InstrumentCatalog.Load", row.LotSizeFilter.MinNotionalValue, row.LotSizeFilter.MinOrderAmt)
		if err != nil {
			return nil, err
		}
		out = append(out, newInstrument(
			exchange.VenueBybit,
			product,
			canonical,
			row.BaseCoin,
			row.QuoteCoin,
			row.SettleCoin,
			row.Symbol,
			"",
			priceIncrement,
			quantityIncrement,
			minQuantity,
			minNotional,
			oneContract(),
		))
	}
	return out, nil
}

type bitgetInstrumentLoader struct {
	product     exchange.Product
	productType string
	settings    Settings
}

func NewBitgetInstrumentLoader(product exchange.Product, productType string, settings Settings) exchange.InstrumentLoader {
	return bitgetInstrumentLoader{product: product, productType: productType, settings: settings}
}

func (loader bitgetInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
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
	category := "spot"
	if loader.product == exchange.ProductPerp {
		category = loader.productType
	}
	rows, err := sdk.GetInstruments(ctx, category, "")
	if err != nil {
		return exchange.InstrumentSnapshot{}, normErr(clientMeta{venue: exchange.VenueBitget, product: loader.product}, "InstrumentCatalog.Load", err)
	}
	instruments, err := bitgetInstrumentsFromRows(loader.product, bitgetMarginCoin(loader.productType), rows)
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   filterInstrumentsForScope(instruments, scope),
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func bitgetInstrumentsFromRows(product exchange.Product, marginCoin string, rows []bitget.Instrument) ([]exchange.Instrument, error) {
	meta := clientMeta{venue: exchange.VenueBitget, product: product}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.Status != "" && !stringsEqualFold(row.Status, "online") && !stringsEqualFold(row.Status, "normal") {
			continue
		}
		if product == exchange.ProductSpot &&
			row.SymbolType != "" &&
			!stringsEqualFold(row.SymbolType, "crypto") {
			continue
		}
		settle := ""
		if product == exchange.ProductPerp {
			settle = marginCoin
			rowQuote := row.QuoteCoin
			if rowQuote == "" {
				rowQuote = quoteAsset(row.Symbol)
			}
			if rowQuote != marginCoin {
				continue
			}
		}
		if err := requireNativeMatchesMetadata(row.Symbol, row.BaseCoin, row.QuoteCoin); err != nil {
			return nil, err
		}
		priceIncrement, err := firstPositiveDecimalStrict(meta, "InstrumentCatalog.Load", row.PriceMultiplier, precisionIncrement(row.PricePrecision))
		if err != nil {
			return nil, err
		}
		if !priceIncrement.IsPositive() {
			return nil, malformed(meta, "InstrumentCatalog.Load", "price increment is required")
		}
		quantityIncrement, err := firstPositiveDecimalStrict(meta, "InstrumentCatalog.Load", row.QuantityMultiplier, precisionIncrement(row.QuantityPrecision))
		if err != nil {
			return nil, err
		}
		if !quantityIncrement.IsPositive() {
			return nil, malformed(meta, "InstrumentCatalog.Load", "quantity increment is required")
		}
		minQuantity, err := parseOptionalDecimal(meta, "InstrumentCatalog.Load", row.MinOrderQty)
		if err != nil {
			return nil, err
		}
		minNotional, err := optionalPositiveStrict(meta, "InstrumentCatalog.Load", row.MinOrderAmount)
		if err != nil {
			return nil, err
		}
		out = append(out, newInstrument(
			exchange.VenueBitget,
			product,
			canonicalSymbol(row.Symbol, row.BaseCoin, row.QuoteCoin),
			row.BaseCoin,
			row.QuoteCoin,
			settle,
			row.Symbol,
			"",
			priceIncrement,
			quantityIncrement,
			minQuantity,
			minNotional,
			oneContract(),
		))
	}
	return out, nil
}

func bitgetMarginCoin(productType string) string {
	if stringsContains(productType, "USDC") {
		return "USDC"
	}
	return "USDT"
}

func requireNativeMatchesMetadata(native, base, quote string) error {
	if base == "" || quote == "" {
		return fmt.Errorf("instrument metadata is incomplete")
	}
	want := base + quote
	if strings.EqualFold(native, want) || strings.EqualFold(native, base+"PERP") {
		return nil
	}
	return fmt.Errorf("native symbol does not match base/quote metadata")
}

func stringsEqualFold(a, b string) bool {
	return strings.EqualFold(a, b)
}

func stringsContains(a, b string) bool {
	return strings.Contains(a, b)
}
