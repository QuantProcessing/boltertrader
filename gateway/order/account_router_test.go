package order

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/gateway/account"
	"github.com/QuantProcessing/boltertrader/gateway/domain"
	"github.com/shopspring/decimal"
)

func TestAccountRouterUsesCatalogIdentityAndRequiresPromotedExactLookup(t *testing.T) {
	instrument := exchange.Instrument{
		Identity: exchange.InstrumentIdentity{
			Venue: exchange.VenueBinance, Product: exchange.ProductSpot,
			Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT",
			NativeSymbol: "BTCUSDT", NativeID: "BTCUSDT",
		},
		Rules: exchange.InstrumentRules{
			PriceIncrement:    decimal.RequireFromString("0.01"),
			QuantityIncrement: decimal.RequireFromString("0.001"),
			MinQuantity:       decimal.RequireFromString("0.001"),
		},
	}
	client := &routingSpotClient{lookupResult: exchange.ExactOrderLookupResult{
		Status: exchange.ExactOrderFound,
		Order: exchange.Order{
			NativeSymbol: "BTCUSDT", OrderID: "venue-1", ClientOrderID: "1", Status: "open",
		},
		Evidence: exchange.ExactOrderEvidence{
			Scope: exchange.ExactOrderScope{
				Venue: exchange.VenueBinance, Product: exchange.ProductSpot, Instrument: &instrument,
			},
			Method:        exchange.ExactOrderEvidenceByClientOrderID,
			Authoritative: true, Complete: true, ObservedClientOrderID: "1",
		},
	}}
	registry := &routingRegistry{
		instrument: instrument,
		status: account.Status{Resource: account.Resource{
			AccountID: "binance-main", Product: exchange.ProductSpot, ReadReady: true, WriteReady: true,
		}},
		recoveryStatus: account.Status{Resource: account.Resource{
			AccountID: "binance-main", Product: exchange.ProductSpot, ReadReady: true, WriteReady: true,
		}},
		spot: client,
	}
	router, err := NewAccountRouter(registry)
	if err != nil {
		t.Fatal(err)
	}
	route, err := router.Route(context.Background(), "binance-main", "binance:demo:spot:BTC-USDT")
	if err != nil {
		t.Fatalf("Route: %v", err)
	}
	if !route.WriteReady || route.Instrument.Identity.NativeSymbol != "BTCUSDT" {
		t.Fatalf("route=%+v, want promoted canonical instrument", route)
	}
	exact, err := route.Driver.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument: &instrument, ClientOrderID: "1",
	})
	if err != nil || exact.State != domain.StateOpen {
		t.Fatalf("LookupExactOrder result=%+v err=%v", exact, err)
	}

	registry.status.WriteReady = false
	route, err = router.Route(context.Background(), "binance-main", "binance:demo:spot:BTC-USDT")
	if err != nil || route.WriteReady {
		t.Fatalf("unpromoted route=%+v err=%v", route, err)
	}
	recoveryRoute, err := router.RouteForRecovery(context.Background(), "binance-main", "binance:demo:spot:BTC-USDT")
	if err != nil || !recoveryRoute.WriteReady {
		t.Fatalf("recovery route=%+v err=%v", recoveryRoute, err)
	}
}

func TestExactDomainStateRejectsUnknownFoundStatus(t *testing.T) {
	_, err := exactDomainState(exchange.ExactOrderLookupResult{
		Status: exchange.ExactOrderFound,
		Order:  exchange.Order{Status: "venue-private-status"},
	})
	if err == nil {
		t.Fatal("unknown exact order status was accepted")
	}
}

type routingRegistry struct {
	instrument     exchange.Instrument
	status         account.Status
	recoveryStatus account.Status
	spot           exchange.SpotClient
}

func (registry *routingRegistry) Spot(domain.AccountID) (exchange.SpotClient, error) {
	return registry.spot, nil
}

func (*routingRegistry) Perp(domain.AccountID) (exchange.PerpClient, error) {
	return nil, account.ErrProductMismatch
}

func (registry *routingRegistry) Resolve(context.Context, domain.AccountID, domain.InstrumentID) (exchange.Instrument, error) {
	return registry.instrument, nil
}

func (registry *routingRegistry) Status(domain.AccountID) (account.Status, error) {
	return registry.status, nil
}

func (registry *routingRegistry) RecoveryStatus(domain.AccountID) (account.Status, error) {
	return registry.recoveryStatus, nil
}

type routingSpotClient struct {
	exchange.SpotClient
	lookupResult exchange.ExactOrderLookupResult
}

func (*routingSpotClient) PlaceOrder(context.Context, exchange.PlaceOrderRequest) (exchange.OrderAcknowledgement, error) {
	return exchange.OrderAcknowledgement{}, errors.New("not used")
}

func (client *routingSpotClient) LookupExactOrder(context.Context, exchange.ExactOrderLookupRequest) (exchange.ExactOrderLookupResult, error) {
	return client.lookupResult, nil
}
