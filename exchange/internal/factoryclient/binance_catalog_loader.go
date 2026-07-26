package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	binanceperp "github.com/QuantProcessing/boltertrader/sdk/binance/perp"
	binancespot "github.com/QuantProcessing/boltertrader/sdk/binance/spot"
)

type binanceInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewBinanceInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return binanceInstrumentLoader{product: product, settings: settings}
}

func (loader binanceInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	var (
		instruments []exchange.Instrument
		err         error
	)
	switch loader.product {
	case exchange.ProductSpot:
		instruments, err = loader.loadSpot(ctx)
	case exchange.ProductPerp:
		instruments, err = loader.loadPerp(ctx)
	default:
		err = exchange.NewError(exchange.KindInvalidRequest, exchange.ErrorDetails{
			Venue:       exchange.VenueBinance,
			Product:     loader.product,
			Operation:   "InstrumentCatalog.Load",
			SafeMessage: "unsupported Binance product",
		})
	}
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	instruments = binanceFilterInstrumentsForScope(instruments, scope)
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   instruments,
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func binanceFilterInstrumentsForScope(instruments []exchange.Instrument, scope exchange.CatalogScope) []exchange.Instrument {
	filtered := make([]exchange.Instrument, 0, len(instruments))
	for _, instrument := range instruments {
		identity := instrument.Identity
		if scope.Venue != "" && identity.Venue != scope.Venue {
			continue
		}
		if scope.Product != "" && identity.Product != scope.Product {
			continue
		}
		if scope.Settlement != "" && identity.SettleAsset != scope.Settlement {
			continue
		}
		filtered = append(filtered, instrument)
	}
	return filtered
}

func (loader binanceInstrumentLoader) loadSpot(ctx context.Context) ([]exchange.Instrument, error) {
	sdk := binancespot.NewClient()
	env := binancespot.Environment(loader.settings.Environment)
	profile, err := binancespot.EndpointProfileForEnvironment(env)
	if err != nil {
		return nil, binanceSpotInvalid("InstrumentCatalog.Load", err.Error())
	}
	sdk.WithBaseURL(profile.RESTBaseURL)
	if loader.settings.Endpoint != "" {
		sdk.WithBaseURL(loader.settings.Endpoint)
	}
	if loader.settings.HTTPClient != nil {
		sdk.HTTPClient = loader.settings.HTTPClient
	}
	res, err := sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, binanceSpotNormalizeErr(err, "InstrumentCatalog.Load")
	}
	return binanceSpotInstrumentsFromExchangeInfo(res)
}

func (loader binanceInstrumentLoader) loadPerp(ctx context.Context) ([]exchange.Instrument, error) {
	sdk := binanceperp.NewClient()
	env := binanceperp.Environment(loader.settings.Environment)
	profile, err := binanceperp.EndpointProfileForEnvironment(env)
	if err != nil {
		return nil, binancePerpInvalidRequest("InstrumentCatalog.Load", err.Error())
	}
	sdk.WithEndpointProfile(profile)
	if loader.settings.Endpoint != "" {
		sdk.WithBaseURL(loader.settings.Endpoint)
	}
	if loader.settings.HTTPClient != nil {
		sdk.HTTPClient = loader.settings.HTTPClient
	}
	info, err := sdk.ExchangeInfo(ctx)
	if err != nil {
		return nil, binancePerpNormalizeError("InstrumentCatalog.Load", err)
	}
	return binancePerpInstrumentsFromExchangeInfo(info)
}
