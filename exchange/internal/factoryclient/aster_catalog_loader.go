package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	astercommon "github.com/QuantProcessing/boltertrader/sdk/aster/common"
	asterperp "github.com/QuantProcessing/boltertrader/sdk/aster/perp"
	asterspot "github.com/QuantProcessing/boltertrader/sdk/aster/spot"
)

type asterInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewAsterInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return asterInstrumentLoader{product: product, settings: settings}
}

func (loader asterInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
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
		err = asterErr(loader.product, "InstrumentCatalog.Load", exchange.KindInvalidRequest, "unsupported Aster product")
	}
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	instruments = filterInstrumentsForScope(instruments, scope)
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   instruments,
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func (loader asterInstrumentLoader) loadSpot(ctx context.Context) ([]exchange.Instrument, error) {
	profile, err := asterProfileForSettings(astercommon.ProductSpot, loader.settings)
	if err != nil {
		return nil, asterErr(exchange.ProductSpot, "InstrumentCatalog.Load", exchange.KindInvalidConfig, err.Error())
	}
	client, err := asterspot.NewClient(profile, nil)
	if err != nil {
		return nil, asterErr(exchange.ProductSpot, "InstrumentCatalog.Load", exchange.KindInvalidConfig, err.Error())
	}
	client.WithHTTPClient(loader.settings.HTTPClient)
	resp, err := client.ExchangeInfo(ctx)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductSpot, "InstrumentCatalog.Load", err)
	}
	return asterSpotInstruments(resp)
}

func (loader asterInstrumentLoader) loadPerp(ctx context.Context) ([]exchange.Instrument, error) {
	profile, err := asterProfileForSettings(astercommon.ProductPerp, loader.settings)
	if err != nil {
		return nil, asterErr(exchange.ProductPerp, "InstrumentCatalog.Load", exchange.KindInvalidConfig, err.Error())
	}
	client, err := asterperp.NewClient(profile, nil)
	if err != nil {
		return nil, asterErr(exchange.ProductPerp, "InstrumentCatalog.Load", exchange.KindInvalidConfig, err.Error())
	}
	client.WithHTTPClient(loader.settings.HTTPClient)
	resp, err := client.ExchangeInfo(ctx)
	if err != nil {
		return nil, asterNormalizeErr(exchange.ProductPerp, "InstrumentCatalog.Load", err)
	}
	return asterPerpInstruments(resp)
}

func asterProfileForSettings(product astercommon.Product, settings Settings) (astercommon.Profile, error) {
	env := astercommon.EnvironmentProduction
	if settings.Environment == "testnet" || settings.Environment == "demo" {
		env = astercommon.EnvironmentTestnet
	}
	profile, err := astercommon.NewProfile(env, product)
	if err != nil {
		return astercommon.Profile{}, err
	}
	return profile.WithEndpointOverrides(settings.Endpoint, settings.WebSocketEndpoint, settings.WebSocketEndpoint)
}

func asterSpotInstruments(resp *asterspot.ExchangeInfoResponse) ([]exchange.Instrument, error) {
	if resp == nil {
		return nil, asterMalformed(exchange.ProductSpot, "InstrumentCatalog.Load", "missing exchange info")
	}
	out := make([]exchange.Instrument, 0, len(resp.Symbols))
	for _, row := range resp.Symbols {
		if row.Status != "TRADING" {
			continue
		}
		price, qty, minQty, minNotional, err := asterSpotFilters(row.Filters)
		if err != nil {
			return nil, asterMalformed(exchange.ProductSpot, "InstrumentCatalog.Load", err.Error())
		}
		out = append(out, newInstrument(
			exchange.VenueAster,
			exchange.ProductSpot,
			asterCanonical(row.Symbol),
			row.BaseAsset,
			row.QuoteAsset,
			"",
			row.Symbol,
			"",
			price,
			qty,
			minQty,
			minNotional,
			oneContract(),
		))
	}
	return out, nil
}

func asterPerpInstruments(resp *asterperp.ExchangeInfoResponse) ([]exchange.Instrument, error) {
	if resp == nil {
		return nil, asterMalformed(exchange.ProductPerp, "InstrumentCatalog.Load", "missing exchange info")
	}
	out := make([]exchange.Instrument, 0, len(resp.Symbols))
	for _, row := range resp.Symbols {
		if row.Status != "TRADING" || row.ContractType != "PERPETUAL" || row.MarginAsset != "USDT" {
			continue
		}
		price, qty, minQty, minNotional, err := asterPerpFilters(row.Filters)
		if err != nil {
			return nil, asterMalformed(exchange.ProductPerp, "InstrumentCatalog.Load", err.Error())
		}
		out = append(out, newInstrument(
			exchange.VenueAster,
			exchange.ProductPerp,
			asterCanonical(row.Symbol),
			row.BaseAsset,
			row.QuoteAsset,
			row.MarginAsset,
			row.Symbol,
			"",
			price,
			qty,
			minQty,
			minNotional,
			oneContract(),
		))
	}
	return out, nil
}
