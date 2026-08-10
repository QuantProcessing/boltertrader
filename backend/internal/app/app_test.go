package app

import (
	"context"
	"crypto/sha256"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/boltertrader/backend/internal/config"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/catalog"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/marketdata"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/orders"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/portfolio"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/secret"
	"github.com/QuantProcessing/boltertrader/backend/internal/platform/session"
	"github.com/QuantProcessing/boltertrader/backend/internal/products/manual"
	"github.com/QuantProcessing/boltertrader/secretstore"
)

func TestAppStartsAndStops(t *testing.T) {
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
		_, _ = w.Write([]byte(`{"command_id":"018f0000-0000-7000-8000-000000000001","gateway_order_id":"018f0000-0000-7000-8000-000000000002","client_order_id":"42","state":"submitted","resource_version":1}`))
	}))
	defer gateway.Close()
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	cfg := config.Config{
		BrowserListenAddr: "127.0.0.1:18191", WorkListenAddr: "127.0.0.1:18192",
		BootstrapUsername: "operator", BootstrapPasswordHash: passwordHash,
		FundingWorkerTokenFile:      "/protected/test-worker.token",
		FundingWorkerTokenDigest:    sha256.Sum256([]byte("test-worker-token-with-at-least-32-bytes")),
		ManualPreviewSigningKeyFile: "/protected/test/manual-preview-signing.key",
		ManualPreviewSigningKey:     []byte("manual-preview-signing-key-with-at-least-32-bytes"),
		GatewayURL:                  gateway.URL,
		GatewayTokenFile:            "/protected/test/gateway.token",
		GatewayToken:                []byte("gateway-adapter-token-with-at-least-32-bytes"),
		Mode:                        "test",
	}
	app, err := NewWithDependencies(cfg, testDependencies())
	if err != nil {
		t.Fatal(err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- app.Start(ctx) }()
	time.Sleep(150 * time.Millisecond)
	cancel()
	select {
	case err := <-done:
		if err != nil && err != http.ErrServerClosed {
			t.Fatal(err)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("app did not stop")
	}
}

func TestNewWiresGatewayDesiredClientAndAccountReconciler(t *testing.T) {
	app, err := New(testConfig(t, "test"))
	if err != nil {
		t.Fatal(err)
	}
	if app.accountReconciler == nil {
		t.Fatal("production Gateway desired client did not create an account reconciler")
	}
}

func TestStartRejectsMissingCredentialWriterBeforeListen(t *testing.T) {
	cfg := testConfig(t, "test")
	app, err := NewWithDependencies(cfg, Dependencies{MarketDataProbe: readyMarketDataProbe{}, RuntimeProbe: readyMarketDataProbe{}, ManualGateway: &recordingManualGateway{}, FundingGateway: &recordingFundingGateway{}})
	if err != nil {
		t.Fatal(err)
	}
	if err := app.Start(context.Background()); err == nil || !strings.Contains(err.Error(), "Secret Store") {
		t.Fatalf("Start error=%v", err)
	}
	for _, address := range []string{cfg.BrowserListenAddr, cfg.WorkListenAddr} {
		listener, err := net.Listen("tcp", address)
		if err != nil {
			t.Fatalf("listener %s was bound before secret check: %v", address, err)
		}
		_ = listener.Close()
	}
}

func TestNewUsesConfiguredGatewayReadSourcesForManualPreview(t *testing.T) {
	const token = "gateway-adapter-token-with-at-least-32-bytes"
	gateway := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set(gatewayModeHeader, "test")
		if r.Header.Get("Authorization") != "Bearer "+token {
			t.Errorf("authorization=%q", r.Header.Get("Authorization"))
		}
		if r.Header.Get(gatewayModeHeader) != "test" {
			t.Errorf("mode attestation=%q", r.Header.Get(gatewayModeHeader))
		}
		w.Header().Set(gatewayModeHeader, "test")
		switch r.URL.Path {
		case "/internal/v1/market-data/readiness":
			if r.URL.RawQuery != "" {
				t.Errorf("readiness probe query=%q", r.URL.RawQuery)
			}
			_, _ = w.Write([]byte(`{"data":{"ready":true}}`))
		case "/internal/v1/catalog/venues":
			_, _ = w.Write([]byte(`{"data":[{"venue":"binance","environment":"demo","ready":true}]}`))
		case "/internal/v1/catalog/instruments":
			_, _ = fmt.Fprintf(w, `{"data":{"revision":"gateway-catalog-1","observedAt":%q,"instruments":[{"instrumentId":"BTC-USDT","venue":"binance","environment":"demo","product":"perp","symbol":"BTC-USDT","baseAsset":"BTC","quoteAsset":"USDT","rules":{"priceIncrement":"0.1","quantityIncrement":"0.001","minQuantity":"0.001"},"ready":true}]}}`, time.Now().UTC().Format(time.RFC3339Nano))
		default:
			http.NotFound(w, r)
		}
	}))
	defer gateway.Close()
	cfg := testConfig(t, "test")
	cfg.GatewayURL = gateway.URL
	cfg.GatewayToken = []byte(token)
	app, err := New(cfg)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := appLogin(t, app)
	readiness := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/system/readiness", "", "")
	if strings.Contains(readiness.Body.String(), `"gateway":{"ready":true`) {
		t.Fatalf("unobserved Gateway reported ready: %s", readiness.Body.String())
	}
	if !strings.Contains(readiness.Body.String(), `"publicData":{"ready":true`) {
		t.Fatalf("reachable market-data route reported unavailable before catalog observation: %s", readiness.Body.String())
	}

	venues := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/products/manual/venues", "", "")
	if venues.Code != http.StatusOK || !strings.Contains(venues.Body.String(), `"revision":"gateway-`) {
		t.Fatalf("venues=%d body=%s", venues.Code, venues.Body.String())
	}
	readiness = appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/system/readiness", "", "")
	if !strings.Contains(readiness.Body.String(), `"gateway":{"ready":true`) || !strings.Contains(readiness.Body.String(), `"publicData":{"ready":true`) {
		t.Fatalf("observed Gateway did not report ready: %s", readiness.Body.String())
	}
	preview := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/products/manual/execution-previews", `{"accountId":"account-a","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`, "")
	if preview.Code != http.StatusServiceUnavailable || !strings.Contains(preview.Body.String(), "CANONICAL_WRITES_NOT_READY") {
		t.Fatalf("preview=%d body=%s", preview.Code, preview.Body.String())
	}
}

func TestMigrationValidationRequiresWriterUsageAbsent(t *testing.T) {
	cfg := testConfig(t, "test")
	cfg.StartupProfile = config.StartupMigrationValidation
	dependencies := testDependencies()
	if _, err := NewWithDependencies(cfg, dependencies); err == nil || !strings.Contains(err.Error(), "writer USAGE") {
		t.Fatalf("NewWithDependencies() error=%v", err)
	}
	dependencies.WriteAuthority = nil
	if _, err := NewWithDependencies(cfg, dependencies); err == nil || !strings.Contains(err.Error(), "verified absent") {
		t.Fatalf("unverified NewWithDependencies() error=%v", err)
	}

	dependencies.WriteAuthority = NewWriteAuthority(WriteAuthorityFacts{RuntimeInherits: true})
	app, err := NewWithDependencies(cfg, dependencies)
	if err != nil {
		t.Fatal(err)
	}
	if app.reconciler != nil || app.accountReconciler != nil {
		t.Fatal("migration validation constructed a background reconciler")
	}
	cookie, csrf := appLogin(t, app)
	response := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/reservations", `{}`, "")
	if response.Code != http.StatusServiceUnavailable || !strings.Contains(response.Body.String(), "CANONICAL_WRITES_NOT_READY") {
		t.Fatalf("reservation=%d body=%s", response.Code, response.Body.String())
	}
	readiness := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/system/status", "", "")
	if readiness.Code != http.StatusOK || !strings.Contains(readiness.Body.String(), `"canonicalWrites":{"ready":false`) {
		t.Fatalf("status=%d body=%s", readiness.Code, readiness.Body.String())
	}
}

func TestAppSourceVerifiesAuthorityAndWiresPostgresWorkStoreWhenDatabaseExists(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	for _, required := range []string{
		"postgres.VerifyRuntimeAuthority(ctx, database, authority.PlatformV15())",
		"postgres.NewWorkStore(dependencies.Database)",
		"postgres.NewFundingPlanStore(dependencies.Database)",
		"postgres.NewFundingExecutionStore(dependencies.Database)",
		"work.NewDurableFundingPayloadProcessor(commandService, fundingPlanStore, fundingExecutionStore, now)",
		"fundingexecution.NewPlanProducer(fundingPlanStore, fundingExecutionStore)",
		"fundingPlanProducer.WithAdmission(fundingAdmission).WithAccounting(accounting.NewPlatformAdapter(ledgerService, portfolioProjection))",
		"work.NewWithStoreGatewayProcessorAndPlanProducer(workapi.ContractVersion, 15*time.Second, workStore, fundingProcessor, fundingPlanProducer, dependencies.FundingGateway)",
	} {
		if !strings.Contains(text, required) {
			t.Fatalf("app.go missing durable work wiring %q", required)
		}
	}
	if strings.Contains(text, "postgres.Migrate(ctx, database)") {
		t.Fatal("Platform runtime still executes migrations")
	}
}

func TestAppUsesDedicatedManualPreviewSigningKey(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	if !strings.Contains(text, "cfg.ManualPreviewSigningKey") {
		t.Fatal("app.go does not wire the dedicated Manual preview signing key")
	}
	forbiddenDerivation := "manualPreviewKey(cfg." + "FundingWorkerTokenDigest)"
	if strings.Contains(text, forbiddenDerivation) {
		t.Fatal("app.go derives Manual preview authority from the Funding Worker bearer token")
	}
}

func TestAppWiresOwnedRoutesAndKeepsLiveWritesDisabled(t *testing.T) {
	for _, mode := range []string{"test", "live"} {
		t.Run(mode, func(t *testing.T) {
			app, err := NewWithDependencies(testConfig(t, mode), testDependencies())
			if err != nil {
				t.Fatal(err)
			}
			login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"correct horse battery staple"}`))
			loginResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(loginResponse, login)
			if loginResponse.Code != http.StatusOK {
				t.Fatalf("login=%d body=%s", loginResponse.Code, loginResponse.Body.String())
			}
			cookie := loginResponse.Result().Cookies()[0]
			var loginBody struct {
				Data struct {
					Session struct {
						CSRFToken string `json:"csrfToken"`
					} `json:"session"`
				} `json:"data"`
			}
			if err := json.Unmarshal(loginResponse.Body.Bytes(), &loginBody); err != nil {
				t.Fatal(err)
			}

			accountsRequest := httptest.NewRequest(http.MethodGet, "/api/v1/accounts", nil)
			accountsRequest.AddCookie(cookie)
			accountsResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(accountsResponse, accountsRequest)
			if accountsResponse.Code != http.StatusOK || strings.Contains(accountsResponse.Body.String(), `"scaffold":true`) {
				t.Fatalf("accounts=%d body=%s", accountsResponse.Code, accountsResponse.Body.String())
			}
			readinessRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
			readinessRequest.AddCookie(cookie)
			readinessResponse := httptest.NewRecorder()
			app.browser.Handler.ServeHTTP(readinessResponse, readinessRequest)
			if readinessResponse.Code != http.StatusServiceUnavailable || strings.Contains(readinessResponse.Body.String(), "not_reported") {
				t.Fatalf("readiness=%d body=%s", readinessResponse.Code, readinessResponse.Body.String())
			}
			for _, component := range []string{"process", "database", "secretStore", "gateway", "publicData", "accounts", "fundingWorker"} {
				if !strings.Contains(readinessResponse.Body.String(), `"`+component+`"`) {
					t.Fatalf("readiness missing %s: %s", component, readinessResponse.Body.String())
				}
			}

			if mode == "live" {
				command := httptest.NewRequest(http.MethodPost, "/api/v1/commands", strings.NewReader(`{"accountId":"account-1","productId":"manual","lineageId":"lineage-1","reservationId":"","idempotencyKey":"command-1","commandKind":"place","mode":"live","payload":{}}`))
				command.AddCookie(cookie)
				command.Header.Set("Idempotency-Key", "command-1")
				command.Header.Set("X-CSRF-Token", loginBody.Data.Session.CSRFToken)
				response := httptest.NewRecorder()
				app.browser.Handler.ServeHTTP(response, command)
				if response.Code != http.StatusForbidden || !strings.Contains(response.Body.String(), "LIVE_PRIVATE_WRITE_DISABLED") {
					t.Fatalf("live command=%d body=%s", response.Code, response.Body.String())
				}
			}
		})
	}

	dependencies := testDependencies()
	dependencies.CatalogSource = appCatalogSource{}
	app, err := NewWithDependencies(testConfig(t, "test"), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	registration := httptest.NewRequest(http.MethodPost, "/internal/v1/funding-work/workers/registrations", strings.NewReader(`{"workerId":"worker-1","instanceId":"instance-1","productId":"funding","mode":"test","contractVersion":"boltertrader-product-platform-upgrade/v4"}`))
	registration.Header.Set("Authorization", "Bearer test-worker-token-with-at-least-32-bytes")
	registration.Header.Set("Idempotency-Key", "registration-1")
	response := httptest.NewRecorder()
	app.work.Handler.ServeHTTP(response, registration)
	if response.Code != http.StatusCreated || strings.Contains(response.Body.String(), `"scaffold":true`) {
		t.Fatalf("registration=%d body=%s", response.Code, response.Body.String())
	}
	assertFundingWorkerReadiness(t, app, false)
	heartbeat := httptest.NewRequest(http.MethodPut, "/internal/v1/funding-work/workers/worker-1/heartbeat", strings.NewReader(`{"workerGeneration":1,"instanceId":"instance-1","state":"ready","activeLeaseIds":[],"lastAppliedFactsCursor":""}`))
	heartbeat.Header.Set("Authorization", "Bearer test-worker-token-with-at-least-32-bytes")
	heartbeat.Header.Set("X-Worker-Id", "worker-1")
	heartbeat.Header.Set("X-Worker-Generation", "1")
	heartbeat.Header.Set("X-Worker-Instance-Id", "instance-1")
	heartbeatResponse := httptest.NewRecorder()
	app.work.Handler.ServeHTTP(heartbeatResponse, heartbeat)
	if heartbeatResponse.Code != http.StatusOK {
		t.Fatalf("heartbeat=%d body=%s", heartbeatResponse.Code, heartbeatResponse.Body.String())
	}
	assertFundingWorkerReadiness(t, app, true)
}

func TestAppWiresFundingBrowserThroughSharedWorkService(t *testing.T) {
	source, err := os.ReadFile("app.go")
	if err != nil {
		t.Fatal(err)
	}
	text := string(source)
	workIndex := strings.Index(text, "workService := work.NewWithStoreGatewayProcessorAndPlanProducer")
	browserIndex := strings.Index(text, "browserHandler, err := httpapi.New")
	if workIndex < 0 || browserIndex < 0 || workIndex > browserIndex {
		t.Fatalf("Funding work service must exist before browser wiring: work=%d browser=%d", workIndex, browserIndex)
	}
	for _, required := range []string{"httpapi.NewSharedFundingProjection", "Catalog: catalogProjection", "MarketData: marketService", "Accounts: accountService", "Reservations: reservationStore", "Admission: fundingAdmission", "workService.SetDependencyReadinessEvaluator", "persistence.Readiness(ctx)", "catalogProjection.Snapshot(ctx)", "dependencies.MarketDataProbe.CheckMarketDataRoute(ctx)", "dependencies.RuntimeProbe.CheckRuntimeRoute(ctx)", "workService.SetLiveWritesEnabled(cfg.FundingLiveEnabled)", "fundingProcessor.WithExecutionEvidence(portfolioProjection, ledgerService).WithAdmission(fundingAdmission)", "fundingPlanProducer.WithAdmission(fundingAdmission)", "&httpapi.FundingRoutesOptions", "Projection: fundingProjection", "Work: workService", "Executions: fundingExecutionStore", "manualService.SetExecutionAdmission(orders.Mode(cfg.Mode), cfg.LivePrivateWritesEnabled)"} {
		if !strings.Contains(text, required) {
			t.Errorf("app Funding wiring missing %q", required)
		}
	}
}

func assertFundingWorkerReadiness(t *testing.T, app *App, want bool) {
	t.Helper()
	login := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", strings.NewReader(`{"username":"operator","password":"correct horse battery staple"}`))
	loginResponse := httptest.NewRecorder()
	app.browser.Handler.ServeHTTP(loginResponse, login)
	if loginResponse.Code != http.StatusOK {
		t.Fatalf("login=%d body=%s", loginResponse.Code, loginResponse.Body.String())
	}
	readinessRequest := httptest.NewRequest(http.MethodGet, "/api/v1/system/readiness", nil)
	readinessRequest.AddCookie(loginResponse.Result().Cookies()[0])
	readinessResponse := httptest.NewRecorder()
	app.browser.Handler.ServeHTTP(readinessResponse, readinessRequest)
	var readinessBody struct {
		Data struct {
			Components map[string]struct {
				Ready bool `json:"ready"`
			} `json:"components"`
		} `json:"data"`
	}
	if err := json.Unmarshal(readinessResponse.Body.Bytes(), &readinessBody); err != nil {
		t.Fatal(err)
	}
	if readinessBody.Data.Components["fundingWorker"].Ready != want {
		t.Fatalf("funding worker readiness=%v want=%v body=%s", readinessBody.Data.Components["fundingWorker"].Ready, want, readinessResponse.Body.String())
	}
}

type recordingManualGateway struct {
	requests []manual.GatewayRequest
}

func (gateway *recordingManualGateway) Execute(_ context.Context, request manual.GatewayRequest) (manual.GatewayResult, error) {
	gateway.requests = append(gateway.requests, request)
	occurredAt := time.Date(2026, time.August, 5, 1, 2, 3, 0, time.UTC)
	evidence := orders.ExecutionEvidence{
		CommandID: request.CommandID, GatewayOrderID: "gateway-order-app-1", ClientOrderID: "client-order-app-1", State: "filled", ResourceVersion: 1,
		Fills: []orders.FillEvidence{{FillID: "gateway-fill-app-1", AccountID: request.AccountID, InstrumentID: request.InstrumentID, Side: request.Side, Quantity: request.Quantity, Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: occurredAt}},
	}
	return manual.GatewayResult{
		CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode, State: manual.GatewayStateFilled,
		Handoff: orders.HandoffResult{Outcome: orders.HandoffAccepted, Evidence: evidence},
		Fills: []manual.FillFact{{
			FillID: "gateway-fill-app-1", CommandID: request.CommandID, AccountID: request.AccountID, Mode: request.Mode,
			InstrumentID: request.InstrumentID, Side: portfolio.Side(request.Side), Quantity: request.Quantity,
			Price: "101.25", Fee: "1.5", FeeCurrency: "USDT", SettlementCurrency: "USDT", OccurredAt: occurredAt,
		}},
	}, nil
}

type appCatalogSource struct{}

func (appCatalogSource) Fetch(context.Context) (catalog.Snapshot, error) {
	return catalog.Snapshot{
		Revision: "gateway-catalog-1", ObservedAt: time.Now().UTC(),
		Venues:      []catalog.Venue{{Venue: "binance", Environment: "demo", Ready: true}},
		Instruments: []catalog.Instrument{{InstrumentID: "BTC-USDT", Venue: "binance", Environment: "demo", Product: "perp", Symbol: "BTC-USDT", BaseAsset: "BTC", QuoteAsset: "USDT", Rules: catalog.TradeRules{PriceIncrement: "0.1", QuantityIncrement: "0.001", MinQuantity: "0.001"}, Ready: true}},
	}, nil
}

type appMarketDataSource struct{}

func (appMarketDataSource) Fetch(_ context.Context, query marketdata.Query) (marketdata.Fact, error) {
	return marketdata.Fact{
		Venue: query.Venue, Environment: query.Environment, Product: query.Product, InstrumentID: query.InstrumentID,
		BestBid: "49", BestBidSize: "10", BestAsk: "50", BestAskSize: "10",
		Bids: []marketdata.Level{{Price: "49", Quantity: "10"}}, Asks: []marketdata.Level{{Price: "50", Quantity: "10"}},
		ObservedAt: time.Now().UTC(),
	}, nil
}

func TestManualFrozenRoutesReachGatewayAndRecordAuthoritativeEffects(t *testing.T) {
	gateway := &recordingManualGateway{}
	dependencies := testDependencies()
	dependencies.CatalogSource, dependencies.MarketDataSource, dependencies.ManualGateway = appCatalogSource{}, appMarketDataSource{}, gateway
	app, err := NewWithDependencies(testConfig(t, "test"), dependencies)
	if err != nil {
		t.Fatal(err)
	}
	cookie, csrf := appLogin(t, app)
	accountID := createManualAccount(t, app, cookie, csrf)

	venues := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/products/manual/venues", "", "")
	if venues.Code != http.StatusOK || !strings.Contains(venues.Body.String(), `"venue":"binance"`) || strings.Contains(venues.Body.String(), `"scaffold":true`) {
		t.Fatalf("venues=%d body=%s", venues.Code, venues.Body.String())
	}

	previewWithoutCSRF := appRequest(t, app, cookie, "", http.MethodPost, "/api/v1/products/manual/execution-previews", `{"accountId":"`+accountID+`","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`, "")
	if previewWithoutCSRF.Code != http.StatusForbidden {
		t.Fatalf("preview without CSRF=%d body=%s", previewWithoutCSRF.Code, previewWithoutCSRF.Body.String())
	}
	unknownPreviewField := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/products/manual/execution-previews", `{"accountId":"`+accountID+`","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test","fillPrice":"1"}`, "")
	if unknownPreviewField.Code != http.StatusBadRequest || strings.Count(unknownPreviewField.Body.String(), `"error"`) != 1 {
		t.Fatalf("unknown preview field=%d body=%s", unknownPreviewField.Code, unknownPreviewField.Body.String())
	}

	preview := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/products/manual/execution-previews", `{"accountId":"`+accountID+`","instrumentId":"BTC-USDT","side":"buy","orderType":"market","quantity":"2","limitPrice":"","timeInForce":"ioc","reduceOnly":false,"mode":"test"}`, "")
	if preview.Code != http.StatusCreated || strings.Contains(preview.Body.String(), `"scaffold":true`) {
		t.Fatalf("preview=%d body=%s", preview.Code, preview.Body.String())
	}
	var previewBody struct {
		Data struct {
			PreviewID string `json:"previewId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(preview.Body.Bytes(), &previewBody); err != nil || previewBody.Data.PreviewID == "" {
		t.Fatalf("preview body=%s err=%v", preview.Body.String(), err)
	}

	commandBody := `{"previewId":"` + previewBody.Data.PreviewID + `","action":"place","lineageId":"lineage-a","orderId":"","idempotencyKey":"manual-app-1"}`
	command := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/products/manual/commands", commandBody, "manual-app-1")
	if command.Code != http.StatusAccepted || !strings.Contains(command.Body.String(), `"state":"filled"`) {
		t.Fatalf("command=%d body=%s", command.Code, command.Body.String())
	}
	if len(gateway.requests) != 1 || gateway.requests[0].Mode != orders.ModeTest || gateway.requests[0].InstrumentID != "BTC-USDT" {
		t.Fatalf("Gateway requests=%+v", gateway.requests)
	}
	var commandResponse struct {
		Data struct {
			CommandID string `json:"commandId"`
		} `json:"data"`
	}
	if err := json.Unmarshal(command.Body.Bytes(), &commandResponse); err != nil || commandResponse.Data.CommandID == "" {
		t.Fatalf("command body=%s err=%v", command.Body.String(), err)
	}

	ordersList := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/products/manual/orders", "", "")
	if ordersList.Code != http.StatusOK || !strings.Contains(ordersList.Body.String(), commandResponse.Data.CommandID) {
		t.Fatalf("orders=%d body=%s", ordersList.Code, ordersList.Body.String())
	}
	order := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/products/manual/orders/"+commandResponse.Data.CommandID, "", "")
	if order.Code != http.StatusOK || !strings.Contains(order.Body.String(), `"state":"filled"`) {
		t.Fatalf("order=%d body=%s", order.Code, order.Body.String())
	}
	positions := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/positions?accountId="+accountID+"&productId=manual", "", "")
	if positions.Code != http.StatusOK || !strings.Contains(positions.Body.String(), `"quantity":"2"`) {
		t.Fatalf("positions=%d body=%s", positions.Code, positions.Body.String())
	}
	ledger := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/ledger?accountId="+accountID+"&productId=manual", "", "")
	if ledger.Code != http.StatusOK || !strings.Contains(ledger.Body.String(), `"entryId":"gateway-fill-app-1:fee"`) || !strings.Contains(ledger.Body.String(), `"amount":"1.5"`) {
		t.Fatalf("ledger=%d body=%s", ledger.Code, ledger.Body.String())
	}
	commands := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/commands?productId=manual", "", "")
	if commands.Code != http.StatusOK || !strings.Contains(commands.Body.String(), commandResponse.Data.CommandID) || strings.Contains(commands.Body.String(), `"scaffold":true`) || strings.Contains(commands.Body.String(), `"payload"`) || strings.Contains(commands.Body.String(), `"principalId"`) {
		t.Fatalf("commands=%d body=%s", commands.Code, commands.Body.String())
	}
	fills := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/fills?productId=manual", "", "")
	if fills.Code != http.StatusOK || !strings.Contains(fills.Body.String(), `"fillId":"gateway-fill-app-1"`) || !strings.Contains(fills.Body.String(), `"productId":"manual"`) || strings.Contains(fills.Body.String(), `"scaffold":true`) {
		t.Fatalf("fills=%d body=%s", fills.Code, fills.Body.String())
	}
	activity := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/activity", "", "")
	for _, expected := range []string{`"kind":"account.created"`, `"kind":"command.filled"`, `"kind":"fill.recorded"`} {
		if activity.Code != http.StatusOK || !strings.Contains(activity.Body.String(), expected) || strings.Contains(activity.Body.String(), `"scaffold":true`) || strings.Contains(activity.Body.String(), `"principalId"`) {
			t.Fatalf("activity missing %s: status=%d body=%s", expected, activity.Code, activity.Body.String())
		}
	}
	runtimes := appRequest(t, app, cookie, csrf, http.MethodGet, "/api/v1/accounts/"+accountID+"/runtimes", "", "")
	if runtimes.Code != http.StatusServiceUnavailable || !strings.Contains(runtimes.Body.String(), "RUNTIME_PROJECTION_UNAVAILABLE") || strings.Contains(runtimes.Body.String(), `"scaffold":true`) {
		t.Fatalf("fail-closed runtimes=%d body=%s", runtimes.Code, runtimes.Body.String())
	}
}

func TestManualFailsStartupWithoutGatewayAuthority(t *testing.T) {
	if _, err := NewWithDependencies(testConfig(t, "test"), Dependencies{}); err == nil || !strings.Contains(err.Error(), "Manual Gateway") {
		t.Fatalf("NewWithDependencies() error=%v, want fail-closed Manual Gateway dependency", err)
	}
	if _, err := NewWithDependencies(testConfig(t, "test"), Dependencies{ManualGateway: &recordingManualGateway{}}); err == nil || !strings.Contains(err.Error(), "Funding Gateway") {
		t.Fatalf("NewWithDependencies() error=%v, want fail-closed Funding Gateway dependency", err)
	}
}

type recordingFundingGateway struct{}

func (*recordingFundingGateway) Handoff(context.Context, orders.Command) (orders.HandoffResult, error) {
	return orders.HandoffResult{Outcome: orders.HandoffAccepted}, nil
}

type readyMarketDataProbe struct{}

func (readyMarketDataProbe) CheckMarketDataRoute(context.Context) error { return nil }
func (readyMarketDataProbe) CheckRuntimeRoute(context.Context) error    { return nil }

func testDependencies() Dependencies {
	return Dependencies{SecretProvider: appSecretProvider{}, MarketDataProbe: readyMarketDataProbe{}, RuntimeProbe: readyMarketDataProbe{}, ManualGateway: &recordingManualGateway{}, FundingGateway: &recordingFundingGateway{}, WriteAuthority: NewWriteAuthority(WriteAuthorityFacts{RuntimeInherits: true, WriterUsage: true, FullCapabilityGrants: true, EffectiveTablePrivileges: true, EffectiveSequencePrivileges: true})}
}

type appSecretProvider struct{}

func (appSecretProvider) Check(context.Context, secret.Scope) error { return nil }
func (appSecretProvider) Put(context.Context, secretstore.Scope, secretstore.OperationID, []byte) (secretstore.OpaqueRef, error) {
	return secretstore.OpaqueRef(strings.Repeat("A", 43)), nil
}
func (appSecretProvider) LookupOperation(context.Context, secretstore.Scope, secretstore.OperationID) (secretstore.OpaqueRef, error) {
	return secretstore.OpaqueRef(strings.Repeat("A", 43)), nil
}
func (appSecretProvider) Delete(context.Context, secretstore.Scope, secretstore.OpaqueRef) error {
	return nil
}

func createManualAccount(t *testing.T, app *App, cookie *http.Cookie, csrf string) string {
	t.Helper()
	response := appRequest(t, app, cookie, csrf, http.MethodPost, "/api/v1/accounts", `{"venue":"binance","displayName":"Manual","productPolicy":["manual"],"routes":[{"routeId":"perp-usdt","product":"perp","settlementAsset":"USDT","routeProfileId":"binance.perp.usdt.demo.v1","requestedState":"active"}],"credentialBundle":{"schemaVersion":1,"venue":"binance","authKind":"api_key_secret","metadata":{},"secrets":{"apiKey":"test-only","apiSecret":"test-secret-only"}}}`, "manual-account")
	var body struct {
		Data struct {
			Account struct {
				AccountID string `json:"accountId"`
			} `json:"account"`
		} `json:"data"`
	}
	if response.Code != http.StatusCreated || json.Unmarshal(response.Body.Bytes(), &body) != nil || body.Data.Account.AccountID == "" {
		t.Fatalf("create account=%d body=%s", response.Code, response.Body.String())
	}
	validated, err := app.accounts.MarkValidated(context.Background(), body.Data.Account.AccountID, 1, session.DefaultPrincipalID)
	if err != nil {
		t.Fatalf("validate account: %v", err)
	}
	active, err := app.accounts.Activate(context.Background(), body.Data.Account.AccountID, validated.DesiredStateRevision, session.DefaultPrincipalID)
	if err != nil || active.State != "active" {
		t.Fatalf("activate account=%+v err=%v", active.Public(), err)
	}
	return active.ID
}

func appLogin(t *testing.T, app *App) (*http.Cookie, string) {
	t.Helper()
	response := appRequest(t, app, nil, "", http.MethodPost, "/api/v1/auth/login", `{"username":"operator","password":"correct horse battery staple"}`, "")
	if response.Code != http.StatusOK {
		t.Fatalf("login=%d body=%s", response.Code, response.Body.String())
	}
	var body struct {
		Data struct {
			Session struct {
				CSRFToken string `json:"csrfToken"`
			} `json:"session"`
		} `json:"data"`
	}
	if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil || body.Data.Session.CSRFToken == "" {
		t.Fatalf("login body=%s err=%v", response.Body.String(), err)
	}
	return response.Result().Cookies()[0], body.Data.Session.CSRFToken
}

func appRequest(t *testing.T, app *App, cookie *http.Cookie, csrf, method, path, body, idempotencyKey string) *httptest.ResponseRecorder {
	t.Helper()
	request := httptest.NewRequest(method, path, strings.NewReader(body))
	if cookie != nil {
		request.AddCookie(cookie)
	}
	if csrf != "" {
		request.Header.Set("X-CSRF-Token", csrf)
	}
	if idempotencyKey != "" {
		request.Header.Set("Idempotency-Key", idempotencyKey)
	}
	response := httptest.NewRecorder()
	app.browser.Handler.ServeHTTP(response, request)
	return response
}

type recordingIdentitySeeder struct {
	identity session.Identity
}

func (store *recordingIdentitySeeder) Upsert(_ context.Context, identity session.Identity) error {
	store.identity = identity
	return nil
}

func TestSeedBootstrapIdentityUsesConfiguredModeAndDigest(t *testing.T) {
	cfg := testConfig(t, "live")
	store := &recordingIdentitySeeder{}
	if err := seedBootstrapIdentity(context.Background(), store, cfg); err != nil {
		t.Fatal(err)
	}
	if store.identity.PrincipalID != session.DefaultPrincipalID || store.identity.Username != cfg.BootstrapUsername || store.identity.PasswordHash != cfg.BootstrapPasswordHash || store.identity.Mode != "live" {
		t.Fatalf("seeded identity=%+v", store.identity)
	}
}

func testConfig(t *testing.T, mode string) config.Config {
	t.Helper()
	passwordHash, err := session.HashPassword("correct horse battery staple")
	if err != nil {
		t.Fatal(err)
	}
	return config.Config{BrowserListenAddr: "127.0.0.1:18191", WorkListenAddr: "127.0.0.1:18192", BootstrapUsername: "operator", BootstrapPasswordHash: passwordHash, FundingWorkerTokenFile: "/protected/worker.token", FundingWorkerTokenDigest: sha256.Sum256([]byte("test-worker-token-with-at-least-32-bytes")), ManualPreviewSigningKeyFile: "/protected/" + mode + "/manual-preview-signing.key", ManualPreviewSigningKey: []byte("manual-preview-signing-key-with-at-least-32-bytes"), GatewayURL: "http://127.0.0.1:18082", GatewayTokenFile: "/protected/" + mode + "/gateway.token", GatewayToken: []byte("gateway-adapter-token-with-at-least-32-bytes"), Mode: mode}
}
