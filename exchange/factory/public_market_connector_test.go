package factory

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"reflect"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
	"github.com/QuantProcessing/boltertrader/routeprofile"
)

func TestPublicMarketConnectorsCoverProfilesWithoutCredentialsOrConstructionIO(t *testing.T) {
	transport := new(countingPublicMarketTransport)
	client := &http.Client{Transport: transport}
	for _, profile := range routeprofile.DefaultRegistry().Profiles() {
		connector, err := NewPublicMarketConnector(
			exchange.Venue(profile.Venue), exchange.Product(profile.Product), profile.SettlementAsset,
			EnvironmentLive, client,
		)
		if err != nil {
			t.Fatalf("%s: %v", profile.ID, err)
		}
		t.Cleanup(func() { _ = connector.Close() })
		if _, writes := any(connector).(exchange.OrderREST); writes {
			t.Fatalf("%s public connector exposes order methods", profile.ID)
		}
		if _, private := any(connector).(exchange.SpotAccountREST); private {
			t.Fatalf("%s public connector exposes spot account methods", profile.ID)
		}
		if _, private := any(connector).(exchange.PerpAccountREST); private {
			t.Fatalf("%s public connector exposes perp account methods", profile.ID)
		}
		socket := connector.WebSocket()
		if socket == nil {
			t.Fatalf("%s public connector has no market socket", profile.ID)
		}
		wantPerp := profile.Product == routeprofile.ProductPerp
		if _, perp := socket.(exchange.PerpMarketWebSocket); perp != wantPerp {
			t.Fatalf("%s public derivative socket capability = %v, want %v", profile.ID, perp, wantPerp)
		}
		if _, reference := socket.(exchange.DerivativeReferenceWebSocket); reference != wantPerp {
			t.Fatalf("%s rich reference capability = %v, want %v", profile.ID, reference, wantPerp)
		}
		// Check the concrete value, not just its return interface. An account
		// socket assigned to MarketWebSocket would still expose private methods
		// through type assertions, even for a single method such as PlaceOrder.
		allowed := map[string]bool{
			"Close": true, "WatchOrderBook": true, "WatchBBO": true,
			"WatchPublicTrades": true, "WatchCandles": true,
			"WatchMarkPrice": wantPerp, "WatchFundingRate": wantPerp,
			"WatchDerivativeReference": wantPerp,
		}
		typ := reflect.TypeOf(socket)
		for index := 0; index < typ.NumMethod(); index++ {
			if method := typ.Method(index).Name; !allowed[method] {
				t.Fatalf("%s public socket exposes %s", profile.ID, method)
			}
		}
		if err := connector.Close(); err != nil {
			t.Fatalf("%s close: %v", profile.ID, err)
		}
	}
	if got := transport.requests.Load(); got != 0 {
		t.Fatalf("construction performed %d HTTP requests", got)
	}
}

func TestPublicMarketConnectorRefreshPopulatesCatalogForHyperliquidHIP3Reads(t *testing.T) {
	transport := roundTripperFunc(func(request *http.Request) (*http.Response, error) {
		var payload map[string]any
		if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
			t.Fatalf("decode request: %v", err)
		}
		switch payload["type"] {
		case "allPerpMetas":
			return publicMarketJSONResponse(`[{"universe":[{"name":"BTC","szDecimals":3,"maxLeverage":50}],"collateralToken":0},{"universe":[{"name":"xyz:WHEAT","szDecimals":2,"maxLeverage":10}],"collateralToken":0}]`), nil
		case "perpDexs":
			return publicMarketJSONResponse(`[null,{"name":"xyz","fullName":"trade xyz"}]`), nil
		case "spotMeta":
			return publicMarketJSONResponse(`{"tokens":[{"name":"USDC","szDecimals":6,"weiDecimals":6,"index":0,"isCanonical":true}],"universe":[]}`), nil
		case "perpCategories":
			return publicMarketJSONResponse(`[["BTC","crypto"],["xyz:WHEAT","commodities"]]`), nil
		case "perpConciseAnnotations":
			return publicMarketJSONResponse(`[]`), nil
		case "candleSnapshot":
			request, _ := payload["req"].(map[string]any)
			if request["coin"] != "xyz:WHEAT" {
				t.Fatalf("candle coin = %v, want xyz:WHEAT", request["coin"])
			}
			return publicMarketJSONResponse(`[{"t":1720000000000,"T":1720003599999,"s":"xyz:WHEAT","i":"1h","o":"6.1","c":"6.2","h":"6.3","l":"6.0","v":"12","n":2}]`), nil
		default:
			t.Fatalf("unexpected Hyperliquid request type %v", payload["type"])
			return nil, nil
		}
	})
	connector, err := NewPublicMarketConnector(
		exchange.VenueHyperliquid, exchange.ProductPerp, "USDC", EnvironmentLive,
		&http.Client{Transport: transport},
	)
	if err != nil {
		t.Fatal(err)
	}
	defer connector.Close()
	instruments, err := connector.Refresh(context.Background())
	if err != nil {
		t.Fatalf("Refresh: %v", err)
	}
	var wheat exchange.Instrument
	for _, instrument := range instruments {
		if instrument.Identity.Symbol == "xyz:WHEAT-USDC" {
			wheat = instrument
			break
		}
	}
	if wheat.Identity.Symbol == "" {
		t.Fatal("HIP-3 WHEAT instrument is missing")
	}
	page, err := connector.Candles(context.Background(), exchange.CandlesRequest{
		Instrument: wheat, Interval: "1h", Limit: 168,
	})
	if err != nil {
		t.Fatalf("Candles: %v", err)
	}
	if len(page.Candles) != 1 || page.Candles[0].Close.String() != "6.2" {
		t.Fatalf("candles = %+v", page.Candles)
	}
}

type countingPublicMarketTransport struct {
	requests atomic.Int64
}

type roundTripperFunc func(*http.Request) (*http.Response, error)

func (fn roundTripperFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return fn(request)
}

func publicMarketJSONResponse(body string) *http.Response {
	return &http.Response{
		StatusCode: http.StatusOK,
		Header:     http.Header{"Content-Type": []string{"application/json"}},
		Body:       io.NopCloser(strings.NewReader(body)),
	}
}

func (transport *countingPublicMarketTransport) RoundTrip(*http.Request) (*http.Response, error) {
	transport.requests.Add(1)
	return nil, context.Canceled
}
