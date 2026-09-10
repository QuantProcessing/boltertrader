package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/services/trading/catalog"
	"github.com/QuantProcessing/boltertrader/services/trading/ledger"
	"github.com/QuantProcessing/boltertrader/services/trading/manual"
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
)

func TestManualOrderReconstructsAcrossInstances(t *testing.T) {
	commandService := newCommandService(orders.NewMemoryStore(), time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	gateway := &manualGatewayFixture{}

	newInstance := func() *http.ServeMux {
		projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		err = RegisterTradingRoutes(mux, TradingOptions{
			Commands:          commandService,
			Portfolio:         portfolioProjection,
			Ledger:            ledgerService,
			Catalog:           projection,
			Manual:            newManualService(commandService, portfolioProjection, ledgerService, gateway, time.Now, projection),
			Principal:         func(*http.Request) (string, error) { return "principal-a", nil },
			AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	commandJSON := `{"action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"manual-restart-1","order":{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"limit","quantity":"2","limitPrice":"1","timeInForce":"gtc","reduceOnly":false,"leverage":1}}`
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(commandJSON))
	commandRequest.Header.Set("Idempotency-Key", "manual-restart-1")
	commandResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(commandResponse, commandRequest)
	var commandBody struct {
		Data struct {
			CommandID     string  `json:"commandId"`
			ReservationID *string `json:"reservationId"`
		} `json:"data"`
	}
	if commandResponse.Code != http.StatusAccepted || json.Unmarshal(commandResponse.Body.Bytes(), &commandBody) != nil || commandBody.Data.CommandID == "" || commandBody.Data.ReservationID != nil {
		t.Fatalf("command=%d body=%s", commandResponse.Code, commandResponse.Body.String())
	}
	storedCommand, err := commandService.Get(context.Background(), commandBody.Data.CommandID)
	if err != nil {
		t.Fatal(err)
	}
	var intent map[string]any
	if err := json.Unmarshal(storedCommand.Payload, &intent); err != nil {
		t.Fatal(err)
	}
	if got := intent["runtimeId"]; got != "runtime-1" {
		t.Fatalf("durable Manual intent runtimeId=%v, want runtime-1; payload=%s", got, storedCommand.Payload)
	}
	for _, obsolete := range []string{"desiredStateRevision", "runtimeGeneration", "credentialVersion"} {
		if _, exists := intent[obsolete]; exists {
			t.Fatalf("durable Manual intent still contains obsolete %s; payload=%s", obsolete, storedCommand.Payload)
		}
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(commandJSON))
	replayRequest.Header.Set("Idempotency-Key", "manual-restart-1")
	replayResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(replayResponse, replayRequest)
	if replayResponse.Code != http.StatusOK || gateway.calls != 1 {
		t.Fatalf("replay=%d calls=%d body=%s", replayResponse.Code, gateway.calls, replayResponse.Body.String())
	}

	ordersResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(ordersResponse, httptest.NewRequest(http.MethodGet, "/api/v1/products/manual/orders", nil))
	if ordersResponse.Code != http.StatusOK || !strings.Contains(ordersResponse.Body.String(), commandBody.Data.CommandID) {
		t.Fatalf("orders after restart=%d body=%s", ordersResponse.Code, ordersResponse.Body.String())
	}
	orderResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(orderResponse, httptest.NewRequest(http.MethodGet, "/api/v1/products/manual/orders/"+commandBody.Data.CommandID, nil))
	if orderResponse.Code != http.StatusOK || !strings.Contains(orderResponse.Body.String(), `"state":"filled"`) {
		t.Fatalf("order after restart=%d body=%s", orderResponse.Code, orderResponse.Body.String())
	}
	positionsResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(positionsResponse, httptest.NewRequest(http.MethodGet, "/api/v1/positions?accountId=account-a&productId=manual", nil))
	if positionsResponse.Code != http.StatusOK || !strings.Contains(positionsResponse.Body.String(), `"quantity":"2"`) {
		t.Fatalf("positions after restart=%d body=%s", positionsResponse.Code, positionsResponse.Body.String())
	}
	ledgerResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(ledgerResponse, httptest.NewRequest(http.MethodGet, "/api/v1/ledger?accountId=account-a&productId=manual", nil))
	if ledgerResponse.Code != http.StatusOK || !strings.Contains(ledgerResponse.Body.String(), `"entryId":"gateway-fill-1:fee"`) {
		t.Fatalf("ledger after restart=%d body=%s", ledgerResponse.Code, ledgerResponse.Body.String())
	}
}

type manualCatalogSource struct {
	revision string
	unready  bool
	now      func() time.Time
}

type staticManualAccounts struct {
	account manual.Account
}

func (accounts *staticManualAccounts) Get(context.Context, string, string) (manual.Account, error) {
	return accounts.account, nil
}

func readyManualAccount() manual.Account {
	return manual.Account{
		ID: "account-a", Venue: "binance", VenueEnvironment: "live",
		ProductIDs: []string{"manual"}, RuntimeID: "runtime-1", State: "active",
		Routes: []manual.AccountRoute{{RouteID: "perp", Product: "perp"}},
	}
}

func TestManualCancelTransitionsOwnedRestingOrderWithoutNewCommand(t *testing.T) {
	commandStore := orders.NewMemoryStore()
	commandService := newCommandService(commandStore, time.Now)
	gateway := &manualGatewayFixture{}
	projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Commands:  commandService,
		Portfolio: portfolio.NewProjection(),
		Ledger:    ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Catalog:   projection,
		Manual: newManualService(
			commandService,
			portfolio.NewProjection(),
			ledger.NewService(ledger.NewMemoryStore(), time.Now),
			gateway,
			time.Now,
		),
		Principal:         func(*http.Request) (string, error) { return "principal-a", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	command, _, err := commandService.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-a",
		IdempotencyKey: "manual-resting-a", Method: http.MethodPost, Path: "/api/v1/products/manual/commands",
		CommandKind: "manual.place",
		Payload:     []byte(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"1","orderType":"limit","limitPrice":"50","timeInForce":"gtc"}`),
	})
	if err != nil {
		t.Fatal(err)
	}
	command, err = commandService.Transition(context.Background(), orders.TransitionRequest{
		CommandID: command.CommandID, State: orders.StateSubmitted,
		Evidence: orders.ExecutionEvidence{CommandID: command.CommandID, GatewayOrderID: "gateway-order-a", ClientOrderID: "client-order-1", State: "submitted", ResourceVersion: 1},
	})
	if err != nil {
		t.Fatal(err)
	}
	wrongLineageRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"action":"cancel","lineageId":"lineage-b","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-wrong-lineage"}`))
	wrongLineageRequest.Header.Set("Idempotency-Key", "manual-cancel-wrong-lineage")
	wrongLineageResponse := httptest.NewRecorder()
	mux.ServeHTTP(wrongLineageResponse, wrongLineageRequest)
	commandsAfterWrongLineage, commandErr := commandStore.ListOwned(context.Background(), "principal-a", "manual")
	if wrongLineageResponse.Code != http.StatusNotFound || commandErr != nil || len(commandsAfterWrongLineage) != 1 || gateway.cancelCalls != 0 {
		t.Fatalf("wrong-lineage cancel=%d commands=%+v Gateway calls=%d err=%v body=%s", wrongLineageResponse.Code, commandsAfterWrongLineage, gateway.cancelCalls, commandErr, wrongLineageResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"action":"cancel","lineageId":"lineage-a","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-a"}`))
	request.Header.Set("Idempotency-Key", "manual-cancel-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	commandsAfter, commandErr := commandStore.ListOwned(context.Background(), "principal-a", "manual")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"state":"canceled"`) || commandErr != nil || len(commandsAfter) != 1 || gateway.calls != 0 || gateway.cancelCalls != 1 {
		t.Fatalf("cancel=%d commands=%+v gateway calls=%d/%d command err=%v body=%s", response.Code, commandsAfter, gateway.calls, gateway.cancelCalls, commandErr, response.Body.String())
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"action":"cancel","lineageId":"lineage-a","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-a"}`))
	replayRequest.Header.Set("Idempotency-Key", "manual-cancel-a")
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK || gateway.cancelCalls != 1 {
		t.Fatalf("cancel replay=%d Gateway calls=%d body=%s", replay.Code, gateway.cancelCalls, replay.Body.String())
	}
}

func TestManualReduceOnlyOrderUsesDirectPlaceCommand(t *testing.T) {
	commandService := newCommandService(orders.NewMemoryStore(), time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	gateway := &manualGatewayFixture{}
	projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService,
		Catalog: projection, Manual: newManualService(commandService, portfolioProjection, ledgerService, gateway, time.Now, projection),
		Principal: func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"action":"place","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"manual-close-a","order":{"accountId":"account-a","instrumentId":"BTC-USDT","side":"sell","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"GTC","reduceOnly":true,"leverage":0}}`))
	request.Header.Set("Idempotency-Key", "manual-close-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	if response.Code != http.StatusAccepted || gateway.calls != 1 {
		t.Fatalf("close=%d calls=%d body=%s", response.Code, gateway.calls, response.Body.String())
	}
	if request := gateway.lastRequest; request.Side != "sell" || request.Quantity != "2" || request.OrderType != "market" || !request.ReduceOnly || request.InstrumentID != "BTC-USDT" {
		t.Fatalf("reduce-only Gateway request=%+v", request)
	}
}

func (source manualCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	revision := source.revision
	if revision == "" {
		revision = "manual-test"
	}
	observedAt := time.Now()
	if source.now != nil {
		observedAt = source.now()
	}
	return catalog.Snapshot{
		Revision: revision, ObservedAt: observedAt,
		Venues:      []catalog.Venue{{Venue: "binance", Environment: "live", Ready: !source.unready}},
		Instruments: []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "live", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: !source.unready}},
	}, nil
}

func TestManualCommandRejectsUnreadyCatalogBeforeCommand(t *testing.T) {
	commandService := newCommandService(orders.NewMemoryStore(), time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	gateway := &manualGatewayFixture{}

	newInstance := func(unready bool) *http.ServeMux {
		source := manualCatalogSource{revision: "catalog-current", unready: unready}
		projection, err := catalog.NewProjection(source, time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		err = RegisterTradingRoutes(mux, TradingOptions{Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService,
			Catalog: projection, Manual: newManualService(commandService, portfolioProjection, ledgerService, gateway, time.Now, projection),
			Principal: func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"unready-route","order":{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"leverage":1}}`))
	request.Header.Set("Idempotency-Key", "unready-route")
	response := httptest.NewRecorder()
	newInstance(true).ServeHTTP(response, request)
	if response.Code != http.StatusNotFound || gateway.calls != 0 {
		t.Fatalf("unready response=%d calls=%d body=%s", response.Code, gateway.calls, response.Body.String())
	}

}

func TestManualPreviewRouteIsRemoved(t *testing.T) {
	for _, route := range Routes() {
		if strings.Contains(route, "/api/v1/products/manual/execution-previews") {
			t.Fatalf("removed preview route is still published: %s", route)
		}
	}
}
