package factoryclient

import (
	"context"
	"errors"
	"io"
	"net/http"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

func TestGatePerpMarkPriceRESTNormalizesFuturesContract(t *testing.T) {
	var seenPath string
	transport := roundTripFunc(func(request *http.Request) (*http.Response, error) {
		seenPath = request.URL.Path
		if request.Method != http.MethodGet {
			t.Fatalf("method=%s, want GET", request.Method)
		}
		if request.URL.RawQuery != "" {
			t.Fatalf("query=%q, want empty", request.URL.RawQuery)
		}
		return gateOpenAPIJSONResponse(`{"name":"BTC_USDT","mark_price":"43125.5"}`), nil
	})
	client := NewGateUSDTPerp("key", "secret", gateTestSettings(exchange.ProductPerp, transport))
	markREST, ok := client.(exchange.PerpMarkPriceREST)
	if !ok {
		t.Fatalf("NewGateUSDTPerp does not implement exchange.PerpMarkPriceREST")
	}

	before := time.Now().UTC()
	event, err := markREST.MarkPrice(context.Background(), gateTestInstrument(exchange.ProductPerp))
	after := time.Now().UTC()
	if err != nil {
		t.Fatalf("MarkPrice: %v", err)
	}
	if seenPath != "/futures/usdt/contracts/BTC_USDT" {
		t.Fatalf("path=%s, want futures contract by requested native symbol", seenPath)
	}
	if event.Instrument.Identity != gateTestInstrument(exchange.ProductPerp).Identity {
		t.Fatalf("instrument identity=%+v", event.Instrument.Identity)
	}
	if !event.Price.Equal(decimal.RequireFromString("43125.5")) {
		t.Fatalf("price=%s, want 43125.5", event.Price)
	}
	if event.Time.Before(before) || event.Time.After(after) || event.Time.Location() != time.UTC {
		t.Fatalf("time=%s outside local UTC receive window [%s,%s]", event.Time, before, after)
	}
}

func TestGatePerpMarkPriceRESTRejectsMalformedFuturesContract(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{name: "instrument mismatch", body: `{"name":"ETH_USDT","mark_price":"43125.5"}`},
		{name: "zero price", body: `{"name":"BTC_USDT","mark_price":"0"}`},
		{name: "invalid price", body: `{"name":"BTC_USDT","mark_price":"bad"}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			transport := roundTripFunc(func(*http.Request) (*http.Response, error) {
				return gateOpenAPIJSONResponse(test.body), nil
			})
			markREST := NewGateUSDTPerp("key", "secret", gateTestSettings(exchange.ProductPerp, transport)).(exchange.PerpMarkPriceREST)

			_, err := markREST.MarkPrice(context.Background(), gateTestInstrument(exchange.ProductPerp))
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("MarkPrice error=%v, want ErrMalformedResponse", err)
			}
		})
	}
}

func TestGatePerpMarkPriceRESTNormalizesTransportAndVenueErrors(t *testing.T) {
	tests := []struct {
		name string
		rt   http.RoundTripper
		kind exchange.ErrorKind
	}{
		{
			name: "transport",
			rt: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return nil, errors.New("dial failed")
			}),
			kind: exchange.KindTransport,
		},
		{
			name: "server",
			rt: roundTripFunc(func(*http.Request) (*http.Response, error) {
				return &http.Response{
					StatusCode: http.StatusInternalServerError,
					Header:     make(http.Header),
					Body:       io.NopCloser(strings.NewReader(`{"label":"INTERNAL","message":"upstream unavailable"}`)),
				}, nil
			}),
			kind: exchange.KindVenueRejected,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			markREST := NewGateUSDTPerp("key", "secret", gateTestSettings(exchange.ProductPerp, test.rt)).(exchange.PerpMarkPriceREST)

			_, err := markREST.MarkPrice(context.Background(), gateTestInstrument(exchange.ProductPerp))
			assertGateErrorKind(t, err, test.kind)
			if errors.Is(err, exchange.ErrUnsupported) {
				t.Fatalf("MarkPrice error=%v, must not become ErrUnsupported", err)
			}
		})
	}
}
