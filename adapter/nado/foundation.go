package nado

import (
	"fmt"
	"net/http"
	"sort"
	"strings"
	"sync"

	"github.com/QuantProcessing/boltertrader/core/clock"
	"github.com/QuantProcessing/boltertrader/core/contract"
	"github.com/QuantProcessing/boltertrader/core/enums"
	"github.com/QuantProcessing/boltertrader/core/model"
	sdk "github.com/QuantProcessing/boltertrader/sdk/nado"
	"github.com/shopspring/decimal"
)

const (
	VenueName        = "NADO"
	AccountIDUnified = model.AccountIDNadoDefault
)

func nadoEventMeta(parts ...string) contract.EventMeta {
	return contract.EventMeta{
		Source:  contract.SourceAdapterStream,
		Flags:   contract.EventFlagFromStream,
		EventID: model.EventID(strings.Join(append([]string{"nado"}, parts...), "|")),
	}
}

type Config struct {
	PrivateKey string
	Subaccount string

	Environment sdk.Environment
	Profile     *sdk.Profile
	Client      *sdk.Client
	AccountID   string
	ProductKind enums.InstrumentKind
	HTTPClient  *http.Client
	Clock       clock.Clock
}

func DefaultConfig(environment sdk.Environment, kind enums.InstrumentKind) Config {
	if environment == "" {
		environment = sdk.EnvironmentMainnet
	}
	return Config{Environment: environment, AccountID: AccountIDUnified, ProductKind: kind}
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
	mu                    sync.RWMutex
	byID                  map[string]*model.Instrument
	bySymbol              map[string]model.InstrumentID
	byProductID           map[int64]model.InstrumentID
	productIDByInstrument map[string]int64
	isolatedOnlyByID      map[string]bool
	currencyByProductID   map[int64]string
	settlementCurrency    string
	all                   []*model.Instrument
}

type discoveredInstrument struct {
	instrument   *model.Instrument
	productID    int64
	isolatedOnly bool
}

type scopedInstrumentProvider struct {
	provider *instrumentProvider
	kind     enums.InstrumentKind
}

func (p scopedInstrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	if id.Kind != selectedKind(p.kind) {
		return nil, false
	}
	return p.provider.Instrument(id)
}

func (p scopedInstrumentProvider) All() []*model.Instrument {
	all := p.provider.All()
	out := make([]*model.Instrument, 0, len(all))
	for _, inst := range all {
		if inst != nil && inst.ID.Kind == selectedKind(p.kind) {
			out = append(out, inst)
		}
	}
	return out
}

func newInstrumentProviderFromDiscovery(products sdk.AllProductsResponse, symbols sdk.SymbolsInfo, kinds []enums.InstrumentKind) (*instrumentProvider, error) {
	if err := sdk.ValidateNadoProductDiscovery(products, symbols); err != nil {
		return nil, err
	}
	allowed := make(map[enums.InstrumentKind]bool)
	for _, kind := range kinds {
		allowed[kind] = true
	}
	if len(allowed) == 0 {
		allowed[enums.KindSpot] = true
		allowed[enums.KindPerp] = true
	}

	const settlement = "USDT0"
	currencyByProductID := map[int64]string{0: settlement}
	for _, symbol := range symbols.Symbols {
		if symbol.Type != string(sdk.MarketTypeSpot) {
			continue
		}
		base, _ := nadoSymbolCurrencies(symbol.Symbol)
		if base == "" {
			return nil, fmt.Errorf("nado: spot product %d has no currency symbol", symbol.ProductID)
		}
		currencyByProductID[int64(symbol.ProductID)] = base
	}

	insts := make([]discoveredInstrument, 0, len(products.SpotProducts)+len(products.PerpProducts))
	for _, product := range products.SpotProducts {
		if !allowed[enums.KindSpot] {
			continue
		}
		symbol, ok := symbolByProduct(symbols, product.ProductID, sdk.MarketTypeSpot)
		if !ok || product.ProductID == 0 {
			continue
		}
		if symbol.TradingStatus == sdk.TradingStatusNotTradable {
			continue
		}
		inst, err := instrumentFromNadoProduct(product.ProductID, sdk.MarketTypeSpot, product.BookInfo, symbol)
		if err != nil {
			return nil, err
		}
		insts = append(insts, discoveredInstrument{instrument: inst, productID: product.ProductID, isolatedOnly: symbol.IsolatedOnly})
	}
	for _, product := range products.PerpProducts {
		if !allowed[enums.KindPerp] {
			continue
		}
		symbol, ok := symbolByProduct(symbols, product.ProductID, sdk.MarketTypePerp)
		if !ok {
			continue
		}
		if symbol.TradingStatus == sdk.TradingStatusNotTradable {
			continue
		}
		inst, err := instrumentFromNadoProduct(product.ProductID, sdk.MarketTypePerp, product.BookInfo, symbol)
		if err != nil {
			return nil, err
		}
		insts = append(insts, discoveredInstrument{instrument: inst, productID: product.ProductID, isolatedOnly: symbol.IsolatedOnly})
	}
	if len(insts) == 0 {
		return nil, fmt.Errorf("nado: discovery returned no instruments for requested products")
	}

	provider := &instrumentProvider{}
	provider.loadDiscovery(insts, currencyByProductID, settlement)
	return provider, nil
}

func (p *instrumentProvider) loadDiscovery(insts []discoveredInstrument, currencies map[int64]string, settlement string) {
	byID := make(map[string]*model.Instrument, len(insts))
	bySymbol := make(map[string]model.InstrumentID, len(insts))
	byProductID := make(map[int64]model.InstrumentID, len(insts))
	productIDByInstrument := make(map[string]int64, len(insts))
	isolatedOnlyByID := make(map[string]bool, len(insts))
	all := make([]*model.Instrument, 0, len(insts))
	for _, discovered := range insts {
		inst := discovered.instrument
		if inst == nil {
			continue
		}
		cp := *inst
		key := cp.ID.String()
		byID[key] = &cp
		if cp.VenueSymbol != "" {
			bySymbol[cp.VenueSymbol] = cp.ID
		}
		byProductID[discovered.productID] = cp.ID
		productIDByInstrument[key] = discovered.productID
		isolatedOnlyByID[key] = discovered.isolatedOnly
		all = append(all, &cp)
	}
	sort.Slice(all, func(i, j int) bool { return all[i].ID.String() < all[j].ID.String() })
	currencyCopy := make(map[int64]string, len(currencies))
	for productID, currency := range currencies {
		currencyCopy[productID] = currency
	}
	p.mu.Lock()
	p.byID = byID
	p.bySymbol = bySymbol
	p.byProductID = byProductID
	p.productIDByInstrument = productIDByInstrument
	p.isolatedOnlyByID = isolatedOnlyByID
	p.currencyByProductID = currencyCopy
	p.settlementCurrency = settlement
	p.all = all
	p.mu.Unlock()
}

func (p *instrumentProvider) ApplyAssetDiscovery(assets []sdk.AssetV2) error {
	byProductID := make(map[int64]sdk.AssetV2, len(assets))
	for _, asset := range assets {
		if asset.ProductId < 0 || strings.TrimSpace(asset.Symbol) == "" {
			return fmt.Errorf("nado: asset discovery contains invalid product %d", asset.ProductId)
		}
		if _, exists := byProductID[asset.ProductId]; exists {
			return fmt.Errorf("nado: asset discovery contains duplicate product %d", asset.ProductId)
		}
		byProductID[asset.ProductId] = asset
	}

	p.mu.Lock()
	defer p.mu.Unlock()

	settlement, ok := byProductID[0]
	if !ok || settlement.Symbol != p.settlementCurrency {
		return fmt.Errorf("nado: asset discovery settlement product mismatch")
	}
	currencies := make(map[int64]string, len(p.currencyByProductID)+len(assets))
	for productID, currency := range p.currencyByProductID {
		currencies[productID] = currency
	}
	for productID, asset := range byProductID {
		if productID == 0 || asset.MarketType == string(sdk.MarketTypeSpot) {
			currencies[productID] = asset.Symbol
		}
	}

	byID := make(map[string]*model.Instrument, len(p.byID))
	bySymbol := make(map[string]model.InstrumentID, len(p.byID))
	all := make([]*model.Instrument, 0, len(p.all))
	for _, current := range p.all {
		productID, exists := p.productIDByInstrument[current.ID.String()]
		if !exists {
			return fmt.Errorf("nado: instrument %s has no product identity", current.ID)
		}
		asset, exists := byProductID[productID]
		if !exists {
			return fmt.Errorf("nado: product %d has no V2 asset identity", productID)
		}
		expectedMarket := sdk.MarketTypeSpot
		if current.ID.Kind == enums.KindPerp {
			expectedMarket = sdk.MarketTypePerp
		}
		if asset.MarketType != string(expectedMarket) || strings.TrimSpace(asset.TickerId) == "" {
			return fmt.Errorf("nado: product %d V2 asset market/ticker mismatch", productID)
		}
		base, quote := nadoSymbolCurrencies(asset.TickerId)
		if !strings.EqualFold(base, current.Base) || !strings.EqualFold(quote, current.Quote) {
			return fmt.Errorf("nado: product %d V2 ticker %q does not match %s/%s", productID, asset.TickerId, current.Base, current.Quote)
		}
		if _, exists := bySymbol[asset.TickerId]; exists {
			return fmt.Errorf("nado: duplicate V2 ticker %q", asset.TickerId)
		}
		clone := *current
		clone.VenueSymbol = asset.TickerId
		byID[clone.ID.String()] = &clone
		bySymbol[clone.VenueSymbol] = clone.ID
		all = append(all, &clone)
	}
	p.byID = byID
	p.bySymbol = bySymbol
	p.all = all
	p.currencyByProductID = currencies
	return nil
}

func (p *instrumentProvider) Instrument(id model.InstrumentID) (*model.Instrument, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	inst, ok := p.byID[id.String()]
	if !ok {
		return nil, false
	}
	cp := *inst
	return &cp, true
}

func (p *instrumentProvider) All() []*model.Instrument {
	p.mu.RLock()
	defer p.mu.RUnlock()
	out := make([]*model.Instrument, 0, len(p.all))
	for _, inst := range p.all {
		cp := *inst
		out = append(out, &cp)
	}
	return out
}

func (p *instrumentProvider) ResolveVenueInstrument(symbol string, kind enums.InstrumentKind, settle string) (model.InstrumentID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	for _, inst := range p.all {
		if inst.VenueSymbol != symbol || inst.ID.Kind != kind {
			continue
		}
		if settle != "" && inst.Settle != settle {
			continue
		}
		return inst.ID, true
	}
	return model.InstrumentID{}, false
}

func (p *instrumentProvider) ResolveProductID(productID int64) (model.InstrumentID, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	id, ok := p.byProductID[productID]
	return id, ok
}

func (p *instrumentProvider) ProductID(id model.InstrumentID) (int64, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	productID, ok := p.productIDByInstrument[id.String()]
	return productID, ok
}

func (p *instrumentProvider) IsolatedOnly(id model.InstrumentID) (bool, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	isolatedOnly, ok := p.isolatedOnlyByID[id.String()]
	return isolatedOnly, ok
}

func (p *instrumentProvider) CurrencyForProductID(productID int64) (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	currency, ok := p.currencyByProductID[productID]
	return currency, ok
}

func (p *instrumentProvider) SettlementCurrency() (string, bool) {
	p.mu.RLock()
	defer p.mu.RUnlock()
	return p.settlementCurrency, p.settlementCurrency != ""
}

func symbolByProduct(symbols sdk.SymbolsInfo, productID int64, market sdk.MarketType) (sdk.Symbol, bool) {
	for _, symbol := range symbols.Symbols {
		if int64(symbol.ProductID) == productID && symbol.Type == string(market) {
			return symbol, true
		}
	}
	return sdk.Symbol{}, false
}

func instrumentFromNadoProduct(productID int64, market sdk.MarketType, book sdk.ProductBookInfo, symbol sdk.Symbol) (*model.Instrument, error) {
	if !nadoTradingStatusUsable(symbol.TradingStatus) {
		return nil, fmt.Errorf("nado: product %d status %q is not tradable", productID, symbol.TradingStatus)
	}
	kind := enums.KindSpot
	if market == sdk.MarketTypePerp {
		kind = enums.KindPerp
	}
	base, quote := nadoSymbolCurrencies(symbol.Symbol)
	if base == "" || quote == "" {
		return nil, fmt.Errorf("nado: product %d has malformed symbol %q", productID, symbol.Symbol)
	}
	if quote != "USDT0" {
		return nil, fmt.Errorf("nado: product %d quote %q is outside USDT0 scope", productID, quote)
	}
	priceTick, err := parseX18Required(book.PriceIncrementX18, "price increment")
	if err != nil {
		return nil, fmt.Errorf("nado: product %d invalid price increment: %w", productID, err)
	}
	if !priceTick.IsPositive() {
		return nil, fmt.Errorf("nado: product %d price increment must be positive", productID)
	}
	sizeStep, err := parseX18Required(book.SizeIncrement, "size increment")
	if err != nil {
		return nil, fmt.Errorf("nado: product %d invalid size increment: %w", productID, err)
	}
	if !sizeStep.IsPositive() {
		return nil, fmt.Errorf("nado: product %d size increment must be positive", productID)
	}
	minNotional, err := parseX18Required(book.MinSize, "minimum notional")
	if err != nil {
		return nil, fmt.Errorf("nado: product %d invalid minimum notional: %w", productID, err)
	}
	if !minNotional.IsPositive() {
		return nil, fmt.Errorf("nado: product %d minimum notional must be positive", productID)
	}
	inst := &model.Instrument{
		ID: model.InstrumentID{
			Venue:  VenueName,
			Symbol: base + "-" + quote,
			Kind:   kind,
		},
		Base:               base,
		Quote:              quote,
		Settle:             quote,
		VenueSymbol:        nadoV2TickerID(base, quote, market),
		PriceTick:          priceTick,
		SizeStep:           sizeStep,
		MinQty:             sizeStep,
		MinNotional:        minNotional,
		PricePrecision:     decimalPlaces(priceTick),
		ContractMultiplier: decimal.NewFromInt(1),
		PositionMode:       model.NetOnly,
	}
	return inst, nil
}

func nadoV2TickerID(base, quote string, market sdk.MarketType) string {
	if market == sdk.MarketTypePerp {
		base += "-PERP"
	}
	return base + "_" + quote
}

func nadoTradingStatusUsable(status sdk.TradingStatus) bool {
	switch status {
	case sdk.TradingStatusLive, sdk.TradingStatusPostOnly, sdk.TradingStatusReduceOnly, sdk.TradingStatusSoftReduceOnly:
		return true
	default:
		return false
	}
}

func nadoSymbolCurrencies(symbol string) (string, string) {
	symbol = strings.TrimSpace(strings.ToUpper(symbol))
	symbol = strings.TrimSuffix(symbol, "-PERP")
	symbol = strings.ReplaceAll(symbol, "/", "_")
	if base, quote, ok := strings.Cut(symbol, "_"); ok {
		return strings.TrimSuffix(base, "-PERP"), quote
	}
	if strings.HasSuffix(symbol, "USDT0") && len(symbol) > len("USDT0") {
		return strings.TrimSuffix(symbol, "USDT0"), "USDT0"
	}
	if symbol != "" && symbol != "USDT0" {
		return symbol, "USDT0"
	}
	return symbol, ""
}

func productKinds(kind enums.InstrumentKind) ([]enums.InstrumentKind, error) {
	switch kind {
	case enums.KindSpot, enums.KindPerp:
		return []enums.InstrumentKind{kind}, nil
	case enums.KindUnknown:
		return []enums.InstrumentKind{enums.KindSpot, enums.KindPerp}, nil
	default:
		return nil, fmt.Errorf("nado: unsupported product kind %s: %w", kind, contract.ErrNotSupported)
	}
}
