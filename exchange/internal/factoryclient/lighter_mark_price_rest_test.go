package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"net/url"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestLighterPerpMarkPriceRESTNormalizesOrderBookDetailsMarkPrice(t *testing.T) {
	var queries []url.Values
	transport := lighterOpenAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
		if request.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", request.Method)
		}
		if request.URL.Path != "/api/v1/orderBookDetails" {
			t.Fatalf("path=%s, want /api/v1/orderBookDetails", request.URL.Path)
		}
		queries = append(queries, request.URL.Query())
		switch len(queries) {
		case 1:
			if request.URL.RawQuery != "" {
				t.Fatalf("metadata query=%q, want empty", request.URL.RawQuery)
			}
			return lighterOpenAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
		case 2:
			if got := queries[1].Encode(); got != "market_id=0" {
				t.Fatalf("mark query=%q, want market_id=0", got)
			}
			return lighterOpenAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"43125.5","index_price":"43120.1"}]}`), nil
		default:
			t.Fatalf("unexpected orderBookDetails call #%d", len(queries))
		}
		return nil, nil
	})
	client := NewLighterPerp(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
		Endpoint:   "https://openapi.invalid",
		HTTPClient: &http.Client{Transport: transport},
	})
	markREST, ok := client.(exchange.PerpMarkPriceREST)
	if !ok {
		t.Fatalf("NewLighterPerp does not implement exchange.PerpMarkPriceREST")
	}

	instrument := lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH", "0")
	before := time.Now().UTC()
	event, err := markREST.MarkPrice(context.Background(), instrument)
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	if len(queries) != 2 {
		t.Fatalf("orderBookDetails calls = %d, want metadata + mark", len(queries))
	}
	if event.Instrument.Identity != instrument.Identity {
		t.Fatalf("instrument identity=%+v, want %+v", event.Instrument.Identity, instrument.Identity)
	}
	if !event.Price.Equal(decimal.RequireFromString("43125.5")) {
		t.Fatalf("price=%s, want 43125.5", event.Price)
	}
	if event.Time.Before(before) || event.Time.After(after) || event.Time.Location() != time.UTC {
		t.Fatalf("time=%s outside local UTC receive window [%s,%s]", event.Time, before, after)
	}
}

func TestLighterPerpMarkPriceRESTRejectsMalformedRows(t *testing.T) {
	tests := []struct {
		name     string
		markBody string
	}{
		{name: "empty rows", markBody: `{"code":200,"message":"","order_book_details":[]}`},
		{name: "multiple rows", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"43125.5"},{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"43125.6"}]}`},
		{name: "wrong market id", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":1,"market_type":"perp","mark_price":"43125.5"}]}`},
		{name: "wrong market type", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"spot","mark_price":"43125.5"}]}`},
		{name: "wrong symbol", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"BTC","market_id":0,"market_type":"perp","mark_price":"43125.5"}]}`},
		{name: "zero price", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"0"}]}`},
		{name: "invalid price", markBody: `{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"bad"}]}`},
		{name: "malformed code", markBody: `{"code":500,"message":"bad","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","mark_price":"43125.5"}]}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			calls := 0
			transport := lighterOpenAPIRoundTripFunc(func(*http.Request) (*http.Response, error) {
				calls++
				if calls == 1 {
					return lighterOpenAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
				}
				return lighterOpenAPIJSONResponse(test.markBody), nil
			})
			markREST := NewLighterPerp(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
				Endpoint:   "https://openapi.invalid",
				HTTPClient: &http.Client{Transport: transport},
			}).(exchange.PerpMarkPriceREST)

			_, err := markREST.MarkPrice(context.Background(), lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH", "0"))
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("MarkPrice error=%v, want ErrMalformedResponse", err)
			}
			if errors.Is(err, exchange.ErrUnsupported) {
				t.Fatalf("MarkPrice error=%v, must not become ErrUnsupported", err)
			}
		})
	}
}

func TestLighterPerpMarkPriceRESTNormalizesTransportAndHTTPMalformed(t *testing.T) {
	tests := []struct {
		name string
		rt   http.RoundTripper
		kind exchange.ErrorKind
	}{
		{
			name: "transport",
			rt: lighterOpenAPIRoundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			}),
			kind: exchange.KindTransport,
		},
		{
			name: "http malformed",
			rt: lighterOpenAPIRoundTripFunc(func(request *http.Request) (*http.Response, error) {
				if request.URL.RawQuery == "" {
					return lighterOpenAPIJSONResponse(`{"code":200,"message":"","order_book_details":[{"symbol":"ETH","market_id":0,"market_type":"perp","min_base_amount":"0.001","min_quote_amount":"5","size_decimals":3,"price_decimals":2,"supported_quote_decimals":6}]}`), nil
				}
				return &http.Response{
					StatusCode: http.StatusOK,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{`)),
				}, nil
			}),
			kind: exchange.KindMalformedResponse,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			markREST := NewLighterPerp(lighterOpenAPILighterPrivateKey, 7, 2, Settings{
				Endpoint:   "https://openapi.invalid",
				HTTPClient: &http.Client{Transport: test.rt},
			}).(exchange.PerpMarkPriceREST)

			_, err := markREST.MarkPrice(context.Background(), lighterTestInstrument(exchange.ProductPerp, "ETH-USDC", "ETH", "0"))
			assertExchangeErrorKind(t, err, test.kind)
			if errors.Is(err, exchange.ErrUnsupported) {
				t.Fatalf("MarkPrice error=%v, must not become ErrUnsupported", err)
			}
		})
	}
}
