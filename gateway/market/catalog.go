// Package market serves public market data: the instrument catalog, REST
// snapshots and shared WebSocket streams. It never touches credentials.
package market

import (
	"context"
	"fmt"
	"log/slog"
	"net/http"
	"sort"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/exchange/factory"
	"github.com/QuantProcessing/boltertrader/gateway/venue"
)

// ErrUnknownInstrument is returned for ids that are not in the catalog.
var ErrUnknownInstrument = fmt.Errorf("unknown instrument")

// Instrument is the public projection of one tradable instrument.
type Instrument struct {
	ID           venue.InstrumentID `json:"id"`
	Venue        exchange.Venue     `json:"venue"`
	Product      exchange.Product   `json:"product"`
	Symbol       string             `json:"symbol"`
	Base         string             `json:"base"`
	Quote        string             `json:"quote"`
	Settle       string             `json:"settle,omitempty"`
	TickSize     string             `json:"tickSize"`
	StepSize     string             `json:"stepSize"`
	MinQty       string             `json:"minQty"`
	MinNotional  string             `json:"minNotional,omitempty"`
	MarketFamily string             `json:"marketFamily,omitempty"`

	native exchange.Instrument
}

// Native returns the exchange-layer instrument used for venue calls.
func (instrument Instrument) Native() exchange.Instrument { return instrument.native }

type connector struct {
	market venue.Market
	client *factory.PublicMarketConnector
}

// Catalog owns one credential-free connector per served market and the merged
// instrument directory. Instrument metadata is refreshed periodically; the
// last good snapshot of a market survives a failed refresh.
type Catalog struct {
	connectors map[venue.Market]*connector

	mu          sync.RWMutex
	instruments map[venue.InstrumentID]Instrument
	byMarket    map[venue.Market][]venue.InstrumentID
	loadErrors  map[venue.Market]string
}

func NewCatalog(httpClient *http.Client) (*Catalog, error) {
	catalog := &Catalog{
		connectors:  make(map[venue.Market]*connector, len(venue.Markets)),
		instruments: make(map[venue.InstrumentID]Instrument),
		byMarket:    make(map[venue.Market][]venue.InstrumentID),
		loadErrors:  make(map[venue.Market]string),
	}
	for _, market := range venue.Markets {
		client, err := factory.NewPublicMarketConnector(market.Venue, market.Product, market.Settlement, factory.EnvironmentLive, httpClient)
		if err != nil {
			return nil, fmt.Errorf("public connector %s: %w", market, err)
		}
		catalog.connectors[market] = &connector{market: market, client: client}
	}
	return catalog, nil
}

// Refresh reloads every market concurrently and returns once all attempts
// finished. Failures are recorded per market and logged, never fatal.
func (catalog *Catalog) Refresh(ctx context.Context) {
	markets := make([]venue.Market, 0, len(catalog.connectors))
	for market := range catalog.connectors {
		markets = append(markets, market)
	}
	catalog.refreshMarkets(ctx, markets)
}

func (catalog *Catalog) refreshMarkets(ctx context.Context, markets []venue.Market) {
	var wg sync.WaitGroup
	for _, market := range markets {
		item := catalog.connectors[market]
		if item == nil {
			continue
		}
		wg.Add(1)
		go func(item *connector) {
			defer wg.Done()
			loadCtx, cancel := context.WithTimeout(ctx, 45*time.Second)
			defer cancel()
			natives, err := item.client.Refresh(loadCtx)
			if err != nil {
				slog.Warn("instrument catalog refresh failed", "market", item.market.String(), "error", err)
				catalog.mu.Lock()
				catalog.loadErrors[item.market] = err.Error()
				catalog.mu.Unlock()
				return
			}
			catalog.mu.RLock()
			recovered := catalog.loadErrors[item.market] != ""
			catalog.mu.RUnlock()
			catalog.publish(item.market, natives)
			if recovered {
				slog.Info("market recovered", "market", item.market.String(), "instruments", len(natives))
			}
		}(item)
	}
	wg.Wait()
}

// Run refreshes the catalog every interval until ctx ends. While any market
// has never loaded (e.g. the venue was unreachable at startup), it retries
// that market's load every retry period instead of waiting a full interval.
func (catalog *Catalog) Run(ctx context.Context, interval, retry time.Duration) {
	next := time.Now().Add(interval)
	for {
		wait := time.Until(next)
		if missing := catalog.unloadedMarkets(); len(missing) > 0 && retry < wait {
			wait = retry
		}
		select {
		case <-ctx.Done():
			return
		case <-time.After(wait):
		}
		if time.Now().Before(next) {
			catalog.refreshMarkets(ctx, catalog.unloadedMarkets())
			continue
		}
		catalog.Refresh(ctx)
		next = time.Now().Add(interval)
	}
}

// unloadedMarkets lists markets with no instruments loaded yet.
func (catalog *Catalog) unloadedMarkets() []venue.Market {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	var missing []venue.Market
	for _, market := range venue.Markets {
		if len(catalog.byMarket[market]) == 0 {
			missing = append(missing, market)
		}
	}
	return missing
}

func (catalog *Catalog) publish(market venue.Market, natives []exchange.Instrument) {
	ids := make([]venue.InstrumentID, 0, len(natives))
	projected := make(map[venue.InstrumentID]Instrument, len(natives))
	for _, native := range natives {
		owner, ok := venue.MarketOf(native)
		if !ok || owner != market {
			continue
		}
		instrument := project(native)
		if _, duplicate := projected[instrument.ID]; duplicate {
			slog.Warn("duplicate instrument id dropped", "id", instrument.ID)
			continue
		}
		projected[instrument.ID] = instrument
		ids = append(ids, instrument.ID)
	}
	sort.Slice(ids, func(i, j int) bool { return ids[i] < ids[j] })
	catalog.mu.Lock()
	defer catalog.mu.Unlock()
	for _, old := range catalog.byMarket[market] {
		delete(catalog.instruments, old)
	}
	for id, instrument := range projected {
		catalog.instruments[id] = instrument
	}
	catalog.byMarket[market] = ids
	delete(catalog.loadErrors, market)
}

func project(native exchange.Instrument) Instrument {
	identity := native.Identity
	instrument := Instrument{
		ID:           venue.NewInstrumentID(identity.Venue, identity.Product, identity.Symbol),
		Venue:        identity.Venue,
		Product:      identity.Product,
		Symbol:       identity.Symbol,
		Base:         identity.BaseAsset,
		Quote:        identity.QuoteAsset,
		Settle:       identity.SettleAsset,
		TickSize:     native.Rules.PriceIncrement.String(),
		StepSize:     native.Rules.QuantityIncrement.String(),
		MinQty:       native.Rules.MinQuantity.String(),
		MarketFamily: string(native.Metadata.MarketFamily),
		native:       native,
	}
	if native.Rules.MinNotional.Valid {
		instrument.MinNotional = native.Rules.MinNotional.Value.String()
	}
	return instrument
}

// Get resolves one instrument id.
func (catalog *Catalog) Get(id venue.InstrumentID) (Instrument, error) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	instrument, ok := catalog.instruments[id]
	if !ok {
		return Instrument{}, fmt.Errorf("%w: %s", ErrUnknownInstrument, id)
	}
	return instrument, nil
}

// List returns instruments filtered by venue and product (either may be empty).
func (catalog *Catalog) List(filterVenue exchange.Venue, filterProduct exchange.Product) []Instrument {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	var result []Instrument
	for _, market := range venue.Markets {
		if (filterVenue != "" && market.Venue != filterVenue) || (filterProduct != "" && market.Product != filterProduct) {
			continue
		}
		for _, id := range catalog.byMarket[market] {
			result = append(result, catalog.instruments[id])
		}
	}
	return result
}

// MarketInstruments returns the native instruments of one market, used to
// build private-stream instrument indexes.
func (catalog *Catalog) MarketInstruments(market venue.Market) []Instrument {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	result := make([]Instrument, 0, len(catalog.byMarket[market]))
	for _, id := range catalog.byMarket[market] {
		result = append(result, catalog.instruments[id])
	}
	return result
}

// ResolveNative maps a venue-native symbol or id back to the gateway id.
func (catalog *Catalog) ResolveNative(market venue.Market, nativeSymbol, nativeID string) (Instrument, bool) {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	for _, id := range catalog.byMarket[market] {
		instrument := catalog.instruments[id]
		identity := instrument.native.Identity
		if (nativeID != "" && identity.NativeID == nativeID) || (nativeSymbol != "" && identity.NativeSymbol == nativeSymbol) {
			return instrument, true
		}
	}
	return Instrument{}, false
}

// MarketStatus summarizes catalog health per market.
type MarketStatus struct {
	Market      string `json:"market"`
	Instruments int    `json:"instruments"`
	Error       string `json:"error,omitempty"`
}

func (catalog *Catalog) Status() []MarketStatus {
	catalog.mu.RLock()
	defer catalog.mu.RUnlock()
	result := make([]MarketStatus, 0, len(venue.Markets))
	for _, market := range venue.Markets {
		result = append(result, MarketStatus{Market: market.String(), Instruments: len(catalog.byMarket[market]), Error: catalog.loadErrors[market]})
	}
	return result
}

func (catalog *Catalog) connector(id venue.InstrumentID) (Instrument, *factory.PublicMarketConnector, error) {
	instrument, err := catalog.Get(id)
	if err != nil {
		return Instrument{}, nil, err
	}
	market, _ := venue.MarketOf(instrument.native)
	return instrument, catalog.connectors[market].client, nil
}

func (catalog *Catalog) Close() {
	for _, item := range catalog.connectors {
		_ = item.client.Close()
	}
}

// NewTestInstrument projects a native instrument without a catalog. It exists
// for tests in other packages.
func NewTestInstrument(native exchange.Instrument) Instrument { return project(native) }

// NewStaticCatalog builds a catalog from fixed instruments without any venue
// connector. It exists for tests in other packages.
func NewStaticCatalog(natives []exchange.Instrument) *Catalog {
	catalog := &Catalog{
		connectors:  map[venue.Market]*connector{},
		instruments: map[venue.InstrumentID]Instrument{},
		byMarket:    map[venue.Market][]venue.InstrumentID{},
		loadErrors:  map[venue.Market]string{},
	}
	grouped := map[venue.Market][]exchange.Instrument{}
	for _, native := range natives {
		if market, ok := venue.MarketOf(native); ok {
			grouped[market] = append(grouped[market], native)
		}
	}
	for market, items := range grouped {
		catalog.publish(market, items)
	}
	return catalog
}
