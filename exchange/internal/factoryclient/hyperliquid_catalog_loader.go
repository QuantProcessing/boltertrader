package factoryclient

import (
	"context"
	"sort"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	hyperliquidbase "github.com/QuantProcessing/boltertrader/sdk/hyperliquid"
	hyperliquidperp "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/perp"
	hyperliquidspot "github.com/QuantProcessing/boltertrader/sdk/hyperliquid/spot"
)

type hyperliquidInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewHyperliquidInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return hyperliquidInstrumentLoader{product: product, settings: settings}
}

func (loader hyperliquidInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
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
		err = hlInvalid(loader.product, "InstrumentCatalog.Load", "unsupported Hyperliquid product")
	}
	if err != nil {
		return exchange.InstrumentSnapshot{}, err
	}
	instruments = filterInstrumentsForScope(instruments, scope)
	sort.Slice(instruments, func(i, j int) bool {
		return instruments[i].Identity.Symbol < instruments[j].Identity.Symbol
	})
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   instruments,
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func (loader hyperliquidInstrumentLoader) loadSpot(ctx context.Context) ([]exchange.Instrument, error) {
	base := loader.publicBaseClient()
	meta, err := hyperliquidspot.NewClient(base).GetSpotMeta(ctx)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductSpot, "InstrumentCatalog.Load", err, nil)
	}
	metas, err := hlSpotMetas("InstrumentCatalog.Load", meta)
	if err != nil {
		return nil, err
	}
	return hlInstrumentsFromMetas(metas), nil
}

func (loader hyperliquidInstrumentLoader) loadPerp(ctx context.Context) ([]exchange.Instrument, error) {
	base := loader.publicBaseClient()
	client := hyperliquidperp.NewClient(base)
	full, err := client.GetMetaAndAssetCtxs(ctx)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductPerp, "InstrumentCatalog.Load", err, nil)
	}
	prep, err := client.GetPrepMeta(ctx)
	if err != nil {
		return nil, hlNormalizeQueryErr(exchange.ProductPerp, "InstrumentCatalog.Load", err, nil)
	}
	metas, err := hlPerpMetas("InstrumentCatalog.Load", full, prep)
	if err != nil {
		return nil, err
	}
	return hlInstrumentsFromMetas(metas), nil
}

func (loader hyperliquidInstrumentLoader) publicBaseClient() *hyperliquidbase.Client {
	base := hyperliquidbase.NewClient()
	if loader.settings.Environment == "testnet" {
		base.WithEnvironment(hyperliquidbase.EnvironmentTestnet)
	} else {
		base.WithEnvironment(hyperliquidbase.EnvironmentMainnet)
	}
	if loader.settings.Endpoint != "" {
		base.BaseURL = loader.settings.Endpoint
	}
	if loader.settings.HTTPClient != nil {
		base.Http = loader.settings.HTTPClient
	}
	return base
}

func hlInstrumentsFromMetas(metas map[string]hyperliquidMarketMeta) []exchange.Instrument {
	instruments := make([]exchange.Instrument, 0, len(metas))
	for _, meta := range metas {
		instruments = append(instruments, meta.instrument)
	}
	return instruments
}
