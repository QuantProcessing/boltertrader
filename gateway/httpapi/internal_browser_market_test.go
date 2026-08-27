package httpapi

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"
)

type browserMarketAuthority struct {
	requests []AuthorityRequest
	now      time.Time
}

func (authority *browserMarketAuthority) Execute(_ context.Context, request AuthorityRequest) (AuthorityResponse, error) {
	authority.requests = append(authority.requests, request)
	switch request.Operation {
	case AuthorityCatalogInstruments:
		return AuthorityResponse{Body: map[string]any{
			"revision": "gateway-7", "observedAt": authority.now,
			"instruments": []map[string]any{{
				"instrumentId": "binance:live:perp:USDT:BTC-USDT", "venue": "binance",
				"environment": "live", "product": "perp", "symbol": "BTC-USDT", "ready": true,
			}},
		}}, nil
	case AuthorityCatalogVenues:
		return AuthorityResponse{Body: []map[string]any{{"venue": "binance", "environment": "live", "ready": true}}}, nil
	case AuthorityMarketData:
		return AuthorityResponse{Body: map[string]any{"instrumentId": request.Query.Get("instrumentId"), "markPrice": "100"}}, nil
	case AuthorityCandles:
		return AuthorityResponse{Body: map[string]any{"instrumentId": request.Query.Get("instrumentId"), "interval": request.Query.Get("interval"), "candles": []any{}}}, nil
	case AuthorityFundingHistory:
		return AuthorityResponse{Body: map[string]any{"instrumentId": request.Query.Get("instrumentId"), "points": []any{}}}, nil
	default:
		return AuthorityResponse{}, AuthorityError{Status: http.StatusInternalServerError, Code: "UNEXPECTED_OPERATION"}
	}
}

func TestBrowserMarketAdapterResolvesCanonicalIdentityInsideGateway(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	authority := &browserMarketAuthority{now: now}
	handler, err := NewInternalHandler(InternalOptions{
		Authority: authority, Authenticate: func(*http.Request) error { return nil }, Now: func() time.Time { return now },
	})
	if err != nil {
		t.Fatal(err)
	}

	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/browser/market-data?instrumentId=binance:live:perp:USDT:BTC-USDT", nil))
	if response.Code != http.StatusOK || !strings.Contains(response.Body.String(), `"markPrice":"100"`) {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	if len(authority.requests) != 2 || authority.requests[0].Operation != AuthorityCatalogInstruments || authority.requests[1].Operation != AuthorityMarketData {
		t.Fatalf("requests=%+v", authority.requests)
	}
	query := authority.requests[1].Query
	if query.Get("venue") != "binance" || query.Get("environment") != "live" || query.Get("product") != "perp" || query.Get("instrumentId") != "binance:live:perp:USDT:BTC-USDT" {
		t.Fatalf("resolved query=%v", query)
	}
}

func TestBrowserMarketAdapterKeepsBrowserContractAndRejectsMixedIdentity(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 0, time.UTC)
	authority := &browserMarketAuthority{now: now}
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}

	for _, test := range []struct {
		path string
		want int
		body string
	}{
		{"/internal/v1/browser/catalog/instruments", http.StatusOK, `"projection":{"observedAt":"2026-08-26T12:00:00Z","revision":"gateway-7"}`},
		{"/internal/v1/browser/catalog/venues", http.StatusOK, `"venue":"binance"`},
		{"/internal/v1/browser/candles?instrumentId=binance:live:perp:USDT:BTC-USDT&interval=1m&limit=500", http.StatusOK, `"interval":"1m"`},
		{"/internal/v1/browser/candles?instrumentId=binance:live:perp:USDT:BTC-USDT&interval=1m&limit=501", http.StatusBadRequest, "VALIDATION_ERROR"},
		{"/internal/v1/browser/market-data?instrumentId=binance:live:perp:USDT:BTC-USDT&venue=binance", http.StatusBadRequest, "VALIDATION_ERROR"},
		{"/internal/v1/browser/market-data?instrumentId=missing", http.StatusNotFound, "MARKET_INSTRUMENT_UNKNOWN"},
	} {
		response := httptest.NewRecorder()
		handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, test.path, nil))
		if response.Code != test.want || !strings.Contains(response.Body.String(), test.body) {
			t.Errorf("GET %s = %d %s, want %d containing %q", test.path, response.Code, response.Body.String(), test.want, test.body)
		}
	}
}

func TestBrowserFundingWindowIsTranslatedOnceAtGatewayBoundary(t *testing.T) {
	now := time.Date(2026, 8, 26, 12, 0, 0, 987654321, time.UTC)
	authority := &browserMarketAuthority{now: now}
	handler, err := NewInternalHandler(InternalOptions{Authority: authority, Authenticate: func(*http.Request) error { return nil }, Now: func() time.Time { return now }})
	if err != nil {
		t.Fatal(err)
	}
	response := httptest.NewRecorder()
	handler.ServeHTTP(response, httptest.NewRequest(http.MethodGet, "/internal/v1/browser/funding-history?instrumentId=binance:live:perp:USDT:BTC-USDT&window=7d", nil))
	if response.Code != http.StatusOK {
		t.Fatalf("status=%d body=%s", response.Code, response.Body.String())
	}
	request := authority.requests[len(authority.requests)-1]
	if request.Operation != AuthorityFundingHistory || request.Query.Get("since") != "2026-08-19T12:00:00Z" || request.Query.Get("until") != "2026-08-26T12:00:00Z" {
		t.Fatalf("funding query=%v", request.Query)
	}
}
