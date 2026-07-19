package factoryclient

import (
	"context"
	"errors"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestBybitBitgetValidRESTRequestsRejectNilContextBeforeNetwork(t *testing.T) {
	transport := &noSendTransport{}
	settings := Settings{Endpoint: "https://openapi.invalid", Environment: "testnet", HTTPClient: &http.Client{Transport: transport}}
	spotClients := map[string]exchange.SpotClient{
		"bybit":  NewBybitSpot("key", "secret", settings),
		"bitget": NewBitgetSpot("key", "secret", "passphrase", settings),
	}
	for name, client := range spotClients {
		t.Run(name+"/spot", func(t *testing.T) {
			assertNilContextInvalid(t, map[string]error{
				"Instruments":  errorFromSlice(client.Instruments(nil)),
				"OrderBook":    errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{Instrument: "BTC-USDT", Limit: 5})),
				"Candles":      errorFromValue(client.Candles(nil, exchange.CandlesRequest{Instrument: "BTC-USDT", Interval: "1m", Limit: 1})),
				"PublicTrades": errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{Instrument: "BTC-USDT", Limit: 1})),
				"PlaceOrder":   errorFromValue(client.PlaceOrder(nil, validBoundaryOrder(exchange.ProductSpot, "BTC-USDT"))),
				"CancelOrder":  errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"})),
				"OpenOrders":   errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{Instrument: "BTC-USDT", Limit: 1})),
				"OrderHistory": errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{Instrument: "BTC-USDT", Limit: 1})),
				"Fills":        errorFromValue(client.Fills(nil, exchange.FillsRequest{Instrument: "BTC-USDT", Limit: 1})),
				"Balances":     errorFromSlice(client.Balances(nil)),
				"SpotAccount":  errorFromValue(client.SpotAccount(nil)),
			})
		})
	}

	perpClients := map[string]exchange.PerpClient{
		"bybit":  NewBybitLinearPerp("key", "secret", "USDT", settings),
		"bitget": NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings),
	}
	for name, client := range perpClients {
		t.Run(name+"/perp", func(t *testing.T) {
			assertNilContextInvalid(t, map[string]error{
				"Instruments":        errorFromSlice(client.Instruments(nil)),
				"OrderBook":          errorFromValue(client.OrderBook(nil, exchange.OrderBookRequest{Instrument: "BTC-USDT", Limit: 5})),
				"Candles":            errorFromValue(client.Candles(nil, exchange.CandlesRequest{Instrument: "BTC-USDT", Interval: "1m", Limit: 1})),
				"PublicTrades":       errorFromValue(client.PublicTrades(nil, exchange.PublicTradesRequest{Instrument: "BTC-USDT", Limit: 1})),
				"PlaceOrder":         errorFromValue(client.PlaceOrder(nil, validBoundaryOrder(exchange.ProductPerp, "BTC-USDT"))),
				"CancelOrder":        errorFromValue(client.CancelOrder(nil, exchange.CancelOrderRequest{Instrument: "BTC-USDT", OrderID: "11"})),
				"OpenOrders":         errorFromValue(client.OpenOrders(nil, exchange.OpenOrdersRequest{Instrument: "BTC-USDT", Limit: 1})),
				"OrderHistory":       errorFromValue(client.OrderHistory(nil, exchange.OrderHistoryRequest{Instrument: "BTC-USDT", Limit: 1})),
				"Fills":              errorFromValue(client.Fills(nil, exchange.FillsRequest{Instrument: "BTC-USDT", Limit: 1})),
				"Balances":           errorFromSlice(client.Balances(nil)),
				"PerpAccount":        errorFromValue(client.PerpAccount(nil)),
				"Positions":          errorFromSlice(client.Positions(nil, exchange.PositionsRequest{Instrument: "BTC-USDT"})),
				"FundingRate":        errorFromValue(client.FundingRate(nil, exchange.FundingRateRequest{Instrument: "BTC-USDT"})),
				"FundingRateHistory": errorFromValue(client.FundingRateHistory(nil, exchange.FundingRateHistoryRequest{Instrument: "BTC-USDT", Limit: 1})),
				"SetLeverage":        errorFromValue(client.SetLeverage(nil, exchange.SetLeverageRequest{Instrument: "BTC-USDT", Leverage: 2})),
			})
		})
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("valid nil-context requests made %d network calls", got)
	}
}

func TestBybitBitgetPerpClientsRejectQuoteSettlementMismatch(t *testing.T) {
	transport := &noSendTransport{}
	settings := Settings{
		Endpoint:          "https://openapi.invalid",
		WebSocketEndpoint: "ws://openapi.invalid",
		Environment:       "testnet",
		HTTPClient:        &http.Client{Transport: transport},
	}
	tests := []struct {
		name       string
		client     exchange.PerpClient
		instrument string
	}{
		{name: "bybit/usdt", client: NewBybitLinearPerp("key", "secret", "USDT", settings), instrument: "BTC-USDC"},
		{name: "bybit/usdc", client: NewBybitLinearPerp("key", "secret", "USDC", settings), instrument: "BTC-USDT"},
		{name: "bitget/usdt", client: NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", settings), instrument: "BTC-USDC"},
		{name: "bitget/usdc", client: NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", settings), instrument: "BTC-USDT"},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			_, err := test.client.OrderBook(context.Background(), exchange.OrderBookRequest{Instrument: test.instrument, Limit: 1})
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("REST quote mismatch error = %v, want ErrInvalidRequest", err)
			}
			_, err = test.client.WebSocket().PlaceOrder(context.Background(), validBoundaryOrder(exchange.ProductPerp, test.instrument))
			if !errors.Is(err, exchange.ErrInvalidRequest) {
				t.Fatalf("WS quote mismatch error = %v, want ErrInvalidRequest", err)
			}
		})
	}
	if got := transport.calls.Load(); got != 0 {
		t.Fatalf("quote mismatch requests made %d network calls", got)
	}
}

func TestBybitUSDCNormalizedInstrumentUsesNativePerpetualSymbol(t *testing.T) {
	client := NewBybitLinearPerp("key", "secret", "USDC", Settings{}).(*bybitPerpClient)
	native, canonical, err := client.symbols("BTC-USDC", "OrderBook")
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if native != "BTCPERP" || canonical != "BTC-USDC" {
		t.Fatalf("symbols = (%q, %q), want (BTCPERP, BTC-USDC)", native, canonical)
	}
}

func TestBitgetUSDCNormalizedInstrumentUsesNativePerpetualSymbol(t *testing.T) {
	client := NewBitgetPerp("key", "secret", "passphrase", "USDC-FUTURES", Settings{}).(*bitgetPerpClient)
	native, canonical, err := client.symbols("BTC-USDC", "OrderBook")
	if err != nil {
		t.Fatalf("symbols: %v", err)
	}
	if native != "BTCPERP" || canonical != "BTC-USDC" {
		t.Fatalf("symbols = (%q, %q), want (BTCPERP, BTC-USDC)", native, canonical)
	}
}

func TestFundingHistoryRequestSemanticsAreTruthful(t *testing.T) {
	t.Run("Bybit wires supported window and rejects cursor", func(t *testing.T) {
		var query string
		client := NewBybitLinearPerp("key", "secret", "USDT", Settings{
			Endpoint: "https://openapi.invalid",
			HTTPClient: &http.Client{Transport: openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				query = request.URL.RawQuery
				return bybitData(`{"category":"linear","list":[{"symbol":"BTCUSDT","fundingRate":"0.0001","fundingRateTimestamp":"1720000000000"}]}`), nil
			})},
		})
		start := time.UnixMilli(1719990000000)
		end := start.Add(time.Hour)
		if _, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: "BTC-USDT", Start: start, End: end, Limit: 7}); err != nil {
			t.Fatalf("FundingRateHistory: %v", err)
		}
		for _, want := range []string{"startTime=1719990000000", "endTime=1719993600000", "limit=7"} {
			if !strings.Contains(query, want) {
				t.Fatalf("Bybit funding query %q missing %q", query, want)
			}
		}
		_, err := client.FundingRateHistory(context.Background(), exchange.FundingRateHistoryRequest{Instrument: "BTC-USDT", Cursor: "next"})
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Fatalf("Bybit cursor error = %v, want ErrInvalidRequest", err)
		}
	})

	t.Run("Bitget rejects unsupported window and cursor before network", func(t *testing.T) {
		transport := &noSendTransport{}
		client := NewBitgetPerp("key", "secret", "passphrase", "USDT-FUTURES", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
		start := time.UnixMilli(1719990000000)
		for name, request := range map[string]exchange.FundingRateHistoryRequest{
			"start":  {Instrument: "BTC-USDT", Start: start},
			"end":    {Instrument: "BTC-USDT", End: start.Add(time.Hour)},
			"cursor": {Instrument: "BTC-USDT", Cursor: "next"},
		} {
			t.Run(name, func(t *testing.T) {
				_, err := client.FundingRateHistory(context.Background(), request)
				if !errors.Is(err, exchange.ErrInvalidRequest) {
					t.Fatalf("error = %v, want ErrInvalidRequest", err)
				}
			})
		}
		if got := transport.calls.Load(); got != 0 {
			t.Fatalf("unsupported Bitget funding filters made %d network calls", got)
		}
	})
}

func TestRequiredRESTTimestampsReturnMalformedResponse(t *testing.T) {
	tests := []struct {
		name string
		call func(http.RoundTripper) error
	}{
		{
			name: "bybit order book",
			call: func(transport http.RoundTripper) error {
				client := NewBybitSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.OrderBook(context.Background(), exchange.OrderBookRequest{Instrument: "BTC-USDT"})
				return err
			},
		},
		{
			name: "bitget public trade",
			call: func(transport http.RoundTripper) error {
				client := NewBitgetSpot("key", "secret", "passphrase", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.PublicTrades(context.Background(), exchange.PublicTradesRequest{Instrument: "BTC-USDT"})
				return err
			},
		},
		{
			name: "bybit candle",
			call: func(transport http.RoundTripper) error {
				client := NewBybitSpot("key", "secret", Settings{Endpoint: "https://openapi.invalid", HTTPClient: &http.Client{Transport: transport}})
				_, err := client.Candles(context.Background(), exchange.CandlesRequest{Instrument: "BTC-USDT", Interval: "1m"})
				return err
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := openAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				switch request.URL.Path {
				case "/v5/market/orderbook":
					return bybitData(`{"s":"BTCUSDT","b":[["99","1"]],"a":[["101","1"]],"ts":0,"u":1}`), nil
				case "/api/v3/market/fills":
					return bitgetData(`[{"execId":"1","price":"100","size":"1","side":"buy","ts":"bad"}]`), nil
				case "/v5/market/kline":
					return bybitData(`{"category":"spot","symbol":"BTCUSDT","list":[["bad","100","101","99","100","1","100"]]}`), nil
				default:
					t.Fatalf("unexpected path %s", request.URL.Path)
					return nil, nil
				}
			})
			assertExchangeErrorKind(t, test.call(transport), exchange.KindMalformedResponse)
		})
	}
}

func TestWebSocketReferenceRejectsMalformedRequiredTimestamps(t *testing.T) {
	meta := clientMeta{venue: exchange.VenueBybit, product: exchange.ProductPerp}
	_, err := strictReference(meta, "WatchReference", "BTC-USDT", "100", "0.0001", "", "bad", time.UnixMilli(1720000000000))
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)

	_, err = strictReference(meta, "WatchReference", "BTC-USDT", "100", "", "", "", time.Time{})
	assertExchangeErrorKind(t, err, exchange.KindMalformedResponse)
}

func validBoundaryOrder(product exchange.Product, instrument string) exchange.PlaceOrderRequest {
	return exchange.PlaceOrderRequest{
		Instrument:    instrument,
		ClientOrderID: "401",
		Side:          exchange.SideBuy,
		Type:          exchange.OrderTypeLimit,
		Quantity:      decimal.NewFromInt(1),
		LimitPrice:    decimal.NewFromInt(100),
		LimitPolicy:   exchange.LimitPolicyResting,
		ReduceOnly:    product == exchange.ProductPerp,
	}
}

func assertNilContextInvalid(t *testing.T, results map[string]error) {
	t.Helper()
	for operation, err := range results {
		if !errors.Is(err, exchange.ErrInvalidRequest) {
			t.Errorf("%s error = %v, want ErrInvalidRequest", operation, err)
		}
	}
}
