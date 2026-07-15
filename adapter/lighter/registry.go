package lighter

import (
	"fmt"
	"strings"

	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/lighter"
	"github.com/shopspring/decimal"
)

const venueName = "LIGHTER"

type registry struct {
	byID       map[string]*model.Instrument
	byMarketID map[int]*model.Instrument
}

// AccountIDForIndex is retained for compatibility with older tests/callers.
// AccountIndex is a Lighter selector, not a runtime account identity.
func AccountIDForIndex(index int64) string {
	return AccountIDDefault
}

func newRegistry(insts []*model.Instrument) *registry {
	r := &registry{
		byID:       make(map[string]*model.Instrument, len(insts)),
		byMarketID: make(map[int]*model.Instrument, len(insts)),
	}
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		cp := *inst
		r.byID[cp.ID.String()] = &cp
		if cp.AssetIndex != nil {
			r.byMarketID[*cp.AssetIndex] = &cp
		}
	}
	return r
}

func newRegistryFromOrderBookDetails(details *sdk.OrderBookDetailsResponse) (*registry, error) {
	if details == nil {
		return nil, fmt.Errorf("lighter: missing order book details")
	}
	insts := make([]*model.Instrument, 0, len(details.OrderBookDetails)+len(details.SpotOrderBookDetails))
	for _, detail := range details.OrderBookDetails {
		inst, err := instrumentFromOrderBookDetail(detail, enums.KindPerp)
		if err != nil {
			return nil, err
		}
		insts = append(insts, inst)
	}
	for _, detail := range details.SpotOrderBookDetails {
		inst, err := instrumentFromOrderBookDetail(detail, enums.KindSpot)
		if err != nil {
			return nil, err
		}
		insts = append(insts, inst)
	}
	return newRegistry(insts), nil
}

func instrumentFromOrderBookDetail(detail *sdk.OrderBookDetail, kind enums.InstrumentKind) (*model.Instrument, error) {
	if detail == nil {
		return nil, fmt.Errorf("lighter: nil order book detail")
	}
	marketType := strings.ToLower(strings.TrimSpace(detail.MarketType))
	if kind == enums.KindPerp && marketType != "" && marketType != string(sdk.MarketTypePerp) {
		return nil, fmt.Errorf("lighter: expected perp market type for %s, got %q", detail.Symbol, detail.MarketType)
	}
	if kind == enums.KindSpot && marketType != "" && marketType != string(sdk.MarketTypeSpot) {
		return nil, fmt.Errorf("lighter: expected spot market type for %s, got %q", detail.Symbol, detail.MarketType)
	}
	base, quote := lighterSymbolCurrencies(detail.Symbol, kind)
	sizeDecimals := firstPositiveInt(int(detail.SizeDecimals), int(detail.SupportedSizeDecimals))
	priceDecimals := firstPositiveInt(int(detail.PriceDecimals), int(detail.SupportedPriceDecimals))
	priceTick := decimal.NewFromInt(1).Shift(int32(-priceDecimals))
	sizeStep := decimal.NewFromInt(1).Shift(int32(-sizeDecimals))
	marketID := detail.MarketId
	inst := &model.Instrument{
		ID: model.InstrumentID{
			Venue:  venueName,
			Symbol: base + "-" + quote,
			Kind:   kind,
		},
		Base:               base,
		Quote:              quote,
		Settle:             quote,
		VenueSymbol:        detail.Symbol,
		AssetIndex:         &marketID,
		PriceTick:          priceTick,
		SizeStep:           sizeStep,
		MinQty:             dec(detail.MinBaseAmount),
		MinNotional:        dec(detail.MinQuoteAmount),
		PricePrecision:     priceDecimals,
		ContractMultiplier: decimal.NewFromInt(1),
		PositionMode:       model.NetOnly,
	}
	if kind == enums.KindSpot {
		inst.Settle = ""
	}
	return inst, nil
}

func lighterSymbolCurrencies(symbol string, kind enums.InstrumentKind) (string, string) {
	symbol = strings.TrimSpace(strings.ToUpper(symbol))
	if base, quote, ok := strings.Cut(symbol, "/"); ok {
		return base, quote
	}
	if base, quote, ok := strings.Cut(symbol, "-"); ok {
		return base, quote
	}
	if kind == enums.KindPerp {
		return symbol, "USDC"
	}
	return symbol, "USDC"
}

func firstPositiveInt(values ...int) int {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func (r *registry) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if r == nil {
		return nil, false
	}
	inst, ok := r.byID[id.String()]
	if !ok {
		return nil, false
	}
	cp := *inst
	return &cp, true
}

func (r *registry) All() []*model.Instrument {
	if r == nil {
		return nil
	}
	out := make([]*model.Instrument, 0, len(r.byID))
	for _, inst := range r.byID {
		cp := *inst
		out = append(out, &cp)
	}
	return out
}

func (r *registry) byMarket(marketID int) (*model.Instrument, bool) {
	if r == nil {
		return nil, false
	}
	inst, ok := r.byMarketID[marketID]
	if !ok {
		return nil, false
	}
	cp := *inst
	return &cp, true
}

func intPtr(v int) *int { return &v }

func dec(s string) decimal.Decimal {
	if strings.TrimSpace(s) == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}
