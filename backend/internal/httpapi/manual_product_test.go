package httpapi

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	platformaccounts "github.com/QuantProcessing/boltertrader/backend/internal/platform/accounts"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/ledger"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/reservations"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
)

func TestManualPreviewAndOrderReconstructAcrossInstances(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandService := orders.NewService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, time.Now)
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
			Manual:            manual.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, gateway, time.Now, previewKey),
			ManualAccounts:    &staticManualAccounts{account: readyManualAccount()},
			Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
			AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	previewResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`)))
	var previewBody struct {
		Data struct {
			PreviewID string `json:"previewId"`
		} `json:"data"`
	}
	if previewResponse.Code != http.StatusCreated || json.Unmarshal(previewResponse.Body.Bytes(), &previewBody) != nil || previewBody.Data.PreviewID == "" {
		t.Fatalf("preview=%d body=%s", previewResponse.Code, previewResponse.Body.String())
	}

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
	for key, want := range map[string]float64{"desiredStateRevision": 1, "runtimeGeneration": 1, "credentialVersion": 1} {
		if intent[key] != want {
			t.Fatalf("durable Manual intent %s=%v, want %v; payload=%s", key, intent[key], want, storedCommand.Payload)
		}
	}
	items, err := reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
	if err != nil || len(items) != 1 || items[0].ReservationID != *commandBody.Data.ReservationID || items[0].Status != reservations.StatusConsumed || items[0].Scope.Venue != "binance" || items[0].Scope.VenueEnvironment != "demo" {
		t.Fatalf("manual reservation=%+v err=%v", items, err)
	}
	replayRequest := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"manual-restart-1"}`))
	replayRequest.Header.Set("Idempotency-Key", "manual-restart-1")
	replayResponse := httptest.NewRecorder()
	newInstance().ServeHTTP(replayResponse, replayRequest)
	items, err = reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
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
		ReservationID: claim.ReservationID, PrincipalID: claim.PrincipalID, Mode: reservations.Mode(claim.Mode), ProductID: claim.ProductID,
	})
	return err
}

type manualCatalogSource struct {
	revision string
	unready  bool
	now      func() time.Time
}

type staticManualAccounts struct {
	account platformaccounts.Account
}

func (accounts *staticManualAccounts) Get(context.Context, string) (platformaccounts.Account, error) {
	return accounts.account, nil
}

func readyManualAccount() platformaccounts.Account {
	return platformaccounts.Account{
		ID: "account-a", Venue: "binance", VenueEnvironment: platformaccounts.EnvironmentDemo,
		ProductIDs: []string{"manual"}, DesiredStateRevision: 1, RuntimeGeneration: 1,
		CredentialVersion: 1, State: platformaccounts.StateActive,
	}
}

func TestManualAccountReadyRequiresActiveState(t *testing.T) {
	for _, state := range []platformaccounts.State{platformaccounts.StateCreating, platformaccounts.StateRotating} {
		account := readyManualAccount()
		account.State = state
		if manualAccountReady(account, "test") {
			t.Fatalf("state %s reported ready", state)
		}
	}
	if !manualAccountReady(readyManualAccount(), "test") {
		t.Fatal("active account reported unready")
	}
}

func TestManualCancelIsRejectedBeforeReservationCommandOrGatewayEffects(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandStore := orders.NewMemoryStore()
	commandService := orders.NewService(commandStore, manualReservationGate{service: reservationService}, time.Now)
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
		Manual: manual.NewGatewayServiceWithPreviewKey(
			commandService,
			portfolio.NewProjection(),
			ledger.NewService(ledger.NewMemoryStore(), time.Now),
			gateway,
			time.Now,
			[]byte("shared-preview-signing-key-with-32-bytes"),
		),
		ManualAccounts:    &staticManualAccounts{account: readyManualAccount()},
		Principal:         func(*http.Request) (string, string, error) { return "principal-a", "test", nil },
		AuthorizeMutation: func(*http.Request) error { return nil },
	}); err != nil {
		t.Fatal(err)
	}

	request := httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"","action":"cancel","lineageId":"lineage-a","orderId":"venue-order-a","idempotencyKey":"manual-cancel-a"}`))
	request.Header.Set("Idempotency-Key", "manual-cancel-a")
	response := httptest.NewRecorder()
	mux.ServeHTTP(response, request)

	reservationsAfter, reservationErr := reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
	commandsAfter, commandErr := commandStore.ListOwned(context.Background(), "principal-a", orders.ModeTest, "manual")
	if response.Code != http.StatusBadRequest || !strings.Contains(response.Body.String(), "VALIDATION_ERROR") || reservationErr != nil || commandErr != nil || len(reservationsAfter) != 0 || len(commandsAfter) != 0 || gateway.calls != 0 {
		t.Fatalf("cancel=%d reservations=%+v commands=%+v gateway calls=%d reservation err=%v command err=%v body=%s", response.Code, reservationsAfter, commandsAfter, gateway.calls, reservationErr, commandErr, response.Body.String())
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
		Venues:      []catalog.Venue{{Venue: "binance", Environment: "demo", Ready: !source.unready}},
		Instruments: []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: !source.unready}},
	}, nil
}

func TestManualCommandRejectsChangedCatalogBeforeReservation(t *testing.T) {
	reservationService := reservations.NewService(reservations.NewMemoryStore(), time.Now)
	commandService := orders.NewService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, time.Now)
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
			Catalog: projection, Manual: manual.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, gateway, time.Now, previewKey),
			ManualAccounts: &staticManualAccounts{account: readyManualAccount()},
			Principal:      func(*http.Request) (string, string, error) { return "principal-a", "test", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
		})
		if err != nil {
			t.Fatal(err)
		}
		return mux
	}

	previewResponse := httptest.NewRecorder()
	newInstance("catalog-1").ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`)))
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
	items, err := reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
	if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
		t.Fatalf("command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
	}

	request = httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/commands", strings.NewReader(`{"previewId":"`+previewBody.Data.PreviewID+`","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"unready-route"}`))
	request.Header.Set("Idempotency-Key", "unready-route")
	response = httptest.NewRecorder()
	newInstance("catalog-1", true).ServeHTTP(response, request)
	items, err = reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
	if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
		t.Fatalf("unready route command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
	}
}

func TestManualCommandRejectsExpiredOrChangedAccountBeforeReservation(t *testing.T) {
	for _, test := range []struct {
		name   string
		mutate func(*time.Time, *platformaccounts.Account)
	}{
		{name: "expired preview", mutate: func(now *time.Time, _ *platformaccounts.Account) { *now = now.Add(manualPreviewTTL) }},
		{name: "runtime generation changed", mutate: func(_ *time.Time, account *platformaccounts.Account) { account.RuntimeGeneration++ }},
	} {
		t.Run(test.name, func(t *testing.T) {
			now := time.Date(2026, 8, 6, 2, 0, 0, 0, time.UTC)
			clock := func() time.Time { return now }
			accountSource := &staticManualAccounts{account: readyManualAccount()}
			reservationService := reservations.NewService(reservations.NewMemoryStore(), clock)
			commandService := orders.NewService(orders.NewMemoryStore(), manualReservationGate{service: reservationService}, clock)
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
				Catalog: projection, Manual: manual.NewGatewayServiceWithPreviewKey(commandService, portfolioProjection, ledgerService, gateway, clock, []byte("shared-preview-signing-key-with-32-bytes")),
				ManualAccounts: accountSource, Principal: func(*http.Request) (string, string, error) { return "principal-a", "test", nil }, AuthorizeMutation: func(*http.Request) error { return nil },
			})
			if err != nil {
				t.Fatal(err)
			}
			previewResponse := httptest.NewRecorder()
			mux.ServeHTTP(previewResponse, httptest.NewRequest(http.MethodPost, "/api/v1/products/manual/execution-previews", strings.NewReader(`{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`)))
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
			items, err := reservationService.List(context.Background(), "principal-a", reservations.ModeTest)
			if response.Code != http.StatusNotFound || err != nil || len(items) != 0 || gateway.calls != 0 {
				t.Fatalf("command=%d reservations=%+v gateway calls=%d err=%v body=%s", response.Code, items, gateway.calls, err, response.Body.String())
			}
		})
	}
}
