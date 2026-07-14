package bitget

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
	mu               sync.RWMutex
	byID             map[string]*model.Instrument
	bySymbol         map[string]model.InstrumentID
	byCategorySymbol map[string]model.InstrumentID
	categoryScope    map[string]struct{}
	all              []*model.Instrument
}

func newInstrumentProvider() *instrumentProvider {
	return &instrumentProvider{
		byID:             make(map[string]*model.Instrument),
		bySymbol:         make(map[string]model.InstrumentID),
		byCategorySymbol: make(map[string]model.InstrumentID),
		categoryScope:    make(map[string]struct{}),
	}
}

func (p *instrumentProvider) Load(ctx context.Context, client *bitgetsdk.Client, categories ...string) error {
	var err error
	categories, err = normalizeBitgetCategories(categories)
	if err != nil {
		return err
	}
	all := make([]*model.Instrument, 0)
	for _, category := range categories {
		insts, err := client.GetInstruments(ctx, category, "")
		if err != nil {
			return err
		}
		for i := range insts {
			recordCategory, ok := normalizeBitgetCategory(firstNonEmpty(insts[i].Category, category))
			if !ok || recordCategory != category {
				continue
			}
			insts[i].Category = recordCategory
			inst := instrumentFromBitget(insts[i])
			if inst != nil {
				all = append(all, inst)
			}
		}
	}
	p.loadSnapshot(all, categories)
	return nil
}

func (p *instrumentProvider) LoadSnapshot(insts []*model.Instrument) {
	categories := make([]string, 0, 3)
	seen := make(map[string]struct{}, 3)
	for _, inst := range insts {
		category, err := categoryForInstrument(inst)
		if err != nil {
			continue
		}
		if _, ok := seen[category]; ok {
			continue
		}
		seen[category] = struct{}{}
		categories = append(categories, category)
	}
	p.loadSnapshot(insts, categories)
}

func (p *instrumentProvider) loadSnapshot(insts []*model.Instrument, categories []string) {
	byID := make(map[string]*model.Instrument, len(insts))
	bySymbol := make(map[string]model.InstrumentID, len(insts))
	byCategorySymbol := make(map[string]model.InstrumentID, len(insts))
	categoryScope := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		if normalized, ok := normalizeBitgetCategory(category); ok {
			categoryScope[normalized] = struct{}{}
		}
	}
	ambiguousSymbols := make(map[string]struct{})
	ambiguousCategorySymbols := make(map[string]struct{})
	all := make([]*model.Instrument, 0, len(insts))
	for _, inst := range insts {
		if inst == nil {
			continue
		}
		byID[inst.ID.String()] = inst
		venueSymbol := normalizeVenueSymbol(inst.VenueSymbol)
		if venueSymbol != "" {
			if _, ambiguous := ambiguousSymbols[venueSymbol]; !ambiguous {
				if existing, found := bySymbol[venueSymbol]; found && existing != inst.ID {
					delete(bySymbol, venueSymbol)
					ambiguousSymbols[venueSymbol] = struct{}{}
				} else {
					bySymbol[venueSymbol] = inst.ID
				}
			}
			if category, err := categoryForInstrument(inst); err == nil {
				key := categorySymbolKey(category, venueSymbol)
				if _, ambiguous := ambiguousCategorySymbols[key]; !ambiguous {
					if existing, found := byCategorySymbol[key]; found && existing != inst.ID {
						delete(byCategorySymbol, key)
						ambiguousCategorySymbols[key] = struct{}{}
					} else {
						byCategorySymbol[key] = inst.ID
					}
				}
			}
		}
		all = append(all, inst)
	}
	p.mu.Lock()
	p.byID, p.bySymbol, p.byCategorySymbol, p.categoryScope, p.all = byID, bySymbol, byCategorySymbol, categoryScope, all
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
	id, ok := p.bySymbol[normalizeVenueSymbol(sym)]
	return id, ok
}

// ResolveVenueCategorySymbol resolves a private UTA record only when both its
// normalized category and venue symbol identify an instrument loaded into the
// adapter's configured scope. Category is mandatory because UTA can publish
// spot and derivatives with the same venue symbol.
func (p *instrumentProvider) ResolveVenueCategorySymbol(category, sym string) (model.InstrumentID, bool) {
	normalizedCategory, ok := normalizeBitgetCategory(category)
	if !ok {
		return model.InstrumentID{}, false
	}
	normalizedSymbol := normalizeVenueSymbol(sym)
	if normalizedSymbol == "" {
		return model.InstrumentID{}, false
	}
	p.mu.RLock()
	id, ok := p.byCategorySymbol[categorySymbolKey(normalizedCategory, normalizedSymbol)]
	p.mu.RUnlock()
	if !ok {
		return model.InstrumentID{}, false
	}
	return id, true
}

func (p *instrumentProvider) CategoryInScope(category string) bool {
	normalizedCategory, ok := normalizeBitgetCategory(category)
	if !ok {
		return false
	}
	p.mu.RLock()
	_, ok = p.categoryScope[normalizedCategory]
	p.mu.RUnlock()
	return ok
}

func (p *instrumentProvider) ResolveVenueInstrument(sym string, kind enums.InstrumentKind, settle string) (model.InstrumentID, bool) {
	normalizedSymbol := normalizeVenueSymbol(sym)
	normalizedSettle := strings.ToUpper(strings.TrimSpace(settle))
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, inst := range p.all {
		if inst == nil || normalizeVenueSymbol(inst.VenueSymbol) != normalizedSymbol || inst.ID.Kind != kind {
			continue
		}
		if normalizedSettle != "" && strings.ToUpper(strings.TrimSpace(inst.Settle)) != normalizedSettle {
			continue
		}
		return inst.ID, true
	}
	return model.InstrumentID{}, false
}

func (p *instrumentProvider) resolveVenueSymbol(sym string) model.InstrumentID {
	p.mu.RLock()
	id, ok := p.bySymbol[normalizeVenueSymbol(sym)]
	p.mu.RUnlock()
	if ok {
		return id
	}
	return model.InstrumentID{}
}

func (p *instrumentProvider) resolveReportInstrument(scoped model.InstrumentID, venueSymbol string) model.InstrumentID {
	if scoped.Symbol != "" {
		if inst, ok := p.Instrument(scoped); ok && normalizeVenueSymbol(inst.VenueSymbol) == normalizeVenueSymbol(venueSymbol) {
			return scoped
		}
	}
	return p.resolveVenueSymbol(venueSymbol)
}

func normalizeBitgetCategory(category string) (string, bool) {
	switch normalized := strings.ToUpper(strings.TrimSpace(category)); normalized {
	case "SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures:
		return normalized, true
	default:
		return "", false
	}
}

func normalizeBitgetCategories(categories []string) ([]string, error) {
	if len(categories) == 0 {
		return []string{"SPOT", bitgetsdk.ProductTypeUSDTFutures, bitgetsdk.ProductTypeUSDCFutures}, nil
	}
	normalized := make([]string, 0, len(categories))
	seen := make(map[string]struct{}, len(categories))
	for _, category := range categories {
		value, ok := normalizeBitgetCategory(category)
		if !ok {
			return nil, fmt.Errorf("bitget: unsupported category %q", category)
		}
		if _, duplicate := seen[value]; duplicate {
			continue
		}
		seen[value] = struct{}{}
		normalized = append(normalized, value)
	}
	return normalized, nil
}

func normalizeVenueSymbol(symbol string) string {
	return strings.ToUpper(strings.TrimSpace(symbol))
}

func categorySymbolKey(category, symbol string) string {
	return category + "\x00" + symbol
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
		FillReports:          "bounded 90-day trade history",
		PositionReports:      "account snapshot",
		MassStatus:           "open orders, bounded fills, positions",
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
