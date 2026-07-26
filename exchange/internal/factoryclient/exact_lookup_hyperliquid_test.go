package factoryclient

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"strings"
	"sync/atomic"
	"testing"

	"github.com/QuantProcessing/boltertrader/exchange"
)

type hyperliquidExactLookupTransport struct {
	found bool
	coin  string
	calls atomic.Int64
}

func (transport *hyperliquidExactLookupTransport) RoundTrip(request *http.Request) (*http.Response, error) {
	transport.calls.Add(1)
	var payload map[string]any
	if err := json.NewDecoder(request.Body).Decode(&payload); err != nil {
		return nil, err
	}
	if payload["type"] != "orderStatus" {
		return &http.Response{
			StatusCode: http.StatusBadRequest,
			Header:     make(http.Header),
			Body:       io.NopCloser(strings.NewReader(`{"error":"unexpected query"}`)),
		}, nil
	}
	if !transport.found {
		return openAPIJSONResponse(`{"status":"unknownOid"}`), nil
	}
	oid := int64(41)
	cloid := "0x00000000000000000000000000000065"
	if numeric, ok := payload["oid"].(float64); ok {
		oid = int64(numeric)
	}
	if native, ok := payload["oid"].(string); ok {
		cloid = native
	}
	coin := transport.coin
	return openAPIJSONResponse(`{"status":"order","order":{"order":{"coin":"` + coin +
		`","side":"B","limitPx":"100","sz":"0.5","oid":` + int64String(oid) +
		`,"cloid":"` + cloid +
		`","timestamp":1720000000000,"origSz":"1","reduceOnly":false,"orderType":"Limit","tif":"Gtc","isTrigger":false,"triggerPx":"0"},"status":"open","statusTimestamp":1720000001000}}`), nil
}

func TestHyperliquidExactOrderLookupUsesAuthoritativeOrderStatus(t *testing.T) {
	for _, product := range []exchange.Product{exchange.ProductSpot, exchange.ProductPerp} {
		for _, identity := range []struct {
			name    string
			request func(exchange.Instrument) exchange.ExactOrderLookupRequest
		}{
			{
				name: "client-id",
				request: func(instrument exchange.Instrument) exchange.ExactOrderLookupRequest {
					return exchange.ExactOrderLookupRequest{Instrument: &instrument, ClientOrderID: "101"}
				},
			},
			{
				name: "venue-id",
				request: func(instrument exchange.Instrument) exchange.ExactOrderLookupRequest {
					return exchange.ExactOrderLookupRequest{Instrument: &instrument, VenueOrderID: "41"}
				},
			},
		} {
			t.Run(string(product)+"/"+identity.name, func(t *testing.T) {
				client, instrument, transport := newHyperliquidExactLookupClient(t, product)
				transport.found = true
				result, err := client.LookupExactOrder(context.Background(), identity.request(instrument))
				if err != nil {
					t.Fatalf("LookupExactOrder: %v", err)
				}
				if result.Status != exchange.ExactOrderFound || result.Order.OrderID != "41" || result.Order.ClientOrderID != "101" {
					t.Fatalf("exact result = %+v", result)
				}
				if result.Order.Status != "open" || !result.Evidence.Authoritative || !result.Evidence.Complete {
					t.Fatalf("exact evidence = %+v", result)
				}
			})
		}
	}
}

func TestHyperliquidExactOrderLookupMapsUnknownOIDToAuthoritativeNotFound(t *testing.T) {
	client, instrument, _ := newHyperliquidExactLookupClient(t, exchange.ProductSpot)
	result, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{
		Instrument:   &instrument,
		VenueOrderID: "41",
	})
	if err != nil {
		t.Fatalf("LookupExactOrder: %v", err)
	}
	if result.Status != exchange.ExactOrderNotFound || !result.Evidence.Authoritative || !result.Evidence.Complete {
		t.Fatalf("not-found result = %+v", result)
	}
}

func TestHyperliquidExactOrderLookupRejectsInvalidRequestBeforeIO(t *testing.T) {
	client, _, transport := newHyperliquidExactLookupClient(t, exchange.ProductSpot)
	_, err := client.LookupExactOrder(context.Background(), exchange.ExactOrderLookupRequest{ClientOrderID: "101"})
	if err == nil {
		t.Fatal("lookup without instrument succeeded")
	}
	if transport.calls.Load() != 0 {
		t.Fatalf("invalid lookup performed %d network calls", transport.calls.Load())
	}
}

func newHyperliquidExactLookupClient(
	t *testing.T,
	product exchange.Product,
) (exchange.ExactOrderLookup, exchange.Instrument, *hyperliquidExactLookupTransport) {
	t.Helper()
	transport := &hyperliquidExactLookupTransport{coin: "@1"}
	instrument := hyperliquidCatalogInstrument(exchange.ProductSpot, "PURR-USDC", "@1", "10001", "", 3)
	scope := exchange.CatalogScope{
		Venue:       exchange.VenueHyperliquid,
		Environment: exchange.EnvironmentTestnet,
		Product:     product,
	}
	if product == exchange.ProductPerp {
		instrument = hyperliquidCatalogInstrument(exchange.ProductPerp, "BTC-USDC", "BTC", "0", "USDC", 3)
		scope.Settlement = "USDC"
		transport.coin = "BTC"
	}
	settings := Settings{
		Endpoint:          "https://exact.invalid",
		Environment:       "testnet",
		HTTPClient:        &http.Client{Transport: transport},
		InstrumentCatalog: exchange.NewMemoryCatalog(exchange.StaticInstrumentLoader{Snapshot: exchange.InstrumentSnapshot{SchemaVersion: 1, Scope: scope, Instruments: []exchange.Instrument{instrument}}}, nil),
		CatalogScope:      scope,
	}
	var raw any
	if product == exchange.ProductPerp {
		raw = NewHyperliquidPerp(openAPITestPrivateKey, settings)
	} else {
		raw = NewHyperliquidSpot(openAPITestPrivateKey, settings)
	}
	client, ok := raw.(exchange.ExactOrderLookup)
	if !ok {
		t.Fatalf("%T does not implement ExactOrderLookup", raw)
	}
	return client, instrument, transport
}
