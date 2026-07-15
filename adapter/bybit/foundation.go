package bybit

import (
	"context"
	"fmt"
	"net/http"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/adapter"
	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	bybitsdk "github.com/QuantProcessing/boltertrader/sdk/bybit"
	"github.com/shopspring/decimal"
)

const (
	VenueName        = "BYBIT"
	AccountIDUnified = "BYBIT-001"
)

type Config struct {
	APIKey    string
	APISecret string

	Environment bybitsdk.EnvironmentProfile
	AccountID   string
	Categories  []string
	HTTPClient  *http.Client
	Clock       clock.Clock
}

func DefaultConfig(environment bybitsdk.EnvironmentProfile) Config {
	return Config{
		Environment: environment,
		AccountID:   AccountIDUnified,
		Categories:  []string{"spot", "linear"},
	}
}

func normalizeBybitCategories(categories []string) ([]string, error) {
	if len(categories) == 0 {
		return []string{"spot", "linear"}, nil
	}
	seen := make(map[string]struct{}, len(categories))
	normalized := make([]string, 0, len(categories))
	for _, raw := range categories {
		category := strings.ToLower(strings.TrimSpace(raw))
		if category != "spot" && category != "linear" {
			return nil, fmt.Errorf("bybit: unsupported category %q", raw)
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		normalized = append(normalized, category)
	}
	return normalized, nil
}

func AccountIDForKind(kind enums.InstrumentKind) string {
	switch kind {
	case enums.KindSpot, enums.KindPerp:
		return AccountIDUnified
	default:
		return ""
	}
}

func instrumentKindsForCategories(categories []string) []enums.InstrumentKind {
	kinds := make([]enums.InstrumentKind, 0, len(categories))
	for _, category := range categories {
		switch strings.ToLower(strings.TrimSpace(category)) {
		case "spot":
			kinds = append(kinds, enums.KindSpot)
		case "linear":
			kinds = append(kinds, enums.KindPerp)
		}
	}
	return normalizedBybitScope(kinds)
}

type instrumentProvider struct {
	mu       sync.RWMutex
	byID     map[string]*model.Instrument
	bySymbol map[string]model.InstrumentID
	deferred map[deferredInstrumentKey]struct{}
	all      []*model.Instrument
}

type deferredInstrumentKey struct {
	category string
	symbol   string
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:     make(map[string]*model.Instrument),
		bySymbol: make(map[string]model.InstrumentID),
		deferred: make(map[deferredInstrumentKey]struct{}),
	}
}

func (p *instrumentProvider) Load(ctx context.Context, client *bybitsdk.Client, categories ...string) error {
	if len(categories) == 0 {
		categories = []string{"spot", "linear"}
	}
	all := make([]*model.Instrument, 0)
	deferred := make(map[deferredInstrumentKey]struct{})
	for _, category := range categories {
		category = strings.ToLower(strings.TrimSpace(category))
		insts, err := client.GetInstruments(ctx, category)
		if err != nil {
			return err
		}
		for i := range insts {
			if category == "linear" && isBybitDatedLinearInstrument(insts[i]) {
				deferred[deferredInstrumentKey{category: category, symbol: insts[i].Symbol}] = struct{}{}
			}
			inst := instrumentFromBybit(category, insts[i])
			if inst != nil {
				all = append(all, inst)
			}
		}
	}
	p.loadSnapshot(all, deferred)
	return nil
}

func (p *instrumentProvider) LoadSnapshot(insts []*model.Instrument) {
	p.loadSnapshot(insts, nil)
}

func (p *instrumentProvider) loadSnapshot(insts []*model.Instrument, deferred map[deferredInstrumentKey]struct{}) {
	byID := make(map[string]*model.Instrument, len(insts))
	bySymbol := make(map[string]model.InstrumentID, len(insts))
	if deferred == nil {
		deferred = make(map[deferredInstrumentKey]struct{})
	}
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
	p.byID, p.bySymbol, p.deferred, p.all = byID, bySymbol, deferred, all
	p.mu.Unlock()
}

func (p *instrumentProvider) markDeferred(category, symbol string) {
	p.mu.Lock()
	defer p.mu.Unlock()
	if p.deferred == nil {
		p.deferred = make(map[deferredInstrumentKey]struct{})
	}
	p.deferred[deferredInstrumentKey{
		category: strings.ToLower(strings.TrimSpace(category)),
		symbol:   strings.TrimSpace(symbol),
	}] = struct{}{}
}

func (p *instrumentProvider) isDeferred(category, symbol string) bool {
	p.mu.RLock()
	defer p.mu.RUnlock()
	_, ok := p.deferred[deferredInstrumentKey{
		category: strings.ToLower(strings.TrimSpace(category)),
		symbol:   strings.TrimSpace(symbol),
	}]
	return ok
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

func instrumentFromBybit(category string, in bybitsdk.Instrument) *model.Instrument {
	category = strings.ToLower(strings.TrimSpace(category))
	if in.Symbol == "" || in.BaseCoin == "" || in.QuoteCoin == "" {
		return nil
	}
	if in.Status != "" && !strings.EqualFold(in.Status, "Trading") {
		return nil
	}

	kind := enums.KindUnknown
	settle := in.SettleCoin
	switch category {
	case "spot":
		kind = enums.KindSpot
		settle = in.QuoteCoin
	case "linear":
		if isBybitDatedLinearInstrument(in) {
			return nil
		}
		kind = enums.KindPerp
		if settle == "" {
			settle = in.QuoteCoin
		}
		if settle != bybitsdk.SettleCoinUSDT && settle != bybitsdk.SettleCoinUSDC {
			return nil
		}
	default:
		return nil
	}

	tick := dec(in.PriceFilter.TickSize)
	step := firstNonZero(dec(in.LotSizeFilter.QtyStep), dec(in.LotSizeFilter.BasePrecision))
	return &model.Instrument{
		ID:             model.InstrumentID{Venue: VenueName, Symbol: in.BaseCoin + "-" + settle, Kind: kind},
		Base:           in.BaseCoin,
		Quote:          in.QuoteCoin,
		Settle:         settle,
		VenueSymbol:    in.Symbol,
		PriceTick:      tick,
		SizeStep:       step,
		MinQty:         dec(in.LotSizeFilter.MinOrderQty),
		MinNotional:    firstNonZero(dec(in.LotSizeFilter.MinNotionalValue), dec(in.LotSizeFilter.MinOrderAmt)),
		PricePrecision: decimalPlaces(tick),
		PositionMode:   positionModeForKind(kind),
	}
}

func isBybitDatedLinearInstrument(in bybitsdk.Instrument) bool {
	deliveryTime := strings.TrimSpace(in.DeliveryTime)
	return (deliveryTime != "" && deliveryTime != "0") || isBybitDatedLinearSymbol(in.Symbol)
}

func isBybitDatedLinearSymbol(symbol string) bool {
	symbol = strings.TrimSpace(symbol)
	separator := strings.LastIndexByte(symbol, '-')
	if separator <= 0 || separator+8 != len(symbol) {
		return false
	}
	expiry := strings.ToUpper(symbol[separator+1:])
	day := expiry[:2]
	year := expiry[5:]
	if day < "01" || day > "31" || year[0] < '0' || year[0] > '9' || year[1] < '0' || year[1] > '9' {
		return false
	}
	switch expiry[2:5] {
	case "JAN", "FEB", "MAR", "APR", "MAY", "JUN", "JUL", "AUG", "SEP", "OCT", "NOV", "DEC":
		return true
	default:
		return false
	}
}

func CapabilityRows() []adapter.CapabilityRow {
	return []adapter.CapabilityRow{
		capabilityRow("Spot cash", "make test-bybit-spot-acceptance", false),
		capabilityRow("USDT-linear Perp/SWAP", "make test-bybit-usdt-perp-acceptance", true),
		capabilityRow("USDC-linear Perp/SWAP", "make test-bybit-usdc-perp-acceptance", true),
	}
}

func capabilityRow(product, target string, positionReports bool) adapter.CapabilityRow {
	positionReportsLabel := "unsupported"
	massStatusLabel := "open orders, bounded fills"
	if positionReports {
		positionReportsLabel = "account snapshot"
		massStatusLabel += ", positions"
	}
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
		FillReports:          "bounded execution history",
		PositionReports:      positionReportsLabel,
		MassStatus:           massStatusLabel,
		SingleOrderQuery:     "open order filter",
		OpenOnlyCaveat:       true,
		LatencyTimestamps:    false,
		DemoTarget:           target,
	}
}

func positionModeForKind(kind enums.InstrumentKind) model.PositionModeCap {
	if kind == enums.KindPerp {
		return model.HedgeCapable
	}
	return model.NetOnly
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
