package factoryclient

import (
	"context"
	"strconv"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	nadosdk "github.com/QuantProcessing/boltertrader/sdk/nado"
)

type nadoInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewNadoInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return nadoInstrumentLoader{product: product, settings: settings}
}

func (loader nadoInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	base := newNadoBase(loader.product, "", "", Settings{
		Endpoint:          loader.settings.Endpoint,
		WebSocketEndpoint: loader.settings.WebSocketEndpoint,
		HTTPClient:        loader.settings.HTTPClient,
		Environment:       loader.settings.Environment,
	})
	products, err := base.discover(ctx, "InstrumentCatalog.Load")
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	instruments := make([]exchange.Instrument, 0, len(products))
	for _, product := range products {
		instruments = append(instruments, product.instrument)
	}
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   filterInstrumentsForScope(instruments, scope),
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func nadoInstrumentFromDiscovery(exchangeProduct exchange.Product, symbol nadosdk.Symbol, ticker string) (exchange.Instrument, error) {
	marketType := nadosdk.MarketType(symbol.Type)
	priceIncrement, err := nadoX18(symbol.PriceIncrementX18)
	if err != nil || !priceIncrement.IsPositive() {
		return exchange.Instrument{}, nadoMalformed(exchangeProduct, "InstrumentCatalog.Load", "invalid price increment")
	}
	quantityIncrement, err := nadoX18(symbol.SizeIncrement)
	if err != nil || !quantityIncrement.IsPositive() {
		return exchange.Instrument{}, nadoMalformed(exchangeProduct, "InstrumentCatalog.Load", "invalid quantity increment")
	}
	minSize, err := nadoX18(symbol.MinSize)
	if err != nil {
		return exchange.Instrument{}, nadoMalformed(exchangeProduct, "InstrumentCatalog.Load", "invalid min size")
	}
	base, quote := nadoAssets(nadoCanonical(ticker), marketType)
	settle := ""
	if marketType == nadosdk.MarketTypeSpot {
		settle = quote
	}
	if marketType == nadosdk.MarketTypePerp {
		if strings.HasSuffix(base, "-PERP") {
			base = strings.TrimSuffix(base, "-PERP")
		}
		settle = quote
	}
	return newInstrument(
		exchange.VenueNado,
		exchangeProduct,
		nadoCanonical(ticker),
		base,
		quote,
		settle,
		ticker,
		strconv.Itoa(symbol.ProductID),
		priceIncrement,
		quantityIncrement,
		quantityIncrement,
		exchange.OptionalDecimal{Value: minSize, Valid: minSize.IsPositive()},
		oneContract(),
	), nil
}
