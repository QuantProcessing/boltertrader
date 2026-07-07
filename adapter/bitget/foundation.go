package bitget

import (
	"context"
	"net/http"
	"strconv"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/adapter"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bitgetsdk "github.com/QuantProcessing/boltertrader/sdk/bitget"
	"github.com/shopspring/decimal"
)

const (
	VenueName        = "BITGET"
	AccountIDUnified = model.AccountIDBitgetDefault
)

type Config struct {
	APIKey     string
	APISecret  string
	Passphrase string

	Environment bitgetsdk.EnvironmentProfile
	AccountID   string
	Categories  []string
	HTTPClient  *http.Client
	Clock       clock.Clock
}

func DefaultConfig(environment bitgetsdk.EnvironmentProfile) Config {
	return Config{
		Environment: environment,
		AccountID:   AccountIDUnified,
		Categories:  []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures},
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

func (p *instrumentProvider) Load(ctx context.Context, client *bitgetsdk.Client, categories ...string) error {
	if len(categories) == 0 {
		categories = []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}
	}
	all := make([]*model.Instrument, 0)
	for _, category := range categories {
		insts, err := client.GetInstruments(ctx, category, "")
		if err != nil {
			return err
		}
		for i := range insts {
			insts[i].Category = firstNonEmpty(insts[i].Category, category)
			inst := instrumentFromBitget(insts[i])
			if inst != nil {
				all = append(all, inst)
			}
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
			bySymbol[inst.VenueSymbol] = inst.ID
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
	for _, inst := range p.all {
		if inst == nil || inst.VenueSymbol != sym || inst.ID.Kind != kind {
			continue
		}
		if settle != "" && inst.Settle != settle {
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
	return model.InstrumentID{Venue: VenueName, Symbol: sym, Kind: enums.KindPerp}
}

func (p *instrumentProvider) resolveReportInstrument(scoped model.InstrumentID, venueSymbol string) model.InstrumentID {
	if scoped.Symbol != "" {
		if inst, ok := p.Instrument(scoped); ok && inst.VenueSymbol == venueSymbol {
			return scoped
		}
	}
	return p.resolveVenueSymbol(venueSymbol)
}

func instrumentFromBitget(in bitgetsdk.Instrument) *model.Instrument {
	if in.Symbol == "" || in.BaseCoin == "" || in.QuoteCoin == "" {
		return nil
	}
	if !isOnline(in.Status) {
		return nil
	}

	kind := enums.KindUnknown
	settle := in.QuoteCoin
	switch strings.ToUpper(strings.TrimSpace(in.Category)) {
	case "SPOT":
		kind = enums.KindSpot
	case bitgetsdk.ProductTypeUSDTFutures:
		kind = enums.KindPerp
		settle = "USDT"
	case bitgetsdk.ProductTypeUSDCFutures:
		kind = enums.KindPerp
		settle = "USDC"
	default:
		return nil
	}

	tick := firstNonZero(stepFromPrecision(in.PricePrecision), dec(in.PriceMultiplier))
	step := firstNonZero(stepFromPrecision(in.QuantityPrecision), dec(in.QuantityMultiplier))
	return &model.Instrument{
		ID:             model.InstrumentID{Venue: VenueName, Symbol: in.BaseCoin + "-" + settle, Kind: kind},
		Base:           in.BaseCoin,
		Quote:          in.QuoteCoin,
		Settle:         settle,
		VenueSymbol:    in.Symbol,
		PriceTick:      tick,
		SizeStep:       step,
		MinQty:         dec(in.MinOrderQty),
		MinNotional:    dec(in.MinOrderAmount),
		PricePrecision: decimalPlaces(tick),
		PositionMode:   positionModeForKind(kind),
	}
}

func CapabilityRows() []adapter.CapabilityRow {
	return []adapter.CapabilityRow{
		capabilityRow("Spot cash", "make test-bitget-spot-acceptance"),
		capabilityRow("USDT-linear Perp/SWAP", "make test-bitget-usdt-perp-acceptance"),
		capabilityRow("USDC-linear Perp/SWAP", "make test-bitget-usdc-perp-acceptance"),
	}
}

func capabilityRow(product, target string) adapter.CapabilityRow {
	return adapter.CapabilityRow{
		Venue:                VenueName,
		Product:              product,
		MarketStream:         true,
		ExecutionStream:      true,
		AccountStream:        true,
		AccountStateSnapshot: true,
		Submit:               true,
		Cancel:               true,
		Modify:               true,
		OrderStatusReports:   "open orders",
		FillReports:          "unsupported",
		PositionReports:      "account snapshot",
		MassStatus:           "open-order mass status",
		SingleOrderQuery:     "open order filter",
		OpenOnlyCaveat:       true,
		LatencyTimestamps:    false,
		DemoTarget:           target,
	}
}

func isOnline(status string) bool {
	status = strings.TrimSpace(status)
	if status == "" {
		return true
	}
	return strings.EqualFold(status, "online") || strings.EqualFold(status, "trading") || strings.EqualFold(status, "listed")
}

func positionModeForKind(kind enums.InstrumentKind) model.PositionModeCap {
	if kind == enums.KindPerp {
		return model.HedgeCapable
	}
	return model.NetOnly
}

func stepFromPrecision(s string) decimal.Decimal {
	if s == "" {
		return decimal.Zero
	}
	if strings.Contains(s, ".") {
		return dec(s)
	}
	places, err := strconv.Atoi(s)
	if err != nil || places < 0 {
		return decimal.Zero
	}
	return decimal.New(1, int32(-places))
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if v != "" {
			return v
		}
	}
	return ""
}

func firstNonZero(values ...decimal.Decimal) decimal.Decimal {
	for _, v := range values {
		if !v.IsZero() {
			return v
		}
	}
	return decimal.Zero
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
