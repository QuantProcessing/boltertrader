package factoryclient

import (
	"context"
	"io"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
)

func TestLighterCandlesDerivesNativeWindowWhenPortableBoundsAreOmitted(t *testing.T) {
	var candleQuery url.Values
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/orderBookDetails":
			return openAPIJSONResponse(`{"code":200,"message":"","spot_order_book_details":[{"symbol":"ETH/USDC","market_id":2048,"market_type":"spot","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
		case "/api/v1/candles":
			candleQuery = request.URL.Query()
			return openAPIJSONResponse(`{"code":200,"message":"","r":"1m","c":[{"t":1720000000,"o":100,"h":101,"l":99,"c":100.5,"v":3}]}`), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":404}`)),
			}, nil
		}
	})
	client := NewLighterSpot(openAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.Candles(context.Background(), exchange.CandlesRequest{
		Instrument: "ETH/USDC",
		Interval:   "1m",
		Limit:      2,
	})
	if err != nil {
		t.Fatalf("Candles with omitted portable bounds: %v", err)
	}
	start := candleQuery.Get("start_timestamp")
	end := candleQuery.Get("end_timestamp")
	if start == "" || end == "" || start == "0" || end == "0" {
		t.Fatalf("native candle window = start:%q end:%q, want derived positive bounds", start, end)
	}
	startUnix, startErr := strconv.ParseInt(start, 10, 64)
	endUnix, endErr := strconv.ParseInt(end, 10, 64)
	if startErr != nil || endErr != nil || endUnix-startUnix != int64((2*time.Minute)/time.Second) {
		t.Fatalf("native candle window = start:%q end:%q, want two 1m intervals", start, end)
	}
	if got := candleQuery.Get("count_back"); got != "2" {
		t.Fatalf("native count_back = %q, want 2", got)
	}
	if page.Page.WindowStart.IsZero() || page.Page.WindowEnd.IsZero() ||
		!page.Page.WindowStart.Before(page.Page.WindowEnd) {
		t.Fatalf("page window = %s..%s, want derived ordered bounds", page.Page.WindowStart, page.Page.WindowEnd)
	}
	if got := page.Page.WindowEnd.Sub(page.Page.WindowStart); got != 2*time.Minute {
		t.Fatalf("page window duration = %s, want 2m", got)
	}
}

func TestLighterOrderHistoryFiltersTriggerOrdersOutsideExchangeSubset(t *testing.T) {
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/orderBookDetails":
			return openAPIJSONResponse(`{"code":200,"message":"","spot_order_book_details":[{"symbol":"ETH/USDC","market_id":2048,"market_type":"spot","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
		case "/api/v1/accountInactiveOrders":
			return openAPIJSONResponse(`{"code":200,"message":"","next_cursor":"","orders":[
				{"order_index":11,"client_order_index":101,"market_index":2048,"initial_base_amount":"0.01","filled_base_amount":"0","price":"100","type":"limit","time_in_force":"good-till-time","trigger_price":"95","status":"canceled","created_at":1720000000000,"updated_at":1720000001000},
				{"order_index":12,"client_order_index":102,"market_index":2048,"initial_base_amount":"0.02","filled_base_amount":"0.02","price":"101","type":"limit","time_in_force":"good-till-time","trigger_price":"0.00","status":"filled","created_at":1720000002000,"updated_at":1720000003000}
			]}`), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":404}`)),
			}, nil
		}
	})
	client := NewLighterSpot(openAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.OrderHistory(context.Background(), exchange.OrderHistoryRequest{
		Instrument: "ETH/USDC",
		Limit:      20,
	})
	if err != nil {
		t.Fatalf("OrderHistory: %v", err)
	}
	if len(page.Orders) != 1 || page.Orders[0].OrderID != "12" {
		t.Fatalf("portable history = %+v, want only common-subset order 12", page.Orders)
	}
}

func TestLighterFundingRateHistoryDerivesNativeWindowWhenPortableBoundsAreOmitted(t *testing.T) {
	var fundingQuery url.Values
	transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		switch request.URL.Path {
		case "/api/v1/orderBookDetails":
			return openAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
		case "/api/v1/fundings":
			fundingQuery = request.URL.Query()
			return openAPIJSONResponse(`{"code":200,"message":"","resolution":"1h","fundings":[{"timestamp":1720000000,"rate":"0.0001","value":"0","direction":"long"}]}`), nil
		default:
			return &http.Response{
				StatusCode: http.StatusNotFound,
				Header:     make(http.Header),
				Body:       io.NopCloser(strings.NewReader(`{"code":404}`)),
			}, nil
		}
	})
	client := NewLighterPerp(openAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:    "https://openapi.invalid",
		Environment: "testnet",
		HTTPClient:  &http.Client{Transport: transport},
	})

	page, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{
		Instrument: "ETH",
		Limit:      10,
	})
	if err != nil {
		t.Fatalf("FundingRateHistory with omitted portable bounds: %v", err)
	}
	start := fundingQuery.Get("start_timestamp")
	end := fundingQuery.Get("end_timestamp")
	startUnix, startErr := strconv.ParseInt(start, 10, 64)
	endUnix, endErr := strconv.ParseInt(end, 10, 64)
	if startErr != nil || endErr != nil || endUnix-startUnix != int64((10*time.Hour)/time.Second) {
		t.Fatalf("native funding window = start:%q end:%q, want ten 1h intervals", start, end)
	}
	if got := fundingQuery.Get("count_back"); got != "10" {
		t.Fatalf("native count_back = %q, want 10", got)
	}
	if page.Page.WindowStart.IsZero() || page.Page.WindowEnd.IsZero() ||
		page.Page.WindowEnd.Sub(page.Page.WindowStart) != 10*time.Hour {
		t.Fatalf("portable funding page window = %s..%s, want ten hours", page.Page.WindowStart, page.Page.WindowEnd)
	}
}
