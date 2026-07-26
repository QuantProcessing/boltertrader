package factoryclient

import (
	"context"
	"errors"
	"fmt"
	"net/http"
	"strings"
	"sync"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/shopspring/decimal"
)

type okxExactLookupRouter struct {
	mu       sync.Mutex
	requests []string
	handler  func(*http.Request) *http.Response
}

func (router *okxExactLookupRouter) RoundTrip(request *http.Request) (*http.Response, error) {
	router.mu.Lock()
	router.requests = append(router.requests, request.Method+" "+request.URL.RequestURI())
	router.mu.Unlock()
	if router.handler != nil {
		return router.handler(request), nil
	}
	return okxOpenAPIData(`[{"instType":"SPOT","instId":"BTC-USDT","ordId":"31","clOrdId":"101","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"}]`), nil
}

func (router *okxExactLookupRouter) count() int {
	router.mu.Lock()
	defer router.mu.Unlock()
	return len(router.requests)
}

func (router *okxExactLookupRouter) requestAt(index int) string {
	router.mu.Lock()
	defer router.mu.Unlock()
	if index < 0 || index >= len(router.requests) {
		return ""
	}
	return router.requests[index]
}

func TestOKXExactOrderLookupFindsSpotByClientOrderID(t *testing.T) {
	router := &okxExactLookupRouter{}
	client := NewOKXSpot("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductSpot, Settings{
		Endpoint:    "https://okx.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	})).(exchange.ExactOrderLookup)
	instrument := okxOpenAPIFixtureInstrument(exchange.ProductSpot)

	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:    &instrument,
		ClientOrderID: "101",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderFound || result.Order.Status != "open" ||
		result.Order.OrderID != "31" || result.Order.ClientOrderID != "101" ||
		result.Evidence.Method != exchange.ExactOrderEvidenceByClientOrderID ||
		!result.Evidence.Authoritative || !result.Evidence.Complete {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(router.requestAt(0), "/api/v5/trade/order?") ||
		!strings.Contains(router.requestAt(0), "instId=BTC-USDT") ||
		!strings.Contains(router.requestAt(0), "clOrdId=101") ||
		strings.Contains(router.requestAt(0), "ordId=") {
		t.Fatalf("request = %q, want exact client id query", router.requestAt(0))
	}
	if err := result.Validate(); err != nil {
		t.Fatalf("result Validate: %v", err)
	}
}

func TestOKXExactOrderLookupFindsPerpByVenueOrderIDWithContractMultiplier(t *testing.T) {
	router := &okxExactLookupRouter{handler: func(request *http.Request) *http.Response {
		return okxOpenAPIData(`[{"instType":"SWAP","instId":"BTC-USDT-SWAP","ordId":"42","clOrdId":"102","side":"sell","ordType":"limit","sz":"10","px":"99","accFillSz":"2","state":"partially_filled","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"true"}]`)
	}}
	client := NewOKXUSDTPerp("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductPerp, Settings{
		Endpoint:    "https://okx.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	})).(exchange.ExactOrderLookup)
	instrument := okxOpenAPIFixtureInstrument(exchange.ProductPerp)

	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:   &instrument,
		VenueOrderID: "42",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderFound || result.Order.Status != "partially_filled" ||
		result.Order.OrderID != "42" || !result.Order.ReduceOnly ||
		!result.Order.Quantity.Equal(decimal.RequireFromString("0.1")) ||
		!result.Order.Filled.Equal(decimal.RequireFromString("0.02")) ||
		result.Evidence.Method != exchange.ExactOrderEvidenceByVenueOrderID {
		t.Fatalf("result = %+v", result)
	}
	if !strings.Contains(router.requestAt(0), "instId=BTC-USDT-SWAP") ||
		!strings.Contains(router.requestAt(0), "ordId=42") ||
		strings.Contains(router.requestAt(0), "clOrdId=") {
		t.Fatalf("request = %q, want exact venue id query", router.requestAt(0))
	}
}

func TestOKXExactOrderLookupMaps51603ToAuthoritativeNotFound(t *testing.T) {
	router := &okxExactLookupRouter{handler: func(request *http.Request) *http.Response {
		return openAPIJSONResponse(`{"code":"51603","msg":"SECRET-not-found-message","data":[]}`)
	}}
	client := NewOKXSpot("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductSpot, Settings{
		Endpoint:    "https://okx.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	})).(exchange.ExactOrderLookup)
	instrument := okxOpenAPIFixtureInstrument(exchange.ProductSpot)

	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:    &instrument,
		ClientOrderID: "101",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderNotFound ||
		result.Evidence.Method != exchange.ExactOrderEvidenceByClientOrderID ||
		!result.Evidence.Authoritative || !result.Evidence.Complete {
		t.Fatalf("result = %+v", result)
	}
	if strings.Contains(fmt.Sprintf("%+v", result), "SECRET-not-found-message") {
		t.Fatal("not-found result leaked venue message")
	}
}

func TestOKXExactOrderLookupRejectsMalformedEvidence(t *testing.T) {
	tests := []struct {
		name string
		body string
	}{
		{
			name: "empty success data",
			body: `{"code":"0","msg":"","data":[]}`,
		},
		{
			name: "multiple success data",
			body: `{"code":"0","msg":"","data":[
				{"instType":"SPOT","instId":"BTC-USDT","ordId":"31","clOrdId":"101","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"},
				{"instType":"SPOT","instId":"BTC-USDT","ordId":"32","clOrdId":"101","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"}
			]}`,
		},
		{
			name: "client id mismatch",
			body: `{"code":"0","msg":"","data":[{"instType":"SPOT","instId":"BTC-USDT","ordId":"31","clOrdId":"999","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"}]}`,
		},
		{
			name: "instrument mismatch",
			body: `{"code":"0","msg":"","data":[{"instType":"SPOT","instId":"ETH-USDT","ordId":"31","clOrdId":"101","side":"buy","ordType":"limit","sz":"1","px":"99","accFillSz":"0","state":"live","cTime":"1720000000000","uTime":"1720000000000","reduceOnly":"false"}]}`,
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			router := &okxExactLookupRouter{handler: func(request *http.Request) *http.Response {
				return openAPIJSONResponse(test.body)
			}}
			client := NewOKXSpot("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductSpot, Settings{
				Endpoint:    "https://okx.invalid",
				Environment: "demo",
				HTTPClient:  &http.Client{Transport: router},
			})).(exchange.ExactOrderLookup)
			instrument := okxOpenAPIFixtureInstrument(exchange.ProductSpot)
			_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
				Instrument:    &instrument,
				ClientOrderID: "101",
			})
			if !errors.Is(err, exchange.ErrMalformedResponse) {
				t.Fatalf("LookupExactOrder error = %v, want malformed", err)
			}
		})
	}
}

func TestOKXExactOrderLookupInvalidRequestDoesNotSend(t *testing.T) {
	router := &okxExactLookupRouter{}
	client := NewOKXSpot("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductSpot, Settings{
		Endpoint:    "https://okx.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	})).(exchange.ExactOrderLookup)

	_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		ClientOrderID: "101",
	})
	if !errors.Is(err, exchange.ErrInvalidRequest) {
		t.Fatalf("LookupExactOrder error = %v, want invalid request", err)
	}
	if got := router.count(); got != 0 {
		t.Fatalf("invalid exact lookup sent %d requests", got)
	}
}

func TestOKXExactOrderLookupNormalizesErrorsWithoutLeakingVenueMessages(t *testing.T) {
	canary := "SECRET-OKX-DETAIL"
	router := &okxExactLookupRouter{handler: func(request *http.Request) *http.Response {
		return openAPIJSONResponse(`{"code":"50111","msg":"` + canary + `","data":[{"detail":"` + canary + `"}]}`)
	}}
	client := NewOKXSpot("key", "secret", "passphrase", okxOpenAPICatalogSettings(exchange.ProductSpot, Settings{
		Endpoint:    "https://okx.invalid",
		Environment: "demo",
		HTTPClient:  &http.Client{Transport: router},
	})).(exchange.ExactOrderLookup)
	instrument := okxOpenAPIFixtureInstrument(exchange.ProductSpot)

	_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:    &instrument,
		ClientOrderID: "101",
	})
	if !errors.Is(err, exchange.ErrAuthentication) {
		t.Fatalf("LookupExactOrder error = %v, want authentication", err)
	}
	if strings.Contains(fmt.Sprintf("%+v", err), canary) {
		t.Fatalf("normalized error leaked venue message: %v", err)
	}
}
