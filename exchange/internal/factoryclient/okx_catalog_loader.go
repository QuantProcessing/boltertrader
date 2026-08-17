package factoryclient

import (
	"context"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/sdk/okx"
)

type okxInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewOKXInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return okxInstrumentLoader{product: product, settings: settings}
}

func (loader okxInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	sdk := okx.NewClient()
	if loader.settings.Endpoint != "" {
		sdk.WithBaseURL(loader.settings.Endpoint)
	}
	if loader.settings.HTTPClient != nil {
		sdk.WithHTTPClient(loader.settings.HTTPClient)
	}

	instType := okxSpotType
	if loader.product == exchange.ProductPerp {
		instType = okxSwapType
	}
	rows, err := sdk.GetInstruments(ctx, instType)
	if err != nil {
		return exchange.InstrumentSnapshot{}, okxNormalizeErr(loader.product, "InstrumentCatalog.Load", err)
	}

	var instruments []exchange.Instrument
	switch loader.product {
	case exchange.ProductSpot:
		instruments, err = okxSpotInstruments(rows)
	case exchange.ProductPerp:
		var metas map[string]okxContractMeta
		metas, err = okxPerpMetasFromRows(rows)
		instruments = make([]exchange.Instrument, 0, len(metas))
		for _, meta := range metas {
			instruments = append(instruments, meta.instrument)
		}
	default:
		err = okxInvalid(loader.product, "InstrumentCatalog.Load", "unsupported OKX product")
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
