package gateway

import (
	"context"
	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	"net/http"
)

// CatalogVenues describes Gateway's configured public venue sources.
func (c *Client) CatalogVenues(ctx context.Context) ([]wire.CatalogVenue, error) {
	var result wire.Envelope[[]wire.CatalogVenue]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/catalog/venues", nil, nil, "", &result, 200); err != nil {
		return nil, err
	}
	if result.Data == nil {
		return nil, protocol("catalog venues")
	}
	seen := map[string]bool{}
	for _, venue := range result.Data {
		if !validID(venue.Venue) || venue.Environment != "live" || seen[venue.Venue] {
			return nil, protocol("catalog venue")
		}
		seen[venue.Venue] = true
	}
	return result.Data, nil
}

// CatalogInstruments includes the exchange precision rules needed for orders.
func (c *Client) CatalogInstruments(ctx context.Context) (wire.CatalogInstruments, error) {
	var result wire.Envelope[wire.CatalogInstruments]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/catalog/instruments", nil, nil, "", &result, 200); err != nil {
		return wire.CatalogInstruments{}, err
	}
	s := result.Data
	if s.Revision == "" || s.ObservedAt.IsZero() || s.Instruments == nil {
		return wire.CatalogInstruments{}, protocol("catalog instruments")
	}
	seen := map[string]bool{}
	for _, item := range s.Instruments {
		if !validID(item.InstrumentID) || !validID(item.Venue) || item.Environment != "live" || !product(item.Product) || !validID(item.BaseAsset) || !validID(item.QuoteAsset) || item.Symbol == "" || seen[item.InstrumentID] || !positive(item.Rules.PriceIncrement) || !positive(item.Rules.QuantityIncrement) || !nonnegative(item.Rules.MinQuantity) || (item.Rules.MinNotional != "" && !nonnegative(item.Rules.MinNotional)) {
			return wire.CatalogInstruments{}, protocol("catalog instrument")
		}
		seen[item.InstrumentID] = true
	}
	return s, nil
}

func (c *Client) RuntimeReady(ctx context.Context) (bool, error) {
	var result wire.Envelope[struct {
		Ready *bool `json:"ready"`
	}]
	if err := c.request(ctx, http.MethodGet, "/internal/v1/runtime/readiness", nil, nil, "", &result, 200); err != nil {
		return false, err
	}
	if result.Data.Ready == nil {
		return false, protocol("runtime readiness")
	}
	return *result.Data.Ready, nil
}
