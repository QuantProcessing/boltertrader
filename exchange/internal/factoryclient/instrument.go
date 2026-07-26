package factoryclient

import (
	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func newInstrument(
	venue exchange.Venue,
	product exchange.Product,
	symbol string,
	base string,
	quote string,
	settle string,
	nativeSymbol string,
	nativeID string,
	priceIncrement decimal.Decimal,
	quantityIncrement decimal.Decimal,
	minQuantity decimal.Decimal,
	minNotional exchange.OptionalDecimal,
	contractMultiplier exchange.OptionalDecimal,
) exchange.Instrument {
	return exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue:        venue,
			Product:      product,
			Symbol:       symbol,
			BaseAsset:    base,
			QuoteAsset:   quote,
			SettleAsset:  settle,
			NativeSymbol: nativeSymbol,
			NativeID:     nativeID,
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    priceIncrement,
			QuantityIncrement: quantityIncrement,
			MinQuantity:       minQuantity,
			MinNotional:       minNotional,
		},
		Encoding: exchange.InstrumentEncoding{
			ContractMultiplier: contractMultiplier,
		},
	}
}

func oneContract() exchange.OptionalDecimal {
	return exchange.OptionalDecimal{Value: decimal.NewFromInt(1), Valid: true}
}

func filterInstrumentsForScope(instruments []exchange.Instrument, scope exchange.CatalogScope) []exchange.Instrument {
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

func requiredNativeSymbol(instrument exchange.Instrument) string {
	return instrument.Identity.NativeSymbol
}

func optionalNativeSymbol(instrument *exchange.Instrument) string {
	if instrument == nil {
		return ""
	}
	return instrument.Identity.NativeSymbol
}

func decodedInstrument(venue exchange.Venue, product exchange.Product, symbol string) (exchange.Instrument, error) {
	return exchange.Instrument{}, exchange.NewError(exchange.KindMetadataResolution, exchange.ErrorDetails{
		Venue:       venue,
		Product:     product,
		Operation:   "InstrumentCatalog.Resolve",
		SafeMessage: "catalog-backed instrument resolution is required for native symbol " + symbol,
	})
}
