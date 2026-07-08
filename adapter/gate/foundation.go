package gate

import (
	"context"
	"fmt"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/adapter"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	"github.com/QuantProcessing/boltertrader/internal/errs"
	gatesdk "github.com/QuantProcessing/boltertrader/sdk/gate"
	"github.com/shopspring/decimal"
)

const (
	VenueName        = "GATE"
	AccountIDUnified = model.AccountIDGateDefault
)

type Config struct {
	APIKey    string
	APISecret string

	Environment gatesdk.EnvironmentProfile
	AccountID   string
	Products    []string
	HTTPClient  *http.Client
	Clock       clock.Clock
}

func DefaultConfig(environment gatesdk.EnvironmentProfile) Config {
	return Config{
		Environment: environment,
		AccountID:   AccountIDUnified,
		Products:    []string{gatesdk.ProductSpot, gatesdk.ProductFuturesUSDT},
	}
}

func AccountIDForKind(kind enums.InstrumentKind) string {
	switch kind {
	case enums.KindSpot, enums.KindPerp:
		return AccountIDUnified
	default:
		return ""
	}
}

var _ model.InstrumentProvider = (*instrumentProvider)(nil)

type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument
	bySymbol map[string]model.InstrumentID
	all      []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		bySymbol: make(map[string]model.InstrumentID),
	}
}

func (p *instrumentProvider) Load(ctx context.Context, client *gatesdk.Client, products ...string) error {
	if len(products) == 0 {
		products = []string{gatesdk.ProductSpot, gatesdk.ProductFuturesUSDT}
	}
	all := make([]*model.Instrument, 0)
	for _, product := range products {
		switch product {
		case gatesdk.ProductSpot:
			pairs, err := client.ListCurrencyPairs(ctx)
			if err != nil {
				return err
			}
			for i := range pairs {
				if inst := instrumentFromGateSpot(pairs[i]); inst != nil {
					all = append(all, inst)
				}
			}
		case gatesdk.ProductFuturesUSDT:
			contracts, err := client.ListFuturesContracts(ctx, gatesdk.SettleUSDT)
			if err != nil {
				return err
			}
			for i := range contracts {
				if inst := instrumentFromGateContract(gatesdk.SettleUSDT, contracts[i]); inst != nil {
					all = append(all, inst)
				}
			}
		default:
			return unsupportedProduct(product)
		}
	}
	p.LoadSnapshot(all)
	return nil
}

func (p *instrumentProvider) LoadSnapshot(insts []*model.Instrument) {
	byID := make(map[string]*model.Instrument, len(insts))
	bySymbol := make(map[string]model.InstrumentID, len(insts))
	all := make([]*model.Instrument, 0, len(insts))
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		byID[inst.ID.String()] = inst
		if inst.VenueSymbol != "" {
			bySymbol[venueSymbolKey(inst.VenueSymbol, inst.ID.Kind, inst.Settle)] = inst.ID
			if _, exists := bySymbol[inst.VenueSymbol]; !exists {
				bySymbol[inst.VenueSymbol] = inst.ID
			}
		}
		all = append(all, inst)
	}
	p.mu.Lock()
	p.byID, p.bySymbol, p.all = byID, bySymbol, all
	p.mu.Unlock()
}

func (p *instrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	inst, ok := p.byID[id.String()]
	return inst, ok
}

func (p *instrumentProvider) All() []*model.Instrument {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*model.Instrument, len(p.all))
	copy(out, p.all)
	return out
}

func (p *instrumentProvider) ResolveVenueSymbol(sym string) (model.InstrumentID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	id, ok := p.bySymbol[sym]
	return id, ok
}

func (p *instrumentProvider) ResolveVenueInstrument(sym string, kind enums.InstrumentKind, settle string) (model.InstrumentID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	if id, ok := p.bySymbol[venueSymbolKey(sym, kind, settle)]; ok {
		return id, true
	}
	for _, inst := range p.all {
		if inst == nil || inst.VenueSymbol != sym || inst.ID.Kind != kind {
			continue
		}
		if settle != "" && !strings.EqualFold(inst.Settle, settle) {
			continue
		}
		return inst.ID, true
	}
	return model.InstrumentID{}, false
}

func (p *instrumentProvider) resolveVenueSymbol(sym string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.bySymbol[sym]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{Venue: VenueName, Symbol: strings.ReplaceAll(sym, "_", "-"), Kind: enums.KindPerp}
}

func (p *instrumentProvider) resolveReportInstrument(scoped model.InstrumentID, venueSymbol string) model.InstrumentID {
	if scoped.Symbol != "" {
		if inst, ok := p.Instrument(scoped); ok && inst.VenueSymbol == venueSymbol {
			return scoped
		}
	}
	return p.resolveVenueSymbol(venueSymbol)
}

func instrumentFromGateSpot(in gatesdk.CurrencyPair) *model.Instrument {
	if in.ID == "" || in.Base == "" || in.Quote == "" || !isTradableSpot(in.TradeStatus) {
		return nil
	}
	tick := stepFromPrecision(in.Precision)
	step := stepFromPrecision(in.AmountPrecision)
	return &model.Instrument{
		ID:             model.InstrumentID{Venue: VenueName, Symbol: in.Base + "-" + in.Quote, Kind: enums.KindSpot},
		Base:           in.Base,
		Quote:          in.Quote,
		Settle:         in.Quote,
		VenueSymbol:    in.ID,
		PriceTick:      tick,
		SizeStep:       step,
		MinQty:         dec(in.MinBaseAmount),
		MinNotional:    dec(in.MinQuoteAmount),
		PricePrecision: decimalPlaces(tick),
		PositionMode:   model.NetOnly,
	}
}

func instrumentFromGateContract(settle string, in gatesdk.Contract) *model.Instrument {
	if !strings.EqualFold(settle, gatesdk.SettleUSDT) || in.Name == "" || in.InDelisting || !isTradableContract(in.Status) {
		return nil
	}
	base, quote := splitGateSymbol(in.Name)
	if base == "" {
		return nil
	}
	if quote == "" {
		quote = strings.ToUpper(settle)
	}
	tick := dec(in.OrderPriceRound)
	if tick.IsZero() {
		tick = dec(in.MarkPriceRound)
	}
	multiplier := dec(in.QuantoMultiplier)
	return &model.Instrument{
		ID:                 model.InstrumentID{Venue: VenueName, Symbol: base + "-" + strings.ToUpper(settle), Kind: enums.KindPerp},
		Base:               base,
		Quote:              quote,
		Settle:             strings.ToUpper(settle),
		VenueSymbol:        in.Name,
		PriceTick:          tick,
		SizeStep:           decimal.NewFromInt(1),
		MinQty:             decimal.NewFromInt(firstPositiveInt(in.OrderSizeMin, 1)),
		MinNotional:        decimal.Zero,
		PricePrecision:     decimalPlaces(tick),
		ContractMultiplier: multiplier,
		PositionMode:       model.HedgeCapable,
	}
}

func CapabilityRows() []adapter.CapabilityRow {
	return []adapter.CapabilityRow{
		capabilityRow("Spot cash", "make test-gate-spot-acceptance", "unsupported"),
		capabilityRow("USDT-linear Perp/SWAP", "make test-gate-usdt-perp-acceptance", "account snapshot"),
	}
}

func capabilityRow(product, target, positionReports string) adapter.CapabilityRow {
	return adapter.CapabilityRow{
		Venue:                VenueName,
		Product:              product,
		MarketStream:         true,
		ExecutionStream:      true,
		AccountStream:        true,
		AccountStateSnapshot: true,
		Submit:               true,
		Cancel:               true,
		Modify:               false,
		OrderStatusReports:   "open orders",
		FillReports:          "my trades",
		PositionReports:      positionReports,
		MassStatus:           "open-order mass status",
		SingleOrderQuery:     "venue order id",
		OpenOnlyCaveat:       true,
		LatencyTimestamps:    false,
		DemoTarget:           target,
	}
}

func venueSymbolKey(sym string, kind enums.InstrumentKind, settle string) string {
	return sym + "|" + kind.String() + "|" + strings.ToUpper(settle)
}

func isTradableSpot(status string) bool {
	status = strings.TrimSpace(status)
	if status == "" {
		return true
	}
	return strings.EqualFold(status, "tradable")
}

func isTradableContract(status string) bool {
	status = strings.TrimSpace(status)
	if status == "" {
		return true
	}
	return strings.EqualFold(status, "trading")
}

func stepFromPrecision(places int) decimal.Decimal {
	if places < 0 {
		return decimal.Zero
	}
	return decimal.New(1, int32(-places))
}

func firstPositiveInt(values ...int64) int64 {
	for _, value := range values {
		if value > 0 {
			return value
		}
	}
	return 0
}

func decimalPlaces(v decimal.Decimal) int {
	if v.IsZero() {
		return 0
	}
	return int(-v.Exponent())
}

func dec(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	d, err := decimal.NewFromString(s)
	if err != nil {
		return decimal.Zero
	}
	return d
}

func splitGateSymbol(symbol string) (string, string) {
	parts := strings.Split(symbol, "_")
	if len(parts) < 2 {
		return symbol, ""
	}
	return parts[0], parts[len(parts)-1]
}

func unsupportedProduct(product string) error {
	return fmt.Errorf("gate: unsupported product %q: %w", product, errs.ErrNotSupported)
}

func parseGateTimestampSeconds(value string) int64 {
	if value == "" {
		return 0
	}
	seconds, err := strconv.ParseFloat(value, 64)
	if err != nil {
		return 0
	}
	return int64(seconds * 1000)
}
