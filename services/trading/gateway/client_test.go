package gateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"reflect"
	"strings"
	"sync"
	"testing"
	"time"

	wire "github.com/QuantProcessing/boltertrader/api/exchange-gateway/v1"
	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

type adapterContractAuthority struct {
	requests []gatewayhttp.AuthorityRequest
}

func (authority *adapterContractAuthority) Execute(_ context.Context, request gatewayhttp.AuthorityRequest) (gatewayhttp.AuthorityResponse, error) {
	authority.requests = append(authority.requests, request)
	switch request.Operation {
	case gatewayhttp.AuthorityRuntimeReadiness:
		return gatewayhttp.AuthorityResponse{Body: gatewayhttp.RuntimeReadiness{Ready: true}}, nil
	case gatewayhttp.AuthorityCatalogVenues:
		return gatewayhttp.AuthorityResponse{Body: []catalog.Venue{{Venue: "binance", Environment: "live", Ready: true}}}, nil
	case gatewayhttp.AuthorityCatalogInstruments:
		return gatewayhttp.AuthorityResponse{Body: map[string]any{"revision": "gateway-catalog-1", "observedAt": time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), "instruments": []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: true}}}}, nil
	case gatewayhttp.AuthorityGetRuntimeAccount:
		if request.AccountID != "account-a" {
			return gatewayhttp.AuthorityResponse{}, errors.New("unexpected runtime account")
		}
		return gatewayhttp.AuthorityResponse{Body: map[string]any{"accountId": "account-a", "runtimeId": "runtime-active", "connected": true, "observedAt": time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC)}}, nil
	default:
		return gatewayhttp.AuthorityResponse{}, errors.New("unexpected authority operation")
	}
}

func TestConfiguredGatewayAdapterConsumesRealInternalHandlerContract(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	authority := new(adapterContractAuthority)
	rejected := 0
	handler, err := gatewayhttp.NewInternalHandler(gatewayhttp.InternalOptions{
		Authority: authority,
		Authenticate: func(request *http.Request) error {
			if request.Header.Get("Authorization") != "Bearer "+token {
				rejected++
				return errors.New("wrong Gateway adapter bearer")
			}
			return nil
		},
	})
	if err != nil {
		t.Fatal(err)
	}
	server := httptest.NewServer(handler)
	defer server.Close()

	unauthorized, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unauthorized.Fetch(context.Background()); err == nil || rejected != 1 || len(authority.requests) != 0 {
		t.Fatalf("unauthorized Fetch() err=%v rejected=%d authority requests=%d", err, rejected, len(authority.requests))
	}

	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err != nil {
		t.Fatalf("CheckMarketDataRoute: %v", err)
	}
	if err := adapter.CheckRuntimeRoute(context.Background()); err != nil {
		t.Fatalf("CheckRuntimeRoute: %v", err)
	}
	snapshot, err := adapter.Fetch(context.Background())
	if err != nil || len(snapshot.Venues) != 1 || snapshot.Venues[0].Venue != "binance" ||
		len(snapshot.Instruments) != 1 || snapshot.Instruments[0].InstrumentID != "BTC-USDT" {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	runtimes, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "account-a")
	if err != nil || len(runtimes) != 1 || !runtimes[0].Connected || !runtimes[0].ObservedAt.Equal(time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC)) {
		t.Fatalf("runtimes=%+v err=%v", runtimes, err)
	}
	wantOperations := []gatewayhttp.AuthorityOperation{
		gatewayhttp.AuthorityCatalogVenues,
		gatewayhttp.AuthorityRuntimeReadiness,
		gatewayhttp.AuthorityCatalogVenues,
		gatewayhttp.AuthorityCatalogInstruments,
		gatewayhttp.AuthorityGetRuntimeAccount,
	}
	if len(authority.requests) != len(wantOperations) {
		t.Fatalf("authority requests=%+v", authority.requests)
	}
	for index, operation := range wantOperations {
		if authority.requests[index].Operation != operation {
			t.Fatalf("authority request[%d]=%+v want operation=%s", index, authority.requests[index], operation)
		}
	}
}

func TestConfiguredGatewayAdapterFetchesStrictAuthenticatedReadSources(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		switch r.URL.RequestURI() {
		case "/internal/v1/catalog/venues":
			_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"live","ready":true}]}`))
		case "/internal/v1/catalog/instruments":
			_, _ = w.Write([]byte(`{"data":{"revision":"gateway-catalog-old","observedAt":"2026-08-04T01:00:00Z","instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"live","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":true}]}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err != nil {
		t.Fatalf("CheckMarketDataRoute: %v", err)
	}
	snapshot, err := adapter.Fetch(context.Background())
	if err != nil || snapshot.Revision != "gateway-catalog-old" || !snapshot.ObservedAt.Equal(time.Date(2026, 8, 4, 1, 0, 0, 0, time.UTC)) || len(snapshot.Venues) != 1 || len(snapshot.Instruments) != 1 {
		t.Fatalf("snapshot=%+v err=%v", snapshot, err)
	}
	projection, err := catalog.NewProjection(adapter, time.Minute, func() time.Time { return time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC) })
	if err != nil {
		t.Fatal(err)
	}
	if stale, err := projection.Snapshot(context.Background()); err != nil || stale.Revision != "gateway-catalog-old" {
		t.Fatalf("stale Gateway catalog was not retained: snapshot=%+v err=%v", stale, err)
	}
	if status := projection.Status(); !status.Ready || status.Reason != catalog.ReasonStale {
		t.Fatalf("old Gateway catalog status=%+v, want readable stale", status)
	}
}

func TestGatewayAdapterMarketDataProbeRequiresReadyVenue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"live","ready":false}]}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("venue without a ready public route was accepted")
	}
}

func TestGatewayAdapterMarketDataProbeRejectsEmptyPublicCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[]}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("empty public catalog was accepted as market-data ready")
	}
}

func TestGatewayAdapterMarketDataProbeRejectsMissingVenueInventory(t *testing.T) {
	for _, test := range []struct {
		name string
		data string
	}{
		{name: "omitted", data: `{}`},
		{name: "null", data: `{"data":null}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprint(w, test.data)
			}))
			defer server.Close()
			adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
				t.Fatalf("%s venue inventory was accepted", test.name)
			}
		})
	}
}

func TestGatewayAdapterMarketDataProbeRejectsWhitespaceVenue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"venue":" ","environment":"live","ready":true}]}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("ready route without a venue identity was accepted")
	}
}

func TestGatewayAdapterMarketDataProbeRejectsNonEmptyCatalogWithoutReadyVenue(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"live","ready":false}]}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("non-empty venue inventory without a ready route was accepted")
	}
}

// TestGatewayAdapterReadsACatalogLargerThanAnAuthorityEnvelope pins the one
// property that distinguishes the catalog reads from every other read this
// client makes: their size is a function of how many venues the Gateway has
// brought up, not of the request. A single venue's spot book is already several
// times the envelope limit, so a shared cap does not merely truncate — the
// truncated document fails to decode, the projection reports it as invalid
// Gateway facts, and a healthy stack reads as an unready service from the
// moment its catalog stops being empty. The failure is invisible in any fixture
// small enough to be convenient, which is why the assertion here is on the wire
// size as much as on the decode.
func TestGatewayAdapterReadsACatalogLargerThanAnAuthorityEnvelope(t *testing.T) {
	const instruments = 512
	catalogBody, err := json.Marshal(wire.Envelope[struct {
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}]{Data: struct {
		Revision    string               `json:"revision"`
		ObservedAt  time.Time            `json:"observedAt"`
		Instruments []catalog.Instrument `json:"instruments"`
	}{
		Revision:    "gateway-catalog-bulk",
		ObservedAt:  time.Date(2026, 8, 13, 0, 0, 0, 0, time.UTC),
		Instruments: bulkInstruments(instruments),
	}})
	if err != nil {
		t.Fatal(err)
	}
	if int64(len(catalogBody)) <= (64 << 10) {
		t.Fatalf("catalog fixture is %d bytes, which does not exceed the %d-byte authority envelope limit it exists to exceed", len(catalogBody), (64 << 10))
	}

	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/internal/v1/catalog/venues":
			_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"live","ready":true}]}`))
		case "/internal/v1/catalog/instruments":
			_, _ = w.Write(catalogBody)
		default:
			w.WriteHeader(http.StatusNotFound)
		}
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	snapshot, err := adapter.Fetch(context.Background())
	if err != nil {
		t.Fatalf("Fetch() = %v", err)
	}
	if snapshot.Revision != "gateway-catalog-bulk" || len(snapshot.Instruments) != instruments {
		t.Fatalf("Fetch() returned revision %q with %d instruments, want %q with %d", snapshot.Revision, len(snapshot.Instruments), "gateway-catalog-bulk", instruments)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err != nil {
		t.Fatalf("CheckMarketDataRoute() = %v", err)
	}
}

func bulkInstruments(count int) []catalog.Instrument {
	built := make([]catalog.Instrument, 0, count)
	for index := range count {
		symbol := fmt.Sprintf("SYM%04d-USDT", index)
		built = append(built, catalog.Instrument{
			InstrumentID: strings.ToLower(symbol), Venue: "binance", Environment: "live", Product: "spot",
			Symbol: symbol, BaseAsset: fmt.Sprintf("SYM%04d", index), QuoteAsset: "USDT", Ready: true,
			Rules: catalog.TradeRules{PriceIncrement: "0.01", QuantityIncrement: "0.0001", MinQuantity: "0.0001", MinNotional: "5"},
		})
	}
	return built
}

func TestConfiguredGatewayAdapterAcceptsLargeMultiAssetBalancePage(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		switch r.URL.Path {
		case "/internal/v1/accounts/account-a":
			_, _ = w.Write([]byte(`{"data":{"account":{"accountId":"account-a","venue":"binance","nativeEnvironment":"live","displayName":"Account A","routes":[{"routeId":"spot","product":"spot","routeProfileId":"binance.spot.v1"}],"status":"active","lastValidatedAt":null,"updatedAt":"2026-08-20T01:00:00Z"}}}`))
		case "/internal/v1/accounts/runtime/account-a":
			_, _ = w.Write([]byte(`{"data":{"accountId":"account-a","runtimeId":"runtime-a","observedAt":"2026-08-20T01:00:00Z","connected":true}}`))
		case "/v1/balances":
			items := make([]map[string]any, 600)
			for index := range items {
				items[index] = map[string]any{
					"account_id": "account-a", "product": "spot",
					"balance": map[string]string{
						"asset":     fmt.Sprintf("ASSET-%03d-%s", index, strings.Repeat("X", 96)),
						"available": "1", "locked": "0", "total": "1",
					},
				}
			}
			_ = json.NewEncoder(w).Encode(map[string]any{"items": items, "complete": true})
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()

	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	items, err := adapter.ListAccountBalances(context.Background(), "browser-a", "account-a")
	if err != nil || len(items) != 600 || !strings.HasPrefix(items[599].Asset, "ASSET-599-") {
		t.Fatalf("ListAccountBalances() items=%d err=%v", len(items), err)
	}
}

func TestConfiguredGatewayAdapterRejectsInvalidReadSourceResponses(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"venue":"binance","environment":"live","ready":true,"secret":"leak"}]}`,
		`{"data":[]} {}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		adapter, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
		if err != nil {
			t.Fatal(err)
		}
		if _, err := adapter.Fetch(context.Background()); err == nil {
			t.Fatalf("invalid response accepted: %s", body)
		}
		server.Close()
	}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { http.Error(w, "denied", http.StatusUnauthorized) }))
	defer server.Close()
	adapter, _ := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
	if _, err := adapter.Fetch(context.Background()); err == nil {
		t.Fatal("non-success response accepted")
	}
}

var _ catalog.Source = (*Client)(nil)

func TestConfiguredGatewayAdapterHandlesManualAndNormalizedCommands(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	var mu sync.Mutex
	requests := make([]wire.PlaceOrderRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/orders" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("request path/auth=%s/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body wire.PlaceOrderRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IdempotencyKey != r.Header.Get("Idempotency-Key") {
			t.Errorf("request body=%+v err=%v", body, err)
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_ = json.NewEncoder(w).Encode(wire.Order{CommandID: "018f0000-0000-7000-8000-000000000001", GatewayOrderID: "018f0000-0000-7000-8000-000000000002", ClientOrderID: "42", State: "accepted", ResourceVersion: 1, AccountID: body.AccountID, InstrumentID: body.InstrumentID})
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	manualResult, err := adapter.Execute(context.Background(), manual.GatewayRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "binance:live:perp:USDT:BTC-USDT",
		RouteID:        "perp-usdt",
		IdempotencyKey: "ignored-in-favor-of-command", Side: "buy", Quantity: "2", OrderType: "market",
		Leverage: 5, RuntimeID: "runtime-a",
	})
	if err != nil || manualResult.State != manual.GatewayStateSubmitted || manualResult.Handoff.Outcome != orders.HandoffAccepted || manualResult.Handoff.Evidence.GatewayOrderID == "" {
		t.Fatalf("Manual Execute() result=%+v err=%v", manualResult, err)
	}
	request := manual.GatewayRequest{CommandID: "command_child_1", AccountID: "account-long", InstrumentID: "binance:live:perp:BTC-USDT", Side: "buy", Quantity: "1", OrderType: "market", RouteID: "perp", RuntimeID: "runtime-long"}
	if _, err := adapter.Execute(context.Background(), request); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].IdempotencyKey != "command_manual_1" || requests[1].IdempotencyKey != "command_child_1" {
		t.Fatalf("Gateway requests=%+v", requests)
	}
	if requests[0].RuntimeID != "runtime-a" || requests[1].RuntimeID != "runtime-long" {
		t.Fatalf("Manual Gateway request lost account fence: %+v", requests[0])
	}
	if requests[0].Leverage != 5 {
		t.Fatalf("Manual Gateway request lost leverage: %+v", requests[0])
	}
}

func TestConfiguredGatewayAdapterPreservesSafeVenueRejectionEvidence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000001","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"rejected","resource_version":1,"venue_code":"-2019","venue_message":"venue rejected order command","account_id":"account-a","instrument_id":"binance:live:perp:USDT:BTC-USDT"}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Execute(context.Background(), manual.GatewayRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "binance:live:perp:USDT:BTC-USDT",
		RouteID: "perp-usdt", Side: "buy", Quantity: "0.001", OrderType: "market", RuntimeID: "runtime-a",
	})
	evidence := result.Handoff.Evidence
	if err != nil || result.State != manual.GatewayStateDenied || evidence.VenueCode != "-2019" || evidence.VenueMessage != "venue rejected order command" {
		t.Fatalf("Execute result=%+v err=%v", result, err)
	}
}

func TestConfiguredGatewayAdapterCancelsManualOrderByGatewayIdentity(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/orders/018f0000-0000-7000-8000-000000000002" {
			_ = json.NewEncoder(w).Encode(wire.Order{AccountID: "account-a", InstrumentID: "binance:live:spot:BTC-USDT", CommandID: "gateway-command", GatewayOrderID: "018f0000-0000-7000-8000-000000000002", ClientOrderID: "42", State: "open", ResourceVersion: 1})
			return
		}
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[],"next_cursor":""}`))
			return
		}
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/orders/018f0000-0000-7000-8000-000000000002" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("cancel request=%s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000009","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"canceled","resource_version":2,"account_id":"account-a","instrument_id":"binance:live:spot:BTC-USDT"}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Cancel(context.Background(), manual.GatewayCancelRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "binance:live:spot:BTC-USDT",
		GatewayOrderID: "018f0000-0000-7000-8000-000000000002",
	})
	if err != nil || result.State != manual.GatewayStateCanceled || result.Handoff.Outcome != orders.HandoffAccepted || result.Handoff.Evidence.ResourceVersion != 2 {
		t.Fatalf("Cancel() result=%+v err=%v", result, err)
	}
}

func TestConfiguredGatewayAdapterDrainsCanceledOrderFillPages(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	var cursors []string
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet && r.URL.Path == "/v1/orders/gateway-order" {
			_ = json.NewEncoder(w).Encode(wire.Order{AccountID: "account-a", InstrumentID: "BTC-USDT", CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "42", State: "open", ResourceVersion: 1})
			return
		}
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"42","state":"canceled","resource_version":3,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
		case http.MethodGet:
			cursors = append(cursors, r.URL.Query().Get("pagination.cursor"))
			if len(cursors) == 1 {
				_, _ = w.Write([]byte(`{"items":[{"gateway_order_id":"gateway-order","account_id":"account-a","instrument_id":"BTC-USDT","product":"spot","base_asset":"BTC","settlement_asset":"USDT","fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.1","fee_asset":"USDT","time":"2026-08-05T00:00:00Z"}}],"next_cursor":"page-2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"gateway_order_id":"gateway-order","account_id":"account-a","instrument_id":"BTC-USDT","product":"spot","base_asset":"BTC","settlement_asset":"USDT","fill":{"fill_id":"fill-2","side":"buy","price":"101","quantity":"0.5","fee":"0.05","fee_asset":"USDT","time":"2026-08-05T00:00:01Z"}}],"next_cursor":""}`))
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Cancel(context.Background(), manual.GatewayCancelRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "BTC-USDT", GatewayOrderID: "gateway-order",
	})
	if err != nil || result.State != manual.GatewayStateCanceled || len(result.Fills) != 2 || result.Fills[0].FillID != "fill-1" || result.Fills[1].FillID != "fill-2" {
		t.Fatalf("Cancel() result=%+v err=%v", result, err)
	}
	if !reflect.DeepEqual(cursors, []string{"", "page-2"}) {
		t.Fatalf("fill cursors=%v", cursors)
	}
}

func TestConfiguredGatewayAdapterPropagatesExactAccountFence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	requestBody := make(chan map[string]any, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode Gateway request: %v", err)
		}
		requestBody <- body
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000001","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"accepted","resource_version":1,"account_id":"account-long","instrument_id":"BTC-USDT-LONG"}`))
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	request := manual.GatewayRequest{CommandID: "command_child_1", AccountID: "account-long", InstrumentID: "BTC-USDT-LONG", Side: "buy", Quantity: "0.5", OrderType: "market", RouteID: "perp", RuntimeID: "runtime-long"}
	if _, err := adapter.Execute(context.Background(), request); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	body := <-requestBody
	if body["runtime_id"] != "runtime-long" {
		t.Fatalf("Gateway request runtime_id=%v, want runtime-long; body=%+v", body["runtime_id"], body)
	}
}

// TestConfiguredGatewayAdapterRefusesDisabledWritesBeforeHTTP pins where the
// refusal happens, which is the half of the interlock a status-code assertion
// cannot see: no request leaves the process at all. A gate that refused after
// dispatch would still have placed the order.
//
// This was a cross-mode test — a test-mode client being handed live-mode work.
// With one mode left, the switch is the whole rule.
func TestConfiguredGatewayAdapterRefusesDisabledWritesBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(strings.Repeat("x", 32)), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(context.Background(), manual.GatewayRequest{}); !errors.Is(err, orders.ErrLiveWriteDisabled) {
		t.Fatalf("Manual execution err=%v", err)
	}
	if _, err := adapter.Cancel(context.Background(), manual.GatewayCancelRequest{}); !errors.Is(err, orders.ErrLiveWriteDisabled) {
		t.Fatalf("Manual cancel err=%v", err)
	}
	if calls != 0 {
		t.Fatalf("disabled writes made %d Gateway HTTP calls", calls)
	}
}

func TestGatewayHandoffClassifiesOutcomesAndPreservesAcceptedEvidence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	valid := manual.GatewayRequest{CommandID: "platform-command", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "1", OrderType: "market", RouteID: "perp", RuntimeID: "runtime-a"}

	t.Run("not attempted", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
		defer server.Close()
		adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
		result, err := adapter.Execute(context.Background(), manual.GatewayRequest{})
		if err == nil || result.Handoff.Outcome != orders.HandoffNotAttempted || calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "rejected", http.StatusUnprocessableEntity)
		}))
		defer server.Close()
		adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
		result, err := adapter.Execute(context.Background(), valid)
		if err == nil || result.Handoff.Outcome != orders.HandoffRejected {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	for _, test := range []struct {
		name   string
		status int
		body   string
	}{
		{name: "request timeout", status: http.StatusRequestTimeout},
		{name: "server error", status: http.StatusServiceUnavailable},
		{name: "truncated accepted evidence", status: http.StatusAccepted, body: `{"command_id":"gateway-command"}`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.WriteHeader(test.status)
				_, _ = w.Write([]byte(test.body))
			}))
			defer server.Close()
			adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
			result, err := adapter.Execute(context.Background(), valid)
			if err == nil || result.Handoff.Outcome != orders.HandoffOutcomeUnknown {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}

	t.Run("outcome unknown", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") })}
		adapter, _ := NewClient("http://127.0.0.1:1", false, []byte(token), true, client)
		result, err := adapter.Execute(context.Background(), valid)
		if err == nil || result.Handoff.Outcome != orders.HandoffOutcomeUnknown {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"accepted","resource_version":7,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
		}))
		defer server.Close()
		adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
		result, err := adapter.Execute(context.Background(), valid)
		if err != nil || result.Handoff.Outcome != orders.HandoffAccepted || result.Handoff.Evidence.CommandID != "gateway-command" || result.Handoff.Evidence.GatewayOrderID != "gateway-order" || result.Handoff.Evidence.ClientOrderID != "client-order" || result.Handoff.Evidence.State != "accepted" || result.Handoff.Evidence.ResourceVersion != 7 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("accepted filled evidence", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","gateway_order_id":"gateway-order","settlement_asset":"USDT","fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.01","fee_asset":"USDT","time":"2026-08-05T00:00:00Z"},"product":"spot","base_asset":"BTC"}],"next_cursor":""}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":8,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
		}))
		defer server.Close()
		adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
		result, err := adapter.Execute(context.Background(), valid)
		if err != nil || len(result.Handoff.Evidence.Fills) != 1 || result.Handoff.Evidence.Fills[0].FillID != "fill-1" || len(result.Handoff.Evidence.OrderEvents) != 1 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("filled evidence read failure is unknown", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				http.Error(w, "fills unavailable", http.StatusServiceUnavailable)
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":9,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
		}))
		defer server.Close()
		adapter, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
		result, err := adapter.Execute(context.Background(), valid)
		if err == nil || result.Handoff.Outcome != orders.HandoffOutcomeUnknown ||
			result.Handoff.Evidence.GatewayOrderID != "gateway-order" || result.Handoff.Evidence.State != "filled" ||
			result.Handoff.Evidence.ResourceVersion != 9 || len(result.Handoff.Evidence.Fills) != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

// TestGatewayAdapterGatesLiveWrites covers the one interlock that outlived the
// mode axis: BT_LIVE_PRIVATE_WRITES_ENABLED. A client built without it refuses
// before reaching the wire, and there is nothing but a real exchange behind the
// wire now — a write that gets through spends real money.
//
// This test used to pin an exact mode attestation as well. The client sent
// X-Bolter-Mode on every request and refused any answer that did not echo it
// back, so a test process could not be pointed at a live Gateway and a live
// process could not be pointed at a test one. Both headers went with the mode
// axis, along with the only deployment split they could have caught.
func TestGatewayAdapterGatesLiveWrites(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	command := manual.GatewayRequest{CommandID: "platform-command", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "1", OrderType: "market", RouteID: "perp", RuntimeID: "runtime-a"}
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"accepted","resource_version":1,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
	}))
	defer server.Close()

	denied, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := denied.Execute(context.Background(), command); !errors.Is(err, orders.ErrLiveWriteDisabled) || result.Handoff.Outcome != orders.HandoffNotAttempted {
		t.Fatalf("disabled result=%+v err=%v", result, err)
	}

	enabled, _ := NewClient(server.URL, false, []byte(token), true, server.Client())
	if result, err := enabled.Execute(context.Background(), command); err != nil || result.Handoff.Outcome != orders.HandoffAccepted {
		t.Fatalf("enabled result=%+v err=%v", result, err)
	}
}

func TestGatewayAdapterReconcilesCanonicalOrderAndFillEvidence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		switch r.URL.Path {
		case "/v1/orders/gateway-order":
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":2,"account_id":"account-a","instrument_id":"BTC-USDT"}`))
		case "/v1/fills":
			_, _ = w.Write([]byte(`{"items":[{"gateway_order_id":"gateway-order","account_id":"account-a","instrument_id":"BTC-USDT","product":"spot","base_asset":"BTC","settlement_asset":"USDT","fill":{"native_symbol":"BTCUSDT","native_id":"11","order_id":"venue-order","client_order_id":"1","liquidity":"taker","fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.1","fee_asset":"BNB","time":"2026-08-05T00:00:00Z"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	command := orders.Command{AccountID: "account-a", Payload: []byte(`{"instrumentId":"BTC-USDT"}`), ExecutionEvidence: orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}}
	result, err := adapter.Reconcile(context.Background(), command)
	if err != nil || result.Outcome != orders.HandoffAccepted || result.Evidence.ResourceVersion != 2 || len(result.Evidence.Fills) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, err := json.Marshal(result.Evidence.Fills[0])
	if err != nil || !strings.Contains(string(raw), `"settlementCurrency":"USDT"`) || !strings.Contains(string(raw), `"feeCurrency":"BNB"`) || result.Evidence.Fills[0].InstrumentProduct != "spot" || result.Evidence.Fills[0].BaseAsset != "BTC" {
		t.Fatalf("fill evidence=%s err=%v", raw, err)
	}
}

// New normalized fields must not stall reconciliation on existing clients.
func TestFillPageAllowsAdditionalGatewayFields(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","gateway_order_id":"order-a","settlement_asset":"USDT","product":"spot","base_asset":"BTC","future_resource":true,"fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"-0.01","fee_asset":"BNB","time":"2026-08-05T00:00:00Z","future_venue_field":{"code":7}}}],"future_page":true}`))
	}))
	defer server.Close()
	client, err := NewClient(server.URL, false, []byte("gateway-adapter-token-with-at-least-32-bytes"), false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	fills, err := client.fetchFills(t.Context(), "order-a", "account-a", "BTC-USDT")
	if err != nil || len(fills) != 1 || fills[0].Fee != "-0.01" || fills[0].FeeCurrency != "BNB" {
		t.Fatalf("fills=%+v err=%v", fills, err)
	}
}

func TestGatewayAdapterRejectsAllZeroOrderFenceBeforeHandoff(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	requests := 0
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests++
		w.WriteHeader(http.StatusAccepted)
	}))
	defer server.Close()
	adapter, err := NewClient(server.URL, false, []byte(token), true, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Execute(context.Background(), manual.GatewayRequest{CommandID: "command-1", AccountID: "account-a", InstrumentID: "BTC-USDT", Side: "buy", Quantity: "1", OrderType: "market", RouteID: "perp"})
	if err == nil || result.Handoff.Outcome != orders.HandoffNotAttempted || requests != 0 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, requests)
	}
}
