package boltertrader

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	"github.com/QuantProcessing/funding-trader/backend/internal/marketdata"
	"github.com/shopspring/decimal"
)

const (
	defaultPublicCatalogTTL      = 30 * time.Second
	publicFundingReadinessMaxAge = 15 * time.Minute
	publicBBOReadinessMaxAge     = 2 * time.Minute
	publicObservationFutureSkew  = time.Minute
)

type PublicMarketProvider struct {
	catalog  PublicCatalogSource
	public   PublicMarketSource
	mode     config.Mode
	cacheTTL time.Duration
	now      func() time.Time

	mu          sync.RWMutex
	instruments []marketdata.Instrument
	byKey       map[string]marketdata.Instrument
	expiresAt   time.Time
}

func NewPublicMarketProvider(
	catalog PublicCatalogSource,
	public PublicMarketSource,
	mode config.Mode,
) (*PublicMarketProvider, error) {
	if catalog == nil {
		return nil, fmt.Errorf("public market catalog source is required")
	}
	if public == nil {
		return nil, fmt.Errorf("public market data source is required")
	}
	switch mode {
	case config.Test, config.Live:
	default:
		return nil, fmt.Errorf("public market mode is invalid")
	}
	return &PublicMarketProvider{
		catalog: catalog, public: public, mode: mode,
		cacheTTL: defaultPublicCatalogTTL, now: time.Now,
		byKey: make(map[string]marketdata.Instrument),
	}, nil
}

func (provider *PublicMarketProvider) Instruments(ctx context.Context) ([]marketdata.Instrument, error) {
	return provider.load(ctx, false)
}

func (provider *PublicMarketProvider) FundingRate(ctx context.Context, requested marketdata.Instrument) (marketdata.FundingRate, error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return marketdata.FundingRate{}, err
	}
	return provider.public.CurrentFunding(ctx, instrument)
}

func (provider *PublicMarketProvider) FundingRateHistory(
	ctx context.Context,
	request marketdata.FundingRateHistoryRequest,
) (marketdata.FundingRatePage, error) {
	instrument, err := provider.binding(ctx, request.Instrument)
	if err != nil {
		return marketdata.FundingRatePage{}, err
	}
	request.Instrument = instrument
	return provider.public.FundingHistory(ctx, request)
}

func (provider *PublicMarketProvider) OrderBook(
	ctx context.Context,
	request marketdata.OrderBookRequest,
) (marketdata.OrderBook, error) {
	instrument, err := provider.binding(ctx, request.Instrument)
	if err != nil {
		return marketdata.OrderBook{}, err
	}
	request.Instrument = instrument
	return provider.public.OrderBook(ctx, request)
}

func (provider *PublicMarketProvider) BBO(ctx context.Context, requested marketdata.Instrument) (marketdata.BBO, error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return marketdata.BBO{}, err
	}
	return provider.public.BBO(ctx, instrument)
}

func (provider *PublicMarketProvider) MarkPrice(ctx context.Context, requested marketdata.Instrument) (marketdata.MarkPrice, error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return marketdata.MarkPrice{}, err
	}
	return provider.public.MarkPrice(ctx, instrument)
}

func (provider *PublicMarketProvider) WatchOrderBook(
	ctx context.Context,
	requested marketdata.Instrument,
) (marketdata.Subscription[marketdata.OrderBook], error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return nil, err
	}
	return provider.public.WatchOrderBook(ctx, instrument)
}

func (provider *PublicMarketProvider) WatchBBO(
	ctx context.Context,
	requested marketdata.Instrument,
) (marketdata.Subscription[marketdata.BBO], error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return nil, err
	}
	return provider.public.WatchBBO(ctx, instrument)
}

func (provider *PublicMarketProvider) WatchMarkPrice(
	ctx context.Context,
	requested marketdata.Instrument,
) (marketdata.Subscription[marketdata.MarkPrice], error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return nil, err
	}
	return provider.public.WatchMarkPrice(ctx, instrument)
}

func (provider *PublicMarketProvider) WatchFundingRate(
	ctx context.Context,
	requested marketdata.Instrument,
) (marketdata.Subscription[marketdata.FundingRate], error) {
	instrument, err := provider.binding(ctx, requested)
	if err != nil {
		return nil, err
	}
	return provider.public.WatchFundingRate(ctx, instrument)
}

func (provider *PublicMarketProvider) Probe(ctx context.Context) (marketdata.Readiness, error) {
	instruments, err := provider.load(ctx, true)
	if err != nil {
		return marketdata.Readiness{}, err
	}
	if len(instruments) == 0 {
		return marketdata.Readiness{}, marketdata.ErrNoConfiguredInstruments
	}
	instrument := instruments[0]
	funding, err := provider.public.CurrentFunding(ctx, instrument)
	if err != nil {
		return marketdata.Readiness{}, fmt.Errorf("public funding probe failed: %w", err)
	}
	bbo, err := provider.public.BBO(ctx, instrument)
	if err != nil {
		return marketdata.Readiness{}, fmt.Errorf("public bbo probe failed: %w", err)
	}
	now := provider.now().UTC()
	if !freshPublicObservation(funding.ObservedAt, now, publicFundingReadinessMaxAge) {
		return marketdata.Readiness{}, fmt.Errorf("%w: stale public funding", marketdata.ErrInvalidMarketData)
	}
	if !freshPublicObservation(bbo.ObservedAt, now, publicBBOReadinessMaxAge) {
		return marketdata.Readiness{}, fmt.Errorf("%w: stale public bbo", marketdata.ErrInvalidMarketData)
	}
	accounts := make(map[string]struct{})
	for _, current := range instruments {
		accounts[current.AccountID] = struct{}{}
	}
	return marketdata.Readiness{
		Mode: string(provider.mode), Accounts: len(accounts), Instruments: len(instruments),
	}, nil
}

func (provider *PublicMarketProvider) Close() error { return nil }

func (provider *PublicMarketProvider) binding(
	ctx context.Context,
	requested marketdata.Instrument,
) (marketdata.Instrument, error) {
	if err := domain.AccountID(requested.AccountID).Validate(); err != nil {
		return marketdata.Instrument{}, fmt.Errorf("%w: account id", marketdata.ErrInstrumentNotFound)
	}
	if err := domain.InstrumentID(requested.InstrumentID).Validate(); err != nil {
		return marketdata.Instrument{}, fmt.Errorf("%w: instrument id", marketdata.ErrInstrumentNotFound)
	}
	key := requested.Key()
	provider.mu.RLock()
	instrument, found := provider.byKey[key]
	expired := !provider.expiresAt.After(provider.now())
	provider.mu.RUnlock()
	if found && !expired {
		return instrument, nil
	}
	if _, err := provider.load(ctx, true); err != nil {
		return marketdata.Instrument{}, err
	}
	provider.mu.RLock()
	instrument, found = provider.byKey[key]
	provider.mu.RUnlock()
	if !found {
		return marketdata.Instrument{}, marketdata.ErrInstrumentNotFound
	}
	return instrument, nil
}

func (provider *PublicMarketProvider) load(ctx context.Context, force bool) ([]marketdata.Instrument, error) {
	provider.mu.RLock()
	fresh := provider.expiresAt.After(provider.now())
	if !force && fresh {
		result := cloneInstruments(provider.instruments)
		provider.mu.RUnlock()
		return result, nil
	}
	provider.mu.RUnlock()

	discovery, err := provider.catalog.DiscoverAccounts(ctx)
	if err != nil {
		return nil, fmt.Errorf("discover public market accounts: %w", err)
	}
	if discovery.Mode != provider.mode {
		return nil, fmt.Errorf("%w: discovery mode", marketdata.ErrInvalidMarketData)
	}
	result := make([]marketdata.Instrument, 0)
	seen := make(map[string]struct{})
	for _, account := range discovery.Accounts {
		if !eligiblePublicFundingAccount(account, provider.mode) {
			continue
		}
		resources, listErr := provider.catalog.ListInstruments(ctx, account.AccountID)
		if listErr != nil {
			return nil, fmt.Errorf("list public market instruments: %w", listErr)
		}
		for _, resource := range resources {
			instrument, convertErr := publicInstrument(account, resource)
			if convertErr != nil {
				return nil, convertErr
			}
			key := instrument.Key()
			if _, duplicate := seen[key]; duplicate {
				return nil, fmt.Errorf("%w: duplicate instrument binding", marketdata.ErrInvalidMarketData)
			}
			seen[key] = struct{}{}
			result = append(result, instrument)
		}
	}
	sort.Slice(result, func(left, right int) bool {
		if result[left].Venue != result[right].Venue {
			return result[left].Venue < result[right].Venue
		}
		if result[left].AccountID != result[right].AccountID {
			return result[left].AccountID < result[right].AccountID
		}
		if result[left].Symbol != result[right].Symbol {
			return result[left].Symbol < result[right].Symbol
		}
		return result[left].InstrumentID < result[right].InstrumentID
	})
	byKey := make(map[string]marketdata.Instrument, len(result))
	for _, instrument := range result {
		byKey[instrument.Key()] = instrument
	}
	provider.mu.Lock()
	provider.instruments = cloneInstruments(result)
	provider.byKey = byKey
	provider.expiresAt = provider.now().Add(provider.cacheTTL)
	provider.mu.Unlock()
	return cloneInstruments(result), nil
}

func eligiblePublicFundingAccount(account DiscoveredAccount, mode config.Mode) bool {
	return eligibleVenueEnvironment(mode, account.VenueEnvironment) &&
		account.ReadReady &&
		account.Product == "perp" &&
		account.Capability.Operations.Funding
}

func publicInstrument(account DiscoveredAccount, resource InstrumentResource) (marketdata.Instrument, error) {
	if err := domain.AccountID(account.AccountID).Validate(); err != nil {
		return marketdata.Instrument{}, fmt.Errorf("%w: account id", marketdata.ErrInvalidMarketData)
	}
	if err := domain.InstrumentID(resource.InstrumentID).Validate(); err != nil {
		return marketdata.Instrument{}, fmt.Errorf("%w: instrument id", marketdata.ErrInvalidMarketData)
	}
	if resource.VenueEnvironment != account.VenueEnvironment ||
		resource.Venue != account.Venue ||
		resource.Product != account.Product ||
		resource.Settlement != account.Settlement {
		return marketdata.Instrument{}, fmt.Errorf("%w: instrument scope mismatch", marketdata.ErrInvalidMarketData)
	}
	if resource.Product != "perp" ||
		strings.TrimSpace(resource.Symbol) == "" ||
		strings.TrimSpace(resource.BaseAsset) == "" ||
		strings.TrimSpace(resource.QuoteAsset) == "" ||
		strings.TrimSpace(resource.Settlement) == "" {
		return marketdata.Instrument{}, fmt.Errorf("%w: incomplete instrument", marketdata.ErrInvalidMarketData)
	}
	rules := marketdata.Rules{
		PriceIncrement:    resource.Rules.PriceIncrement,
		QuantityIncrement: resource.Rules.QuantityIncrement,
		MinQuantity:       resource.Rules.MinQuantity,
		MinNotional: marketdata.OptionalDecimal{
			Value: resource.Rules.MinNotional.Value,
			Valid: resource.Rules.MinNotional.Valid,
		},
	}
	if err := validRules(rules); err != nil {
		return marketdata.Instrument{}, err
	}
	return marketdata.Instrument{
		AccountID: account.AccountID, InstrumentID: resource.InstrumentID,
		Venue: resource.Venue, VenueEnvironment: string(resource.VenueEnvironment),
		Product: resource.Product, Settlement: resource.Settlement,
		Symbol: resource.Symbol, BaseAsset: resource.BaseAsset, QuoteAsset: resource.QuoteAsset,
		Rules: rules,
	}, nil
}

func validRules(rules marketdata.Rules) error {
	if !rules.PriceIncrement.IsPositive() ||
		!rules.QuantityIncrement.IsPositive() ||
		rules.MinQuantity.IsNegative() ||
		rules.MinNotional.Valid && rules.MinNotional.Value.IsNegative() {
		return fmt.Errorf("%w: invalid instrument rules", marketdata.ErrInvalidMarketData)
	}
	if rules.MinNotional.Valid && rules.MinNotional.Value.Equal(decimal.Zero) {
		return fmt.Errorf("%w: invalid minimum notional", marketdata.ErrInvalidMarketData)
	}
	return nil
}

func freshPublicObservation(observedAt, now time.Time, maxAge time.Duration) bool {
	if observedAt.IsZero() || now.IsZero() || maxAge <= 0 {
		return false
	}
	observedAt = observedAt.UTC()
	return !observedAt.Before(now.Add(-maxAge)) &&
		!observedAt.After(now.Add(publicObservationFutureSkew))
}

func cloneInstruments(source []marketdata.Instrument) []marketdata.Instrument {
	if source == nil {
		return nil
	}
	result := make([]marketdata.Instrument, len(source))
	copy(result, source)
	return result
}

func IsPublicMarketNotFound(err error) bool {
	return errors.Is(err, marketdata.ErrInstrumentNotFound)
}
