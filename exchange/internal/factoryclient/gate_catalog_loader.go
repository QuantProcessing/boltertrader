package factoryclient

import (
	"context"
	"strings"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	gate "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

type gateInstrumentLoader struct {
	product  exchange.Product
	settings Settings
}

func NewGateInstrumentLoader(product exchange.Product, settings Settings) exchange.InstrumentLoader {
	return gateInstrumentLoader{product: product, settings: settings}
}

func (loader gateInstrumentLoader) LoadInstruments(ctx context.Context, scope exchange.CatalogScope) (exchange.InstrumentSnapshot, error) {
	sdk, _ := newGateSDK("", "", loader.settings)
	var (
		instruments []exchange.Instrument
		err         error
	)
	switch loader.product {
	case exchange.ProductSpot:
		var rows []gate.CurrencyPair
		rows, err = sdk.ListCurrencyPairs(ctx)
		if err == nil {
			instruments, err = gateSpotCatalogInstruments(rows)
		}
	case exchange.ProductPerp:
		var rows []gate.Contract
		rows, err = sdk.ListFuturesContracts(ctx, gate.SettleUSDT)
		if err == nil {
			instruments, err = gatePerpCatalogInstruments(gateCatalogTradableFuturesRows(rows), gate.SettleUSDT)
		}
	default:
		err = exchange.NewError(exchange.KindInvalidConfig, exchange.ErrorDetails{
			Venue:       exchange.VenueGate,
			Product:     loader.product,
			Operation:   "InstrumentCatalog.Load",
			SafeMessage: "unsupported Gate product",
		})
	}
	if err != nil {
		return exchange.InstrumentSnapshot{}, gateNormalizeErr(clientMeta{venue: exchange.VenueGate, product: loader.product}, "InstrumentCatalog.Load", err)
	}
	return exchange.InstrumentSnapshot{
		SchemaVersion: 1,
		Scope:         scope,
		Instruments:   filterInstrumentsForScope(instruments, scope),
		LoadedAt:      time.Now().UTC(),
	}, nil
}

func gateCatalogTradableFuturesRows(rows []gate.Contract) []gate.Contract {
	filtered := make([]gate.Contract, 0, len(rows))
	for _, row := range rows {
		if row.Status != "" && row.Status != "trading" {
			continue
		}
		if row.OrderSizeMin <= 0 {
			continue
		}
		filtered = append(filtered, row)
	}
	return filtered
}

func gateSpotCatalogInstruments(rows []gate.CurrencyPair) ([]exchange.Instrument, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductSpot}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.TradeStatus != "" && row.TradeStatus != "tradable" {
			continue
		}
		if err := gateValidateSpotInstrumentRow(meta, "InstrumentCatalog.Load", row); err != nil {
			return nil, err
		}
		out = append(out, newInstrument(
			exchange.VenueGate,
			exchange.ProductSpot,
			row.ID,
			row.Base,
			row.Quote,
			"",
			row.ID,
			"",
			decimal.New(1, -int32(row.Precision)),
			decimal.New(1, -int32(row.AmountPrecision)),
			gateDecimal(row.MinBaseAmount),
			gateOptionalDecimal(row.MinQuoteAmount),
			oneContract(),
		))
	}
	return out, nil
}

func gatePerpCatalogInstruments(rows []gate.Contract, settle string) ([]exchange.Instrument, error) {
	meta := clientMeta{venue: exchange.VenueGate, product: exchange.ProductPerp}
	out := make([]exchange.Instrument, 0, len(rows))
	for _, row := range rows {
		if row.Status != "" && row.Status != "trading" {
			continue
		}
		if err := gateValidateFuturesInstrumentRow(meta, "InstrumentCatalog.Load", row); err != nil {
			return nil, err
		}
		multiplier, err := gateFuturesMultiplier(meta, "InstrumentCatalog.Load", row.QuantoMultiplier)
		if err != nil {
			return nil, err
		}
		base, quote := gateSplitSymbol(row.Name)
		out = append(out, newInstrument(
			exchange.VenueGate,
			exchange.ProductPerp,
			row.Name,
			base,
			quote,
			strings.ToUpper(settle),
			row.Name,
			"",
			gateDecimal(row.OrderPriceRound),
			multiplier,
			decimal.NewFromInt(row.OrderSizeMin).Mul(multiplier),
			exchange.OptionalDecimal{},
			exchange.OptionalDecimal{Value: multiplier, Valid: true},
		))
	}
	return out, nil
}

func gateValidateSpotInstrumentRow(meta clientMeta, operation string, row gate.CurrencyPair) error {
	if strings.TrimSpace(row.ID) == "" || strings.TrimSpace(row.Base) == "" || strings.TrimSpace(row.Quote) == "" {
		return gateMalformed(meta, operation, "gate spot instrument identity is incomplete")
	}
	if row.Precision < 0 || row.AmountPrecision < 0 {
		return gateMalformed(meta, operation, "gate spot instrument precision is invalid")
	}
	if err := gateValidatePositiveDecimal(meta, operation, "minimum base amount", row.MinBaseAmount); err != nil {
		return err
	}
	if row.MinQuoteAmount != "" {
		if err := gateValidatePositiveDecimal(meta, operation, "minimum quote amount", row.MinQuoteAmount); err != nil {
			return err
		}
	}
	return nil
}

func gateValidateFuturesInstrumentRow(meta clientMeta, operation string, row gate.Contract) error {
	if strings.TrimSpace(row.Name) == "" {
		return gateMalformed(meta, operation, "gate futures instrument identity is incomplete")
	}
	if row.OrderSizeMin <= 0 {
		return gateMalformed(meta, operation, "gate futures minimum order size is invalid")
	}
	if err := gateValidatePositiveDecimal(meta, operation, "order price round", row.OrderPriceRound); err != nil {
		return err
	}
	return nil
}
