package factoryclient

import (
	"context"
	"errors"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestAsterOpenAPIFixtureFileCoversRESTMatrix(t *testing.T) {
	TestOpenAPIAsterRESTExecutionMatrix(t)
}

func TestAsterOrderBookNormalizesVenueDepthLimitAndTruncatesResult(t *testing.T) {
	venueLimit, err := asterDepthRequestLimit(1)
	if err != nil {
		t.Fatalf("asterDepthRequestLimit: %v", err)
	}
	if venueLimit != 5 {
		t.Fatalf("venue depth limit = %d, want 5", venueLimit)
	}
	book, err := asterOrderBook(
		"BTC-USDT",
		1,
		[][]string{{"99", "1"}, {"98", "2"}},
		[][]string{{"101", "1"}, {"102", "2"}},
		1,
		1,
		exchange.ProductSpot,
	)
	if err != nil {
		t.Fatalf("asterOrderBook: %v", err)
	}
	if len(book.Bids) != 1 || len(book.Asks) != 1 || !book.Bids[0].Price.Equal(decimal.NewFromInt(99)) {
		t.Fatalf("order book was not truncated to requested depth: %+v", book)
	}
}

func TestAsterInvalidEndpointReturnsInvalidConfigInsteadOfPanic(t *testing.T) {
	spot := NewAsterSpot(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, Settings{Endpoint: "://bad"})
	_, err := spot.OpenOrders(context.Background(), exchange.OpenOrdersRequest{Instrument: "BTC-USDT"})
	if !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("Aster spot nil sdk error = %v, want ErrInvalidConfig", err)
	}
	perp := NewAsterUSDTPerp(testAsterUserAddress, testAsterPrivateKey, testAsterUserAddress, Settings{Endpoint: "://bad"})
	_, err = perp.FundingRate(context.Background(), exchange.FundingRateRequest{Instrument: "BTC-USDT"})
	if !errors.Is(err, exchange.ErrInvalidConfig) {
		t.Fatalf("Aster perp nil sdk error = %v, want ErrInvalidConfig", err)
	}
}

func TestAsterNewMarketOrderAcknowledgementRemainsPending(t *testing.T) {
	ack, err := asterAck(exchange.ProductSpot, exchange.OrderOperationPlace, "BTC-USDT", 11, "401", "NEW", "MARKET", "0", "0")
	if err != nil {
		t.Fatalf("asterAck: %v", err)
	}
	if ack.State != exchange.AckAcceptedPending {
		t.Fatalf("market NEW acknowledgement state = %s, want %s", ack.State, exchange.AckAcceptedPending)
	}
}

func TestAsterPostOnlyUsesVenueGTXTimeInForce(t *testing.T) {
	_, orderType, timeInForce := orderParams(exchange.PlaceOrderRequest{
		Type:        exchange.OrderTypeLimit,
		LimitPolicy: exchange.LimitPolicyPostOnly,
	})
	if orderType != "LIMIT" || timeInForce != "GTX" {
		t.Fatalf("post-only native params = (%q, %q), want (LIMIT, GTX)", orderType, timeInForce)
	}
}
