package hyperliquid

import (
	"context"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
)

func TestClientRecentTradesUsesOfficialInfoRequest(t *testing.T) {
	client := NewClient()
	client.BaseURL = "https://hyperliquid.test"
	client.Http = &http.Client{Transport: hyperliquidRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.URL.Path != "/info" || body["type"] != "recentTrades" || body["coin"] != "BTC" {
			t.Fatalf("unexpected request path=%s body=%v", req.URL.Path, body)
		}
		return hyperliquidResponse(req, `[{"coin":"BTC","side":"B","px":"65000.5","sz":"0.01","hash":"0xabc","time":1700000000000,"tid":77,"users":["0x1","0x2"]}]`), nil
	})}

	trades, err := client.RecentTrades(context.Background(), "BTC")
	if err != nil {
		t.Fatalf("RecentTrades: %v", err)
	}
	if len(trades) != 1 || trades[0].Coin != "BTC" || trades[0].Price != "65000.5" ||
		trades[0].Size != "0.01" || trades[0].TradeID != 77 || len(trades[0].Users) != 2 {
		t.Fatalf("unexpected trades: %+v", trades)
	}
}

func TestClientHistoricalOrdersUsesOfficialInfoRequest(t *testing.T) {
	client := NewClient()
	client.BaseURL = "https://hyperliquid.test"
	client.Http = &http.Client{Transport: hyperliquidRoundTripFunc(func(req *http.Request) (*http.Response, error) {
		var body map[string]any
		if err := json.NewDecoder(req.Body).Decode(&body); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		if req.URL.Path != "/info" || body["type"] != "historicalOrders" || body["user"] != "0xuser" {
			t.Fatalf("unexpected request path=%s body=%v", req.URL.Path, body)
		}
		return hyperliquidResponse(req, `[{"order":{"coin":"BTC","side":"A","limitPx":"65001","sz":"0","oid":91,"cloid":"0x1234","timestamp":1700000000000,"origSz":"0.01","reduceOnly":true,"orderType":"Limit","tif":"Ioc"},"status":"filled","statusTimestamp":1700000000100}]`), nil
	})}

	orders, err := client.HistoricalOrders(context.Background(), "0xuser")
	if err != nil {
		t.Fatalf("HistoricalOrders: %v", err)
	}
	if len(orders) != 1 || orders[0].Order.OrderID != 91 || orders[0].Status != "filled" ||
		orders[0].StatusTimestamp != 1700000000100 || !orders[0].Order.ReduceOnly {
		t.Fatalf("unexpected historical orders: %+v", orders)
	}
}

func TestProtectedMarketPriceMatchesOfficialSDKRounding(t *testing.T) {
	tests := []struct {
		name         string
		mid          float64
		buy          bool
		spot         bool
		sizeDecimals int
		want         float64
	}{
		{name: "spot buy", mid: 123.456789, buy: true, spot: true, sizeDecimals: 2, want: 129.63},
		{name: "spot sell", mid: 123.456789, buy: false, spot: true, sizeDecimals: 2, want: 117.28},
		{name: "perp buy", mid: 65000.123, buy: true, sizeDecimals: 5, want: 68250},
		{name: "perp sell", mid: 65000.123, buy: false, sizeDecimals: 5, want: 61750},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			got, err := ProtectedMarketPrice(test.mid, test.buy, test.spot, test.sizeDecimals)
			if err != nil {
				t.Fatalf("ProtectedMarketPrice: %v", err)
			}
			if got != test.want {
				t.Fatalf("ProtectedMarketPrice = %v, want %v", got, test.want)
			}
		})
	}
}

func TestMarketBoundaryErrorsRemainClassifiable(t *testing.T) {
	reference := NewMarketReferenceError(ErrMarketReferenceMalformed, errors.New("missing BTC mid"))
	if !errors.Is(reference, ErrMarketReferenceMalformed) || errors.Is(reference, ErrMutationOutcomeUnknown) {
		t.Fatalf("reference error classification = %v", reference)
	}
	unknown := NewMutationOutcomeUnknown(errors.New("connection reset"))
	if !errors.Is(unknown, ErrMutationOutcomeUnknown) || errors.Is(unknown, ErrMarketReferenceUnavailable) {
		t.Fatalf("mutation error classification = %v", unknown)
	}
	if strings.Contains(reference.Error(), "0xsecret") || strings.Contains(unknown.Error(), "0xsecret") {
		t.Fatal("boundary errors leaked an unexpected secret")
	}
}

type hyperliquidRoundTripFunc func(*http.Request) (*http.Response, error)

func (fn hyperliquidRoundTripFunc) RoundTrip(req *http.Request) (*http.Response, error) {
	return fn(req)
}

func hyperliquidResponse(req *http.Request, body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Body:       io.NopCloser(strings.NewReader(body)),
		Header:     make(http.Header),
		Request:    req,
	}
}
