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

	gatewayhttp "github.com/QuantProcessing/boltertrader/gateway/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	tradinghttp "github.com/QuantProcessing/boltertrader/services/trading/httpapi"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/marketdata"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
)

func TestGatewayAdapterPreservesCompleteBrowserRuntimeLifecycle(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(ModeHeader, "test")
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"data":{"accountId":"account-a","tombstoned":false,"desiredStateRevision":7,"runtimeGeneration":3,"credentialVersion":2,"runtimeVersion":9,"readiness":{"ready":true},"writeReadiness":{"ready":true},"lastAppliedDesiredStateRevision":7,"lastAppliedRuntimeGeneration":3,"blockedReason":null,"active":{"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c33","desiredStateRevision":7,"runtimeGeneration":3,"credentialVersion":2,"requestedState":"active","routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active","ready":true}]},"pending":{"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c34","desiredStateRevision":8,"runtimeGeneration":4,"credentialVersion":3,"requestedState":"rotating","state":"candidate_ready","routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active","ready":false,"reason":"building"}]},"retiring":[{"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c35","desiredStateRevision":6,"runtimeGeneration":2,"credentialVersion":1,"requestedState":"draining","routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"disabled","ready":false,"reason":"draining"}]}],"capabilities":[{"rowId":"binance:perp:USDT","venue":"binance","product":"perp","settlement":"USDT","writeReady":true,"operations":{"cancel":true}}],"observedAt":"2026-08-05T01:03:04Z"}}`))
	}))
	defer server.Close()
	adapter, err := NewClientForMode(server.URL, []byte(token), orders.ModeTest, false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	items, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "test", "account-a")
	if err != nil || len(items) != 1 {
		t.Fatalf("items=%+v err=%v", items, err)
	}
	item := items[0]
	if item.Active == nil || item.Pending == nil || len(item.Retiring) != 1 || len(item.Active.Routes) != 1 || item.Pending.State != "candidate_ready" || item.Retiring[0].CredentialVersion != 1 {
		t.Fatalf("incomplete lifecycle projection: %+v", item)
	}
	encoded, err := json.Marshal(item)
	if err != nil {
		t.Fatal(err)
	}
	for _, forbidden := range []string{"operationId", "secretRef", "provider", "token"} {
		if strings.Contains(string(encoded), forbidden) {
			t.Fatalf("runtime projection leaked %q: %s", forbidden, encoded)
		}
	}
}

func TestGatewayAdapterUsesDurablePendingOperationStates(t *testing.T) {
	for _, state := range []string{"prepared", "candidate_ready", "candidate_failed", "applied"} {
		if !validPendingRuntimeState(state) {
			t.Errorf("durable operation state %q rejected", state)
		}
	}
	for _, state := range []string{"building", "ready", "blocked", "failed"} {
		if validPendingRuntimeState(state) {
			t.Errorf("candidate observation state %q accepted as operation state", state)
		}
	}
}

func TestGatewayAdapterAcceptsPendingOnlyAndTombstonedRuntimeShapes(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	tests := []struct {
		name, response string
		wantPending    bool
	}{
		{name: "pending only", wantPending: true, response: `{"data":{"accountId":"account-a","tombstoned":false,"readiness":{"ready":false,"reason":"candidate_not_ready"},"writeReadiness":{"ready":false,"reason":"candidate_not_ready"},"blockedReason":"candidate_not_ready","capabilities":[],"retiring":[],"pending":{"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c34","desiredStateRevision":1,"runtimeGeneration":1,"credentialVersion":1,"requestedState":"creating","state":"prepared","routes":[]}}}`},
		{name: "tombstoned", response: `{"data":{"accountId":"account-a","tombstoned":true,"readiness":{"ready":false,"reason":"account_retired"},"writeReadiness":{"ready":false,"reason":"account_retired"},"blockedReason":"account_retired","capabilities":[],"retiring":[]}}`},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				w.Header().Set(ModeHeader, "test")
				w.Header().Set("Content-Type", "application/json")
				_, _ = w.Write([]byte(test.response))
			}))
			defer server.Close()
			adapter, err := NewClientForMode(server.URL, []byte(token), orders.ModeTest, false, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			items, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "test", "account-a")
			if err != nil || len(items) != 1 || (items[0].Pending != nil) != test.wantPending || items[0].Active != nil {
				t.Fatalf("items=%+v err=%v", items, err)
			}
		})
	}
}

type adapterContractAuthority struct {
	requests []gatewayhttp.AuthorityRequest
}

func (authority *adapterContractAuthority) Execute(_ context.Context, request gatewayhttp.AuthorityRequest) (gatewayhttp.AuthorityResponse, error) {
	authority.requests = append(authority.requests, request)
	switch request.Operation {
	case gatewayhttp.AuthorityRuntimeReadiness:
		return gatewayhttp.AuthorityResponse{Body: gatewayhttp.RuntimeReadiness{Ready: true}}, nil
	case gatewayhttp.AuthorityCatalogVenues:
		return gatewayhttp.AuthorityResponse{Body: []catalog.Venue{{Venue: "binance", Environment: "demo", Ready: true}}}, nil
	case gatewayhttp.AuthorityCatalogInstruments:
		return gatewayhttp.AuthorityResponse{Body: map[string]any{"revision": "gateway-catalog-1", "observedAt": time.Date(2026, 8, 5, 1, 0, 0, 0, time.UTC), "instruments": []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: true}}}}, nil
	case gatewayhttp.AuthorityMarketData:
		if request.Query.Get("venue") != "binance" || request.Query.Get("environment") != "demo" || request.Query.Get("product") != "perp" || request.Query.Get("instrumentId") != "BTC-USDT" {
			return gatewayhttp.AuthorityResponse{}, errors.New("unexpected market-data query")
		}
		return gatewayhttp.AuthorityResponse{Body: marketdata.Fact{
			Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "BTC-USDT", MarkPrice: "100.05",
			ObservedAt: time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC),
		}}, nil
	case gatewayhttp.AuthorityGetRuntimeAccount:
		if request.AccountID != "account-a" {
			return gatewayhttp.AuthorityResponse{}, errors.New("unexpected runtime account")
		}
		return gatewayhttp.AuthorityResponse{Body: map[string]any{
			"accountId": "account-a", "tombstoned": false, "desiredStateRevision": 7, "runtimeGeneration": 3, "credentialVersion": 2, "runtimeVersion": 9,
			"readiness": map[string]any{"ready": true}, "writeReadiness": map[string]any{"ready": true},
			"lastAppliedDesiredStateRevision": 7, "lastAppliedRuntimeGeneration": 3, "blockedReason": nil,
			"active":       map[string]any{"operationId": "018f47a8-2578-7d3c-9d7d-5e4f4a445c33", "desiredStateRevision": 7, "runtimeGeneration": 3, "credentialVersion": 2, "requestedState": "active", "routes": []any{}},
			"retiring":     []any{},
			"capabilities": []map[string]any{{"rowId": "binance:perp:USDT", "venue": "binance", "product": "perp", "settlement": "USDT", "writeReady": true, "operations": map[string]any{"place": true, "cancel": true, "fills": true, "positions": true}}},
			"observedAt":   time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC),
		}}, nil
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

	unauthorized, err := newClient(server.URL, []byte(strings.Repeat("x", 32)), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := unauthorized.Fetch(context.Background()); err == nil || rejected != 1 || len(authority.requests) != 0 {
		t.Fatalf("unauthorized Fetch() err=%v rejected=%d authority requests=%d", err, rejected, len(authority.requests))
	}

	adapter, err := newClient(server.URL, []byte(token), server.Client())
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
	fact, err := adapter.MarketData().Fetch(context.Background(), marketdata.Query{Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "BTC-USDT"})
	if err != nil || fact.MarkPrice != "100.05" || !fact.ObservedAt.Equal(time.Date(2026, 8, 5, 1, 2, 3, 0, time.UTC)) {
		t.Fatalf("fact=%+v err=%v", fact, err)
	}
	runtimes, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "test", "account-a")
	if err != nil || len(runtimes) != 1 || runtimes[0].RuntimeVersion != 9 || runtimes[0].LastAppliedDesiredStateRevision != 7 || !runtimes[0].ObservedAt.Equal(time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC)) {
		t.Fatalf("runtimes=%+v err=%v", runtimes, err)
	}
	wantOperations := []gatewayhttp.AuthorityOperation{
		gatewayhttp.AuthorityCatalogInstruments,
		gatewayhttp.AuthorityRuntimeReadiness,
		gatewayhttp.AuthorityCatalogVenues,
		gatewayhttp.AuthorityCatalogInstruments,
		gatewayhttp.AuthorityMarketData,
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
			_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"demo","ready":true}]}`))
		case "/internal/v1/catalog/instruments":
			_, _ = w.Write([]byte(`{"data":{"revision":"gateway-catalog-old","observedAt":"2026-08-04T01:00:00Z","instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"demo","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":true}]}}`))
		case "/internal/v1/market-data?environment=demo&instrumentId=BTC-USDT&product=perp&venue=binance":
			_, _ = w.Write([]byte(`{"data":{"venue":"binance","environment":"demo","product":"perp","instrumentId":"BTC-USDT","markPrice":"100.05","observedAt":"2026-08-05T01:02:03Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
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
	if _, err := projection.Snapshot(context.Background()); err == nil {
		t.Fatal("old Gateway catalog became fresh after Platform fetch")
	}
	fact, err := adapter.MarketData().Fetch(context.Background(), marketdata.Query{Venue: "binance", Environment: "demo", Product: "perp", InstrumentID: "BTC-USDT"})
	if err != nil || fact.MarkPrice != "100.05" {
		t.Fatalf("fact=%+v err=%v", fact, err)
	}
}

func TestGatewayAdapterMarketDataProbeRequiresCatalogIdentityAndReadyInstrument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"revision":"","observedAt":"0001-01-01T00:00:00Z","instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"demo","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":true}]}}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte("gateway-adapter-token-with-at-least-32-bytes"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("ready instrument without catalog identity was accepted")
	}
}

func TestGatewayAdapterMarketDataProbeAcceptsAuthoritativeEmptyCatalog(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"revision":"gateway-empty-catalog","observedAt":"2026-08-11T00:00:00Z","instruments":[]}}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte("gateway-adapter-token-with-at-least-32-bytes"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err != nil {
		t.Fatalf("authoritative empty catalog was not accepted as idle-ready: %v", err)
	}
}

func TestGatewayAdapterMarketDataProbeRejectsMissingEmptyCatalogInventory(t *testing.T) {
	for _, test := range []struct {
		name        string
		instruments string
	}{
		{name: "omitted"},
		{name: "null", instruments: `,"instruments":null`},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
				_, _ = fmt.Fprintf(w, `{"data":{"revision":"gateway-empty-catalog","observedAt":"2026-08-11T00:00:00Z"%s}}`, test.instruments)
			}))
			defer server.Close()
			adapter, err := newClient(server.URL, []byte("gateway-adapter-token-with-at-least-32-bytes"), server.Client())
			if err != nil {
				t.Fatal(err)
			}
			if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
				t.Fatalf("%s instruments inventory was accepted", test.name)
			}
		})
	}
}

func TestGatewayAdapterMarketDataProbeRejectsWhitespaceEmptyCatalogRevision(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"revision":" ","observedAt":"2026-08-11T00:00:00Z","instruments":[]}}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte("gateway-adapter-token-with-at-least-32-bytes"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("empty catalog with a whitespace revision was accepted")
	}
}

func TestGatewayAdapterMarketDataProbeRejectsNonEmptyCatalogWithoutReadyInstrument(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte(`{"data":{"revision":"gateway-catalog-unready","observedAt":"2026-08-11T00:00:00Z","instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"demo","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":false}]}}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte("gateway-adapter-token-with-at-least-32-bytes"), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if err := adapter.CheckMarketDataRoute(context.Background()); err == nil {
		t.Fatal("non-empty catalog without a ready instrument was accepted")
	}
}

func TestConfiguredGatewayAdapterProjectsRuntimeCapabilitiesWithoutBrowserGatewayAccess(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	requests := make([]string, 0, 1)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		requests = append(requests, r.URL.RequestURI())
		switch r.URL.RequestURI() {
		case "/internal/v1/accounts/runtime/account-a":
			_, _ = w.Write([]byte(`{"data":{"accountId":"account-a","tombstoned":false,"desiredStateRevision":7,"runtimeGeneration":3,"credentialVersion":2,"runtimeVersion":9,"readiness":{"ready":true},"writeReadiness":{"ready":true},"lastAppliedDesiredStateRevision":7,"lastAppliedRuntimeGeneration":3,"blockedReason":null,"active":{"operationId":"018f47a8-2578-7d3c-9d7d-5e4f4a445c33","desiredStateRevision":7,"runtimeGeneration":3,"credentialVersion":2,"requestedState":"active","routes":[]},"retiring":[],"capabilities":[{"rowId":"binance:perp:USDT","venue":"binance","product":"perp","settlement":"USDT","writeReady":true,"operations":{"place":true,"amend":false,"cancel":true,"open_orders":true,"order_history":true,"fills":true,"balances":true,"account":true,"positions":true,"funding":true,"account_funding_payments":true,"leverage":false,"private_stream":true,"margin_mode":false,"position_mode":false,"funds_transfer":false}}],"observedAt":"2026-08-05T01:03:04Z"}}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	items, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "test", "account-a")
	if err != nil || len(items) != 1 {
		t.Fatalf("ListAccountRuntimes()=%+v err=%v", items, err)
	}
	item := items[0]
	if item.AccountID != "account-a" || item.DesiredStateRevision != 7 || item.RuntimeGeneration != 3 || item.RuntimeVersion != 9 || item.LastAppliedDesiredStateRevision != 7 || item.LastAppliedRuntimeGeneration != 3 || !item.Readiness.Ready || !item.WriteReadiness.Ready || len(item.Capabilities) != 1 || !item.Capabilities[0].WriteReady || !item.Capabilities[0].Operations.Cancel || !item.Capabilities[0].Operations.Fills || !item.ObservedAt.Equal(time.Date(2026, 8, 5, 1, 3, 4, 0, time.UTC)) {
		t.Fatalf("runtime projection=%+v", item)
	}
	if _, err := adapter.ListAccountRuntimes(context.Background(), "browser-a", "live", "account-a"); err == nil {
		t.Fatal("cross-mode runtime projection reached Gateway")
	}
	if len(requests) != 1 || requests[0] != "/internal/v1/accounts/runtime/account-a" {
		t.Fatalf("Gateway projection requests=%+v", requests)
	}
	var _ tradinghttp.AccountRuntimeProjectionReader = adapter
}

func TestConfiguredGatewayAdapterRejectsInvalidReadSourceResponses(t *testing.T) {
	for _, body := range []string{
		`{"data":[{"venue":"binance","environment":"demo","ready":true,"secret":"leak"}]}`,
		`{"data":[]} {}`,
	} {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) { _, _ = w.Write([]byte(body)) }))
		adapter, err := newClient(server.URL, []byte(strings.Repeat("x", 32)), server.Client())
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
	adapter, _ := newClient(server.URL, []byte(strings.Repeat("x", 32)), server.Client())
	if _, err := adapter.Fetch(context.Background()); err == nil {
		t.Fatal("non-success response accepted")
	}
}

var _ catalog.Source = (*Client)(nil)
var _ marketdata.Source = MarketDataSource{}

func TestConfiguredGatewayAdapterHandlesManualAndNormalizedCommands(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	var mu sync.Mutex
	requests := make([]placeRequest, 0, 2)
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.URL.Path != "/v1/orders" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("request path/auth=%s/%q", r.URL.Path, r.Header.Get("Authorization"))
		}
		var body placeRequest
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil || body.IdempotencyKey != r.Header.Get("Idempotency-Key") {
			t.Errorf("request body=%+v err=%v", body, err)
		}
		mu.Lock()
		requests = append(requests, body)
		mu.Unlock()
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000001","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"submitted","resource_version":1}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	manualResult, err := adapter.Execute(context.Background(), manual.GatewayRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "binance:demo:spot:BTC-USDT",
		IdempotencyKey: "ignored-in-favor-of-command", Side: "buy", Quantity: "2", OrderType: "market", Mode: orders.ModeTest,
		DesiredStateRevision: 11, RuntimeGeneration: 4, CredentialVersion: 3,
	})
	if err != nil || manualResult.State != manual.GatewayStateSubmitted || manualResult.Handoff.Outcome != orders.HandoffAccepted || manualResult.Handoff.Evidence.GatewayOrderID == "" {
		t.Fatalf("Manual Execute() result=%+v err=%v", manualResult, err)
	}
	normalizedPayload := json.RawMessage(`{"accountId":"account-long","instrumentId":"binance:demo:perp:BTC-USDT","side":"buy","quantity":"1","orderType":"market","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3}`)
	if _, err := adapter.Handoff(context.Background(), orders.Command{CommandID: "command_child_1", Mode: orders.ModeTest, ProductID: "manual", Payload: normalizedPayload}); err != nil {
		t.Fatal(err)
	}
	mu.Lock()
	defer mu.Unlock()
	if len(requests) != 2 || requests[0].IdempotencyKey != "command_manual_1" || requests[1].IdempotencyKey != "command_child_1" {
		t.Fatalf("Gateway requests=%+v", requests)
	}
	if requests[0].DesiredStateRevision != 11 || requests[0].RuntimeGeneration != 4 || requests[0].CredentialVersion != 3 {
		t.Fatalf("Manual Gateway request lost account fence: %+v", requests[0])
	}
}

func TestConfiguredGatewayAdapterCancelsManualOrderByGatewayIdentity(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Method == http.MethodGet {
			_, _ = w.Write([]byte(`{"items":[],"next_cursor":""}`))
			return
		}
		if r.Method != http.MethodDelete || r.URL.Path != "/v1/orders/018f0000-0000-7000-8000-000000000002" || r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("cancel request=%s %s auth=%q", r.Method, r.URL.Path, r.Header.Get("Authorization"))
		}
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"command_manual_1","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"canceled","resource_version":2}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Cancel(context.Background(), manual.GatewayCancelRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "binance:demo:spot:BTC-USDT", Mode: orders.ModeTest,
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
		switch r.Method {
		case http.MethodDelete:
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"command_manual_1","gateway_order_id":"gateway-order","client_order_id":"42","state":"canceled","resource_version":3}`))
		case http.MethodGet:
			cursors = append(cursors, r.URL.Query().Get("pagination.cursor"))
			if len(cursors) == 1 {
				_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","settlement_asset":"USDT","fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.1","fee_asset":"USDT","time":"2026-08-05T00:00:00Z"}}],"next_cursor":"page-2"}`))
				return
			}
			_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","settlement_asset":"USDT","fill":{"fill_id":"fill-2","side":"buy","price":"101","quantity":"0.5","fee":"0.05","fee_asset":"USDT","time":"2026-08-05T00:00:01Z"}}],"next_cursor":""}`))
		default:
			http.Error(w, "unexpected method", http.StatusMethodNotAllowed)
		}
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}

	result, err := adapter.Cancel(context.Background(), manual.GatewayCancelRequest{
		CommandID: "command_manual_1", AccountID: "account-a", InstrumentID: "BTC-USDT", Mode: orders.ModeTest, GatewayOrderID: "gateway-order",
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
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000001","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"submitted","resource_version":1}`))
	}))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(token), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"accountId":"account-long","instrumentId":"BTC-USDT-LONG","side":"buy","quantity":"0.5","orderType":"market","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3}`)
	if _, err := adapter.Handoff(context.Background(), orders.Command{CommandID: "command_child_1", Mode: orders.ModeTest, ProductID: "manual", Payload: payload}); err != nil {
		t.Fatalf("Handoff: %v", err)
	}
	body := <-requestBody
	for key, want := range map[string]float64{"desired_state_revision": 11, "runtime_generation": 4, "credential_version": 3} {
		if body[key] != want {
			t.Fatalf("Gateway request %s=%v, want %v; body=%+v", key, body[key], want, body)
		}
	}
}

func TestConfiguredGatewayAdapterRejectsUnnormalizedPayloadBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(strings.Repeat("x", 32)), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	payload := json.RawMessage(`{"previewId":"preview-1","opportunityId":"opportunity-1","longAccountId":"account-long","shortAccountId":"account-short","canonicalMarketId":"BTC-USDT-PERP","notionalPerLeg":"50","maxSlippageBps":"10","holdThroughSettlement":true}`)
	if _, err := adapter.Handoff(context.Background(), orders.Command{CommandID: "command_parent", Mode: orders.ModeTest, ProductID: "manual", Payload: payload}); err == nil {
		t.Fatal("unnormalized product payload was accepted by Gateway adapter")
	}
	if calls != 0 {
		t.Fatalf("unnormalized payload made %d Gateway HTTP calls", calls)
	}
}

func TestConfiguredGatewayAdapterRejectsLiveBeforeHTTP(t *testing.T) {
	calls := 0
	server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
	defer server.Close()
	adapter, err := newClient(server.URL, []byte(strings.Repeat("x", 32)), server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if _, err := adapter.Execute(context.Background(), manual.GatewayRequest{Mode: orders.ModeLive}); err == nil {
		t.Fatal("Live Manual execution was accepted")
	}
	if _, err := adapter.Handoff(context.Background(), orders.Command{Mode: orders.ModeLive}); err == nil {
		t.Fatal("Live Funding handoff was accepted")
	}
	if calls != 0 {
		t.Fatalf("Live writes made %d Gateway HTTP calls", calls)
	}
}

func TestGatewayHandoffClassifiesOutcomesAndPreservesAcceptedEvidence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	valid := orders.Command{CommandID: "platform-command", AccountID: "account-a", Mode: orders.ModeTest, Payload: json.RawMessage(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"market","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3}`)}

	t.Run("not attempted", func(t *testing.T) {
		calls := 0
		server := httptest.NewServer(http.HandlerFunc(func(http.ResponseWriter, *http.Request) { calls++ }))
		defer server.Close()
		adapter, _ := newClient(server.URL, []byte(token), server.Client())
		result, err := adapter.Handoff(context.Background(), orders.Command{Mode: orders.ModeTest})
		if err == nil || result.Outcome != orders.HandoffNotAttempted || calls != 0 {
			t.Fatalf("result=%+v calls=%d err=%v", result, calls, err)
		}
	})

	t.Run("rejected", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			http.Error(w, "rejected", http.StatusUnprocessableEntity)
		}))
		defer server.Close()
		adapter, _ := newClient(server.URL, []byte(token), server.Client())
		result, err := adapter.Handoff(context.Background(), valid)
		if err == nil || result.Outcome != orders.HandoffRejected {
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
			adapter, _ := newClient(server.URL, []byte(token), server.Client())
			result, err := adapter.Handoff(context.Background(), valid)
			if err == nil || result.Outcome != orders.HandoffOutcomeUnknown {
				t.Fatalf("result=%+v err=%v", result, err)
			}
		})
	}

	t.Run("outcome unknown", func(t *testing.T) {
		client := &http.Client{Transport: roundTripFunc(func(*http.Request) (*http.Response, error) { return nil, errors.New("connection reset") })}
		adapter, _ := newClient("http://127.0.0.1:1", []byte(token), client)
		result, err := adapter.Handoff(context.Background(), valid)
		if err == nil || result.Outcome != orders.HandoffOutcomeUnknown {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("accepted", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"submitted","resource_version":7}`))
		}))
		defer server.Close()
		adapter, _ := newClient(server.URL, []byte(token), server.Client())
		result, err := adapter.Handoff(context.Background(), valid)
		if err != nil || result.Outcome != orders.HandoffAccepted || result.Evidence.CommandID != "gateway-command" || result.Evidence.GatewayOrderID != "gateway-order" || result.Evidence.ClientOrderID != "client-order" || result.Evidence.State != "submitted" || result.Evidence.ResourceVersion != 7 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})

	t.Run("accepted filled evidence", func(t *testing.T) {
		server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			if r.Method == http.MethodGet {
				_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","gateway_order_id":"gateway-order","settlement_asset":"USDT","fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.01","fee_asset":"USDT","time":"2026-08-05T00:00:00Z"}}],"next_cursor":""}`))
				return
			}
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":8}`))
		}))
		defer server.Close()
		adapter, _ := newClient(server.URL, []byte(token), server.Client())
		result, err := adapter.Handoff(context.Background(), valid)
		if err != nil || len(result.Evidence.Fills) != 1 || result.Evidence.Fills[0].FillID != "fill-1" || len(result.Evidence.OrderEvents) != 1 {
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
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":9}`))
		}))
		defer server.Close()
		adapter, _ := newClient(server.URL, []byte(token), server.Client())
		result, err := adapter.Handoff(context.Background(), valid)
		if err == nil || result.Outcome != orders.HandoffOutcomeUnknown ||
			result.Evidence.GatewayOrderID != "gateway-order" || result.Evidence.State != "filled" ||
			result.Evidence.ResourceVersion != 9 || len(result.Evidence.Fills) != 0 {
			t.Fatalf("result=%+v err=%v", result, err)
		}
	})
}

type roundTripFunc func(*http.Request) (*http.Response, error)

func (function roundTripFunc) RoundTrip(request *http.Request) (*http.Response, error) {
	return function(request)
}

func TestGatewayAdapterRequiresExactModeAttestationAndGatesLiveWrites(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	command := orders.Command{CommandID: "platform-command", AccountID: "account-a", Mode: orders.ModeLive, Payload: json.RawMessage(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"market","desiredStateRevision":11,"runtimeGeneration":4,"credentialVersion":3}`)}
	serverMode := "live"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ModeHeader) != serverMode {
			http.Error(w, "mode mismatch", http.StatusConflict)
			return
		}
		w.Header().Set(ModeHeader, serverMode)
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"submitted","resource_version":1}`))
	}))
	defer server.Close()

	denied, err := NewClientForMode(server.URL, []byte(token), orders.ModeLive, false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	if result, err := denied.Handoff(context.Background(), command); !errors.Is(err, orders.ErrLiveWriteDisabled) || result.Outcome != orders.HandoffNotAttempted {
		t.Fatalf("default Live result=%+v err=%v", result, err)
	}

	enabled, _ := NewClientForMode(server.URL, []byte(token), orders.ModeLive, true, server.Client())
	if result, err := enabled.Handoff(context.Background(), command); err != nil || result.Outcome != orders.HandoffAccepted {
		t.Fatalf("enabled Live result=%+v err=%v", result, err)
	}
	serverMode = "test"
	if result, err := enabled.Handoff(context.Background(), command); err == nil || result.Outcome != orders.HandoffOutcomeUnknown {
		t.Fatalf("cross-mode result=%+v err=%v", result, err)
	}
}

func TestGatewayAdapterRequiresExactModeAttestationOnReads(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	for _, test := range []struct {
		name         string
		responseMode string
		wantError    bool
	}{
		{name: "missing", wantError: true},
		{name: "mismatched", responseMode: "live", wantError: true},
		{name: "exact", responseMode: "test"},
	} {
		t.Run(test.name, func(t *testing.T) {
			server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
				if r.Header.Get(ModeHeader) != "test" {
					t.Errorf("request mode attestation=%q", r.Header.Get(ModeHeader))
				}
				if test.responseMode != "" {
					w.Header().Set(ModeHeader, test.responseMode)
				}
				_, _ = w.Write([]byte(`{"data":{"revision":"gateway-catalog-1","observedAt":"2026-08-09T00:00:00Z","instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"demo","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":true}]}}`))
			}))
			defer server.Close()

			adapter, err := NewClientForMode(server.URL, []byte(token), orders.ModeTest, false, server.Client())
			if err != nil {
				t.Fatal(err)
			}
			err = adapter.CheckMarketDataRoute(context.Background())
			if (err != nil) != test.wantError {
				t.Fatalf("CheckMarketDataRoute() error=%v wantError=%t", err, test.wantError)
			}
		})
	}
}

func TestGatewayAdapterReconcilesCanonicalOrderAndFillEvidence(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Header.Get(ModeHeader) != "test" {
			t.Errorf("mode=%q", r.Header.Get(ModeHeader))
		}
		w.Header().Set(ModeHeader, "test")
		switch r.URL.Path {
		case "/v1/orders/gateway-order":
			_, _ = w.Write([]byte(`{"command_id":"gateway-command","gateway_order_id":"gateway-order","client_order_id":"client-order","state":"filled","resource_version":2}`))
		case "/v1/fills":
			_, _ = w.Write([]byte(`{"items":[{"account_id":"account-a","instrument_id":"BTC-USDT","settlement_asset":"USDT","fill":{"fill_id":"fill-1","side":"buy","price":"100","quantity":"1","fee":"0.1","fee_asset":"BNB","time":"2026-08-05T00:00:00Z"}}]}`))
		default:
			http.NotFound(w, r)
		}
	}))
	defer server.Close()
	adapter, err := NewClientForMode(server.URL, []byte(token), orders.ModeTest, false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	command := orders.Command{Mode: orders.ModeTest, AccountID: "account-a", ExecutionEvidence: orders.ExecutionEvidence{CommandID: "gateway-command", GatewayOrderID: "gateway-order", ClientOrderID: "client-order", State: "submitted", ResourceVersion: 1}}
	result, err := adapter.Reconcile(context.Background(), command)
	if err != nil || result.Outcome != orders.HandoffAccepted || result.Evidence.ResourceVersion != 2 || len(result.Evidence.Fills) != 1 {
		t.Fatalf("result=%+v err=%v", result, err)
	}
	raw, err := json.Marshal(result.Evidence.Fills[0])
	if err != nil || !strings.Contains(string(raw), `"settlementCurrency":"USDT"`) || !strings.Contains(string(raw), `"feeCurrency":"BNB"`) {
		t.Fatalf("fill evidence=%s err=%v", raw, err)
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
	adapter, err := NewClientForMode(server.URL, []byte(token), orders.ModeTest, false, server.Client())
	if err != nil {
		t.Fatal(err)
	}
	result, err := adapter.Handoff(context.Background(), orders.Command{
		CommandID: "command-1", AccountID: "account-a", Mode: orders.ModeTest,
		Payload: json.RawMessage(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"market"}`),
	})
	if err == nil || result.Outcome != orders.HandoffNotAttempted || requests != 0 {
		t.Fatalf("result=%+v err=%v requests=%d", result, err, requests)
	}
}
