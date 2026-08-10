package app

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/QuantProcessing/funding-trader/backend/internal/auth"
	"github.com/QuantProcessing/funding-trader/backend/internal/config"
	boltertrader "github.com/QuantProcessing/funding-trader/backend/internal/integrations/boltertrader"
	"github.com/QuantProcessing/funding-trader/backend/internal/readiness"
	"github.com/QuantProcessing/funding-trader/backend/internal/readmodel"
)

func TestAppLiveBootstrapAndReadiness(t *testing.T) {
	if os.Getenv("RUN_APP_LIVE") != "1" {
		t.Skip("set RUN_APP_LIVE=1 for the explicit Postgres + Gateway bootstrap check")
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("load live app config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	application, err := New(ctx, cfg, "live-test", "local", logger)
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	t.Cleanup(func() {
		shutdownContext, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := application.Shutdown(shutdownContext); err != nil {
			t.Errorf("shutdown app: %v", err)
		}
	})
	if application.GatewayClient == nil || application.MarketData == nil ||
		application.ReadModel == nil || application.PositionEconomics == nil || application.Stream == nil ||
		application.OpportunityEngine == nil || application.ExecutionStore == nil ||
		application.ExecutionProjector == nil || application.ExecutionService == nil ||
		application.ExecutionMonitor == nil || application.LedgerFacts == nil ||
		application.LedgerProjection == nil || application.LedgerSync == nil ||
		application.LedgerReconciler == nil || application.LedgerCoordinator == nil ||
		application.LedgerQuery == nil {
		t.Fatal("production Gateway, marketdata, read-model, stream, opportunity, execution, or ledger dependency was not retained")
	}

	var recorder *httptest.ResponseRecorder
	readinessDeadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(readinessDeadline) {
		request := httptest.NewRequest(http.MethodGet, "/health/ready", nil)
		request.Header.Set("X-Request-Id", "app-live-readiness")
		recorder = httptest.NewRecorder()
		application.Server.Handler.ServeHTTP(recorder, request)
		if recorder.Code == http.StatusOK {
			break
		}
		time.Sleep(25 * time.Millisecond)
	}
	if recorder.Code != http.StatusOK {
		t.Fatalf("readiness status=%d body=%s", recorder.Code, recorder.Body.String())
	}
	var response struct {
		Data readiness.Result `json:"data"`
	}
	if err := json.Unmarshal(recorder.Body.Bytes(), &response); err != nil {
		t.Fatalf("decode readiness: %v", err)
	}
	if !response.Data.Ready {
		t.Fatalf("application not ready: %+v", response.Data.Components)
	}
	publicData := response.Data.Components["publicData"]
	if !publicData.Ready || publicData.Details["mode"] != string(cfg.Mode) ||
		publicData.Details["accounts"] == "0" || publicData.Details["instruments"] == "0" {
		t.Fatalf("publicData readiness=%+v", publicData)
	}
	t.Logf("publicData accounts=%s instruments=%s", publicData.Details["accounts"], publicData.Details["instruments"])
	executionComponent := response.Data.Components["execution"]
	if !executionComponent.Ready ||
		executionComponent.Details["privateEventsReady"] != "true" ||
		executionComponent.Details["recovering"] != "false" {
		t.Fatalf("execution readiness=%+v", executionComponent)
	}
	writeAcceptance := boltertrader.CheckTwoLegWriteAcceptance(ctx, application.GatewayClient)
	if executionComponent.Details["twoLegWriteReady"] != boolString(writeAcceptance.Ready) ||
		executionComponent.Details["twoLegWriteReason"] != writeAcceptance.Reason {
		t.Fatalf("two-leg write readiness=%+v acceptance=%+v",
			executionComponent, writeAcceptance)
	}
	if !writeAcceptance.Ready && writeAcceptance.Reason == "" {
		t.Fatalf("blocked two-leg write acceptance has no stable reason: %+v", writeAcceptance)
	}
	t.Logf("twoLegWrite ready=%t reason=%s eligibleVenues=%d requiredVenues=2",
		writeAcceptance.Ready, writeAcceptance.Reason, writeAcceptance.EligibleVenueCount)
	if err := application.OpportunityEngine.Refresh(ctx); err != nil {
		t.Fatalf("opportunity engine live refresh: %v", err)
	}

	var engineReady bool
	engineDeadline := time.Now().Add(30 * time.Second)
	for time.Now().Before(engineDeadline) {
		status := application.OpportunityEngine.Status()
		if !status.LastRefresh.IsZero() {
			if !status.Ready {
				t.Fatalf("opportunity engine degraded after refresh: %+v", status)
			}
			engineReady = true
			break
		}
		time.Sleep(50 * time.Millisecond)
	}
	if !engineReady {
		t.Fatalf("opportunity engine did not complete a live refresh: %+v publicData=%+v", application.OpportunityEngine.Status(), publicData.Details)
	}

	loginPayload, err := json.Marshal(map[string]string{
		"email": cfg.BootstrapEmail, "password": cfg.BootstrapPassword,
	})
	if err != nil {
		t.Fatal(err)
	}
	loginRequest := httptest.NewRequest(http.MethodPost, "/api/v1/auth/login", bytes.NewReader(loginPayload))
	loginRecorder := httptest.NewRecorder()
	application.Server.Handler.ServeHTTP(loginRecorder, loginRequest)
	if loginRecorder.Code != http.StatusOK {
		t.Fatalf("live login status=%d body=%s", loginRecorder.Code, loginRecorder.Body.String())
	}
	cookies := loginRecorder.Result().Cookies()
	if len(cookies) != 1 {
		t.Fatalf("live login cookies=%+v", cookies)
	}
	for _, path := range []string{
		"/api/v1/opportunities",
		"/api/v1/funding-rate-alerts",
		"/api/v1/positions",
		"/api/v1/activities",
		"/api/v1/audit-events",
	} {
		request := httptest.NewRequest(http.MethodGet, path, nil)
		request.AddCookie(cookies[0])
		response := httptest.NewRecorder()
		application.Server.Handler.ServeHTTP(response, request)
		if response.Code != http.StatusOK {
			t.Fatalf("%s status=%d body=%s", path, response.Code, response.Body.String())
		}
		var body struct {
			Data []json.RawMessage `json:"data"`
		}
		if err := json.Unmarshal(response.Body.Bytes(), &body); err != nil {
			t.Fatalf("%s decode: %v", path, err)
		}
	}
	metricsRequest := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	metricsRequest.AddCookie(cookies[0])
	metricsResponse := httptest.NewRecorder()
	application.Server.Handler.ServeHTTP(metricsResponse, metricsRequest)
	if metricsResponse.Code != http.StatusOK ||
		metricsResponse.Header().Get("Content-Type") != "text/plain; version=0.0.4; charset=utf-8" ||
		!strings.Contains(metricsResponse.Body.String(), "funding_trader_execution_state_current") ||
		!strings.Contains(metricsResponse.Body.String(), "funding_trader_resnapshot_required_events") {
		t.Fatalf("live metrics status=%d headers=%v body=%s",
			metricsResponse.Code, metricsResponse.Header(), metricsResponse.Body.String())
	}

	streamContext, cancelStream := context.WithTimeout(context.Background(), 250*time.Millisecond)
	defer cancelStream()
	streamRequest := httptest.NewRequest(http.MethodGet, "/api/v1/stream", nil).WithContext(streamContext)
	streamRequest.AddCookie(cookies[0])
	streamRecorder := httptest.NewRecorder()
	application.Server.Handler.ServeHTTP(streamRecorder, streamRequest)
	if streamRecorder.Code != http.StatusOK ||
		streamRecorder.Header().Get("Content-Type") != "text/event-stream" ||
		!strings.Contains(streamRecorder.Body.String(), "event: snapshot") ||
		!strings.Contains(streamRecorder.Body.String(), `"cursor"`) {
		t.Fatalf("live stream status=%d headers=%v body=%s", streamRecorder.Code, streamRecorder.Header(), streamRecorder.Body.String())
	}
}

func TestAppLiveLedgerRecoveryIsIdempotent(t *testing.T) {
	if os.Getenv("RUN_APP_LEDGER_RECOVERY_LIVE") != "1" {
		t.Skip("set RUN_APP_LEDGER_RECOVERY_LIVE=1 for the explicit Postgres + Gateway recovery check")
	}
	cfg, err := config.LoadFromEnv()
	if err != nil {
		t.Fatalf("load live app config: %v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()
	application, err := New(ctx, cfg, "live-test", "local", slog.New(slog.NewTextHandler(io.Discard, nil)))
	if err != nil {
		t.Fatalf("bootstrap app: %v", err)
	}
	t.Cleanup(func() {
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		if err := application.Shutdown(shutdownCtx); err != nil {
			t.Errorf("shutdown app: %v", err)
		}
	})

	result, err := RecoverClosedPositionLedger(
		ctx,
		application.ExecutionStore,
		readmodel.Scope{PrincipalID: auth.DefaultPrincipalID, Mode: string(cfg.Mode)},
		application.LedgerCoordinator,
		"USDT",
	)
	if err != nil {
		var recoveryErr LedgerRecoveryError
		if errors.As(err, &recoveryErr) {
			for _, item := range recoveryErr.Items {
				t.Logf("close=%s parent=%s errorType=%T error=%v", item.ExecutionID, item.ParentExecutionID, item.Err, item.Err)
			}
		}
		t.Fatalf("idempotent ledger recovery result=%+v error=%v", result, err)
	}
	if result.Failed != 0 || result.Reconciled+result.Incomplete != result.Eligible {
		t.Fatalf("ledger recovery did not account for every eligible close: %+v", result)
	}
}
