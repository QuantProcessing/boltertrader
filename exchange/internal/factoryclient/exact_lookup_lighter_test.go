package factoryclient

import (
	"context"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type lighterExactLookupTransport struct {
	marketID int
	active   string
	inactive string
	calls    atomic.Int64
}

func (transport *lighterExactLookupTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	switch request.URL.Path {
	case "/api/v1/orderBookDetails":
		return openAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":2,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}],"spot_order_book_details":[{"symbol":"BTC-USDC","market_id":1,"market_type":"spot","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":1,"supported_quote_decimals":6}]}`), nil
	case "/api/v1/accountActiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[` + transport.active + `]}`), nil
	case "/api/v1/accountInactiveOrders":
		return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"next-history-page","orders":[` + transport.inactive + `]}`), nil
	default:
		return &http.Response{
			StatusCode: http.StatusNotFound,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"code":404,"message":"unexpected route"}`)),
		}, nil
	}
}

func TestLighterExactOrderLookupFindsActiveAndInactiveOwnedOrders(t *testing.T) {
	for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		t.Run(string(product)+"/active-client-id", func(t *testing.T) {
			client, instrument, transport := newLighterExactLookupClient(t, product)
			transport.active = lighterExactOrderJSON(transport.marketID, 30, 101, "open")

			result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
				Instrument:    &instrument,
				ClientOrderID: "101",
			})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound || result.Order.OrderID != "30" || result.Order.ClientOrderID != "101" {
				t.Fatalf("active exact result = %+v", result)
			}
			if result.Order.Status != "open" || !result.Evidence.Authoritative || !result.Evidence.Complete {
				t.Fatalf("active exact evidence = %+v", result)
			}
		})

		t.Run(string(product)+"/inactive-venue-id", func(t *testing.T) {
			client, instrument, transport := newLighterExactLookupClient(t, product)
			transport.inactive = lighterExactOrderJSON(transport.marketID, 30, 101, "filled")

			result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
				Instrument:   &instrument,
				VenueOrderID: "30",
			})
			if err != nil {
				t.Fatalf("LookupExactOrder: %v", err)
			}
			if result.Status != exchange.ExactOrderFound || result.Order.Status != "filled" {
				t.Fatalf("inactive exact result = %+v", result)
			}
		})
	}
}

func TestLighterExactOrderLookupNeverPromotesPagedAbsenceToNotFound(t *testing.T) {
	client, instrument, transport := newLighterExactLookupClient(t, exchange.ProductSpot)
	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:    &instrument,
		ClientOrderID: "999",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderUnconfirmed {
		t.Fatalf("absent Lighter order status = %q, want unconfirmed", result.Status)
	}
	if result.Evidence.Authoritative || result.Evidence.Complete || result.Evidence.NativeCursor != "next-history-page" {
		t.Fatalf("paged absence evidence = %+v", result.Evidence)
	}
	if transport.calls.Load() != 3 {
		t.Fatalf("lookup calls = %d, want metadata + active + inactive", transport.calls.Load())
	}
}

func TestLighterExactOrderLookupRejectsInvalidRequestBeforeIO(t *testing.T) {
	client, _, transport := newLighterExactLookupClient(t, exchange.ProductSpot)
	_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{ClientOrderID: "101"})
	if err == nil {
		t.Fatal("lookup without instrument succeeded")
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("invalid lookup performed %d network calls", transport.calls.Load())
	}
}

func newLighterExactLookupClient(
	t *testing.T,
	product exchange.Product,
) (exchange.ExactOrderLookup, exchange.Instrument, *lighterExactLookupTransport) {
	t.Helper()
	marketID := 1
	instrument := lighterExactInstrument(exchange.ProductSpot, "BTC-USDC", "BTC-USDC", "1")
	transport := &lighterExactLookupTransport{marketID: marketID}
	settings := Settings{
		Endpoint:   "https://exact.invalid",
		HTTPClient: &http.Client{Transport: transport},
	}
	var raw any
	if product == exchange.ProductPerp {
		marketID = 2
		transport.marketID = marketID
		instrument = lighterExactInstrument(exchange.ProductPerp, "BTC-USDC", "BTC", "2")
		raw = NewLighterPerp(openAPILighterPrivateKey, 7, 2, settings)
	} else {
		raw = NewLighterSpot(openAPILighterPrivateKey, 7, 2, settings)
	}
	client, ok := raw.(exchange.ExactOrderLookup)
	if !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", raw)
	}
	return client, instrument, transport
}

func lighterExactOrderJSON(marketID int, orderID, clientID int64, status string) string {
	return `{"order_index":` + int64String(orderID) +
		`,"client_order_index":` + int64String(clientID) +
		`,"market_index":` + intString(marketID) +
		`,"initial_base_amount":"1","remaining_base_amount":"0","filled_base_amount":"1","price":"99","is_ask":false,"type":"limit","time_in_force":"good-till-time","reduce_only":false,"status":"` +
		status + `","created_at":1720000000000,"updated_at":1720000001000}`
}

func int64String(value int64) string {
	return intString(int(value))
}

func lighterExactInstrument(product exchange.Product, symbol, nativeSymbol, nativeID string) exchange.Instrument {
	settlement := ""
	if product == exchange.ProductPerp {
		settlement = "USDC"
	}
	instrument := newInstrument(
		exchange.VenueLighter,
		product,
		symbol,
		"BTC",
		"USDC",
		settlement,
		nativeSymbol,
		nativeID,
		decimal.RequireFromString("0.1"),
		decimal.RequireFromString("0.001"),
		decimal.RequireFromString("0.001"),
		exchange.OptionalDecimal{},
		oneContract(),
	)
	instrument.Encoding.PriceScale = exchange.OptionalInt{Value: 1, Valid: true}
	instrument.Encoding.QuantityScale = exchange.OptionalInt{Value: 3, Valid: true}
	instrument.Encoding.QuoteScale = exchange.OptionalInt{Value: 6, Valid: true}
	return instrument
}
