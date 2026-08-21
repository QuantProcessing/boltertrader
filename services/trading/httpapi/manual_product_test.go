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
	"github.com/QuantProcessing/boltertrader/services/trading/orders"
	"github.com/QuantProcessing/boltertrader/services/trading/portfolio"
	"github.com/QuantProcessing/boltertrader/services/trading/reservations"
)

func TestManualPreviewAndOrderReconstructAcrossInstances(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandService := newCommandService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	previewKey := []byte("shared-preview-signing-key-with-32-bytes")
	gateway := &manualGatewayFixture{}

	newInstance := func() *http.ServeMux {
		projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		err = RegisterTradingRoutes(mux, TradingOptions{
			Reservations:      reservationService,
			Commands:          commandService,
			Portfolio:         portfolioProjection,
			Ledger:            ledgerService,
			Catalog:           projection,
			Manual:            newManualService(commandService, portfolioProjection, ledgerService, gateway, time.Now, previewKey),
			ManualAccounts:    &staticManualAccounts{account: readyManualAccount()},
			Principal:         func(*http.Request) (string, error) { return "principal-a", nil },
			AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	// A step used to open here that previewed 2.01 at the fixture's 50 and
	// expected a 400: test mode capped a manual order at 100 USDT of notional,
	// and 2.01 was the first quantity over it. The cap is gone with the mode, and
	// this fixture's TradeRules set only a price/quantity increment and a minimum
	// quantity — so nothing refuses 2.01 any more and the preview simply seals.
	previewResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"limit","quantity":"2","limitPrice":"1","timeInForce":"gtc","reduceOnly":false}`)))
	var previewBody struct {
		Data struct {
			PreviewID string `json:"previewId"`
		} `json:"data"`
	}
	if previewResponse.Code != http.StatusCreated || json.Unmarshal(previewResponse.Body.Bytes(), &previewBody) != nil || previewBody.Data.PreviewID == "" {
		t.Fatalf("preview=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}

	// A step used to sit here that moved the authoritative ask under the sealed
	// preview and expected the command to be refused: test mode capped a manual
	// order at 100 USDT of notional, priced from the market rather than from the
	// client's limit price. There is no cap left to trip, so the reprice has
	// nothing to assert and the flow goes straight to placing the order.
	commandRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"manual-restart-1"}`))
	commandRequest.Header.Set("Idempotency-Key", "manual-restart-1")
	commandResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(commandResponse, commandRequest)
	var commandBody struct {
		Data struct {
			CommandID     string  `json:"commandId"`
			ReservationID *string `json:"reservationId"`
		} `json:"data"`
	}
	if commandResponse.Code != http.StatusAccepted || json.Unmarshal(commandResponse.Body.Bytes(), &commandBody) != nil || commandBody.Data.CommandID == "" || commandBody.Data.ReservationID == nil {
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
	items, err := reservationService.List(context.Background(), "principal-a")
	if err != nil || len(items) != 1 || items[0].ReservationID != *commandBody.Data.ReservationID || items[0].Status != reservations.StatusConsumed || items[0].Scope.Venue != "binance" || items[0].Scope.VenueEnvironment != "live" {
		t.Fatalf("manual reservation=%+v err=%v", items, err)
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"manual-restart-1"}`))
	replayRequest.Header.Set("Idempotency-Key", "manual-restart-1")
	replayResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(replayResponse, replayRequest)
	items, err = reservationService.List(context.Background(), "principal-a")
	if replayResponse.Code != http.StatusOK || gateway.calls != 1 || err != nil || len(items) != 1 || items[0].Status != reservations.StatusConsumed {
		t.Fatalf("manual replay=%d gateway calls=%d reservations=%+v err=%v body=%s", replayResponse.Code, gateway.calls, items, err, replayResponse.Body.String())
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

type manualReservationGate struct{ service *reservations.Service }

func (gate manualReservationGate) Consume(ctx context.Context, claim orders.ReservationClaim) error {
	_, err := gate.service.Consume(ctx, reservations.ConsumeRequest{
		ReservationID: claim.ReservationID, PrincipalID: claim.PrincipalID, ProductID: claim.ProductID,
	})
	return err
}

type manualCatalogSource struct {
	revision string
	unready  bool
	now      func() time.Time
}

type staticManualAccounts struct {
	account ManualAccount
}

func (accounts *staticManualAccounts) Get(context.Context, string, string) (ManualAccount, error) {
	return accounts.account, nil
}

func readyManualAccount() ManualAccount {
	return ManualAccount{
		ID: "account-a", Venue: "binance", VenueEnvironment: "live",
		ProductIDs: []string{"manual"}, RuntimeID: "runtime-1", State: manualAccountActive,
		Routes: []ManualAccountRoute{{RouteID: "perp", Product: "perp", RequestedState: manualAccountActive}},
	}
}

func TestManualAccountRoutePrefersSettlementSpecificRouteOverProductWideOne(t *testing.T) {
	account := ManualAccount{Routes: []ManualAccountRoute{
		{RouteID: "perp-any", Product: "perp", RequestedState: manualAccountActive},
		{RouteID: "perp-usdc", Product: "perp", SettlementAsset: "USDC", RequestedState: manualAccountActive},
		{RouteID: "spot-disabled", Product: "spot", RequestedState: "disabled"},
	}}
	tests := []struct {
		name       string
		product    string
		settlement string
		wantRoute  string
		wantRouted bool
	}{
		{"settlement specific wins", "perp", "USDC", "perp-usdc", true},
		{"unnamed settlement falls back to product wide", "perp", "USDT", "perp-any", true},
		{"disabled route does not serve its product", "spot", "USDT", "", false},
		{"unrouted product is refused", "margin", "USDT", "", false},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			route, routed := manualAccountRoute(account, test.product, test.settlement)
			if route != test.wantRoute || routed != test.wantRouted {
				t.Fatalf("route=%q routed=%v, want %q/%v", route, routed, test.wantRoute, test.wantRouted)
			}
		})
	}
}

func TestManualAccountReadyRequiresActiveState(t *testing.T) {
	for _, state := range []string{"creating", "rotating"} {
		account := readyManualAccount()
		account.State = state
		if manualAccountReady(account) {
			t.Fatalf("state %s reported ready", state)
		}
	}
	if !manualAccountReady(readyManualAccount()) {
		t.Fatal("active account reported unready")
	}
}

func TestManualCancelTransitionsOwnedRestingOrderWithoutNewReservation(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandStore := orders.NewMemoryStore()
	commandService := newCommandService(commandStore, manualReservationGate{service: reservationService}, time.Now)
	gateway := &manualGatewayFixture{}
	projection, err := catalog.NewProjection(manualCatalogSource{}, time.Minute, time.Now)
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservationService,
		Commands:     commandService,
		Portfolio:    portfolio.NewProjection(),
		Ledger:       ledger.NewService(ledger.NewMemoryStore(), time.Now),
		Catalog:      projection,
		Manual: newManualService(
			commandService,
			portfolio.NewProjection(),
			ledger.NewService(ledger.NewMemoryStore(), time.Now),
			gateway,
			time.Now,
			[]byte("shared-preview-signing-key-with-32-bytes"),
		),
		ManualAccounts:    &staticManualAccounts{account: readyManualAccount()},
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
	wrongLineageRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"cancel","lineageId":"lineage-b","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-wrong-lineage"}`))
	wrongLineageRequest.Header.Set("Idempotency-Key", "manual-cancel-wrong-lineage")
	wrongLineageResponse := httptest.NewRecorder()
	mux.ServeHTTP(wrongLineageResponse, wrongLineageRequest)
	commandsAfterWrongLineage, commandErr := commandStore.ListOwned(context.Background(), "principal-a", "manual")
	if wrongLineageResponse.Code != http.StatusNotFound || commandErr != nil || len(commandsAfterWrongLineage) != 1 || gateway.cancelCalls != 0 {
		t.Fatalf("wrong-lineage cancel=%d commands=%+v Gateway calls=%d err=%v body=%s", wrongLineageResponse.Code, commandsAfterWrongLineage, gateway.cancelCalls, commandErr, wrongLineageResponse.Body.String())
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"cancel","lineageId":"lineage-a","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-a"}`))
	request.Header.Set("Idempotency-Key", "manual-cancel-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	reservationsAfter, reservationErr := reservationService.List(context.Background(), "principal-a")
	commandsAfter, commandErr := commandStore.ListOwned(context.Background(), "principal-a", "manual")
	if response.Code != http.StatusAccepted || !strings.Contains(response.Body.String(), `"state":"canceled"`) || reservationErr != nil || commandErr != nil || len(reservationsAfter) != 0 || len(commandsAfter) != 1 || gateway.calls != 0 || gateway.cancelCalls != 1 {
		t.Fatalf("cancel=%d reservations=%+v commands=%+v gateway calls=%d/%d reservation err=%v command err=%v body=%s", response.Code, reservationsAfter, commandsAfter, gateway.calls, gateway.cancelCalls, reservationErr, commandErr, response.Body.String())
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"cancel","lineageId":"lineage-a","orderId":"`+command.CommandID+`","idempotencyKey":"manual-cancel-a"}`))
	replayRequest.Header.Set("Idempotency-Key", "manual-cancel-a")
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK || gateway.cancelCalls != 1 {
		t.Fatalf("cancel replay=%d Gateway calls=%d body=%s", replay.Code, gateway.cancelCalls, replay.Body.String())
	}
}

func TestManualSafeCloseDerivesReduceOnlyMarketIntentFromPosition(t *testing.T) {
	now := time.Date(2026, time.August, 6, 4, 0, 0, 0, time.UTC)
	reservationService := reservations.NewService(reservations.NewMemoryStore(), func() time.Time { return now })
	commandService := newCommandService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, func() time.Time { return now })
	portfolioProjection := portfolio.NewProjection()
	if _, _, err := portfolioProjection.ApplyFill(context.Background(), portfolio.Fill{PrincipalID: "principal-a",
		FillID: "open-fill-a", CommandID: "open-command-a", LineageID: "lineage-close-a", ProductID: "manual",
		AccountID: "account-a", InstrumentID: "BTC-USDT", Side: portfolio.SideBuy,
		Quantity: "2", Price: "49", Fee: "0", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: now,
	}); err != nil {
		t.Fatal(err)
	}
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), func() time.Time { return now })
	gateway := &manualGatewayFixture{}
	projection, err := catalog.NewProjection(manualCatalogSource{now: func() time.Time { return now }}, time.Minute, func() time.Time { return now })
	if err != nil {
		t.Fatal(err)
	}
	mux := http.NewServeMux()
	if err := RegisterTradingRoutes(mux, TradingOptions{
		Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService,
		Catalog: projection, Manual: newManualService(commandService, portfolioProjection, ledgerService, gateway, func() time.Time { return now }, []byte("shared-preview-signing-key-with-32-bytes")),
		ManualAccounts: &staticManualAccounts{account: readyManualAccount()},
		Principal:      func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}
	unownedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"close","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"manual-close-unowned"}`))
	unownedRequest.Header.Set("Idempotency-Key", "manual-close-unowned")
	unownedResponse := httptest.NewRecorder()
	mux.ServeHTTP(unownedResponse, unownedRequest)
	if positions := portfolioProjection.List("principal-a", "account-a", "manual"); unownedResponse.Code != http.StatusNotFound || len(positions) != 1 || positions[0].Quantity != "2" || gateway.calls != 0 {
		t.Fatalf("unowned close=%d positions=%+v gateway=%d body=%s", unownedResponse.Code, positions, gateway.calls, unownedResponse.Body.String())
	}
	if _, _, err := commandService.Create(context.Background(), orders.CreateRequest{
		PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-close-a",
		IdempotencyKey: "manual-open-a", Method: http.MethodPost, Path: "/api/v1/products/manual/commands",
		CommandKind: "manual.place",
		Payload:     []byte(`{"instrumentId":"BTC-USDT","side":"buy","quantity":"2","orderType":"market","reduceOnly":false}`),
	}); err != nil {
		t.Fatal(err)
	}
	for _, terminal := range []orders.State{orders.StateFailed, orders.StateCanceled} {
		key := "manual-close-" + string(terminal)
		terminalCommand, _, err := commandService.Create(context.Background(), orders.CreateRequest{
			PrincipalID: "principal-a", AccountID: "account-a", ProductID: "manual", LineageID: "lineage-close-a",
			IdempotencyKey: key, Method: http.MethodPost, Path: "/api/v1/products/manual/commands",
			CommandKind: "manual.place",
			Payload:     []byte(`{"instrumentId":"BTC-USDT","side":"sell","quantity":"2","orderType":"market","reduceOnly":true}`),
		})
		if err != nil {
			t.Fatal(err)
		}
		if _, err := commandService.Transition(context.Background(), orders.TransitionRequest{CommandID: terminalCommand.CommandID, State: terminal}); err != nil {
			t.Fatal(err)
		}
		terminalRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"close","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"`+key+`"}`))
		terminalRequest.Header.Set("Idempotency-Key", key)
		terminalResponse := httptest.NewRecorder()
		mux.ServeHTTP(terminalResponse, terminalRequest)
		if positions := portfolioProjection.List("principal-a", "account-a", "manual"); terminalResponse.Code != http.StatusConflict || len(positions) != 1 || positions[0].Quantity != "2" || gateway.calls != 0 {
			t.Fatalf("%s close replay=%d positions=%+v gateway=%d body=%s", terminal, terminalResponse.Code, positions, gateway.calls, terminalResponse.Body.String())
		}
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"close","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"manual-close-a"}`))
	request.Header.Set("Idempotency-Key", "manual-close-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	positions := portfolioProjection.List("principal-a", "account-a", "manual")
	items, reservationErr := reservationService.List(context.Background(), "principal-a")
	commands, commandErr := commandService.ListOwned(context.Background(), "principal-a", "manual")
	if response.Code != http.StatusAccepted || reservationErr != nil || commandErr != nil || len(positions) != 1 || positions[0].Quantity != "0" || len(items) != 1 || items[0].Kind != reservations.KindClose || len(commands) != 4 || gateway.calls != 1 {
		t.Fatalf("close=%d positions=%+v reservations=%+v commands=%+v gateway=%d errors=%v/%v body=%s", response.Code, positions, items, commands, gateway.calls, reservationErr, commandErr, response.Body.String())
	}
	if request := gateway.lastRequest; request.Side != "sell" || request.Quantity != "2" || request.OrderType != "market" || !request.ReduceOnly || request.InstrumentID != "BTC-USDT" {
		t.Fatalf("safe-close Gateway request=%+v", request)
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"close","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"manual-close-a"}`))
	replayRequest.Header.Set("Idempotency-Key", "manual-close-a")
	replay := httptest.NewRecorder()
	mux.ServeHTTP(replay, replayRequest)
	if replay.Code != http.StatusOK || gateway.calls != 1 {
		t.Fatalf("safe-close replay=%d Gateway calls=%d body=%s", replay.Code, gateway.calls, replay.Body.String())
	}
	closedRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"close","lineageId":"lineage-close-a","orderId":"","idempotencyKey":"manual-close-after-zero"}`))
	closedRequest.Header.Set("Idempotency-Key", "manual-close-after-zero")
	closedResponse := httptest.NewRecorder()
	mux.ServeHTTP(closedResponse, closedRequest)
	items, reservationErr = reservationService.List(context.Background(), "principal-a")
	commands, commandErr = commandService.ListOwned(context.Background(), "principal-a", "manual")
	if closedResponse.Code != http.StatusNotFound || reservationErr != nil || commandErr != nil || len(items) != 1 || len(commands) != 4 || gateway.calls != 1 {
		t.Fatalf("close-after-zero=%d reservations=%+v commands=%+v Gateway calls=%d errors=%v/%v body=%s", closedResponse.Code, items, commands, gateway.calls, reservationErr, commandErr, closedResponse.Body.String())
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

func TestManualCommandRejectsChangedCatalogBeforeReservation(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandService := newCommandService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, time.Now)
	portfolioProjection := portfolio.NewProjection()
	ledgerService := ledger.NewService(ledger.NewMemoryStore(), time.Now)
	previewKey := []byte("shared-preview-signing-key-with-32-bytes")
	gateway := &manualGatewayFixture{}

	newInstance := func(revision string, unready ...bool) *http.ServeMux {
		source := manualCatalogSource{revision: revision}
		if len(unready) > 0 {
			source.unready = unready[0]
		}
		projection, err := catalog.NewProjection(source, time.Minute, time.Now)
		if err != nil {
			t.Fatal(err)
		}
		mux := http.NewServeMux()
		err = RegisterTradingRoutes(mux, TradingOptions{
			Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService,
			Catalog: projection, Manual: newManualService(commandService, portfolioProjection, ledgerService, gateway, time.Now, previewKey),
			ManualAccounts: &staticManualAccounts{account: readyManualAccount()},
			Principal:      func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	previewResponse := httptest.NewRecorder()
	newInstance("catalog-1").ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false}`)))
	var previewBody struct {
		Data struct {
			PreviewID string `json:"previewId"`
		} `json:"data"`
	}
	if previewResponse.Code != http.StatusCreated || json.Unmarshal(previewResponse.Body.Bytes(), &previewBody) != nil {
		t.Fatalf("preview=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}
	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"stale-catalog"}`))
	request.Header.Set("Idempotency-Key", "stale-catalog")
	response := httptest.NewRecorder()
	newInstance("catalog-2").ServeHTTP(response, request)
	items, err := reservationService.List(context.Background(), "principal-a")
	if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
		t.Fatalf("command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"unready-route"}`))
	request.Header.Set("Idempotency-Key", "unready-route")
	response = httptest.NewRecorder()
	newInstance("catalog-1", true).ServeHTTP(response, request)
	items, err = reservationService.List(context.Background(), "principal-a")
	if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
		t.Fatalf("unready route command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
	}
}

func TestManualCommandRejectsExpiredOrChangedAccountBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*time.Time, *ManualAccount)
	}{
		{name: "expired preview", mutate: func(now *time.Time, _ *ManualAccount) { *now = now.Add(manualPreviewTTL) }},
		{name: "runtime identity changed", mutate: func(_ *time.Time, account *ManualAccount) { account.RuntimeID = "runtime-2" }},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			accountSource := &staticManualAccounts{account: readyManualAccount()}
			reservationService := reservations.NewService(reservations.NewMemoryStore(), clock)
			commandService := newCommandService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, clock)
			portfolioProjection := portfolio.NewProjection()
			ledgerService := ledger.NewService(ledger.NewMemoryStore(), clock)
			gateway := &manualGatewayFixture{}
			projection, err := catalog.NewProjection(manualCatalogSource{now: clock}, time.Minute, clock)
			if err != nil {
				t.Fatal(err)
			}
			mux := http.NewServeMux()
			err = RegisterTradingRoutes(mux, TradingOptions{
				Reservations: reservationService, Commands: commandService, Portfolio: portfolioProjection, Ledger: ledgerService,
				Catalog: projection, Manual: newManualService(commandService, portfolioProjection, ledgerService, gateway, clock, []byte("shared-preview-signing-key-with-32-bytes")),
				ManualAccounts: accountSource, Principal: func(*http.Request) (string, error) { return "principal-a", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			previewResponse := httptest.NewRecorder()
			mux.ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false}`)))
			var previewBody struct {
				Data struct {
					PreviewID string `json:"previewId"`
				} `json:"data"`
			}
			if previewResponse.Code != http.StatusCreated || json.Unmarshal(previewResponse.Body.Bytes(), &previewBody) != nil {
				t.Fatalf("preview=%d body=%s", previewResponse.Code, previewResponse.Body.String())
			}
			test.mutate(&now, &accountSource.account)
			request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"stale-authority"}`))
			request.Header.Set("Idempotency-Key", "stale-authority")
			response := httptest.NewRecorder()
			mux.ServeHTTP(response, request)
			items, err := reservationService.List(context.Background(), "principal-a")
			if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
				t.Fatalf("command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
			}
		})
	}
}
